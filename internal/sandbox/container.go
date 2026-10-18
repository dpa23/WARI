package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dpa23/wari/internal/proc"
)

// Stream identifica el canal de una línea de log.
type Stream string

const (
	// Stdout y Stderr son los canales del proceso dentro del contenedor.
	Stdout Stream = "stdout"
	Stderr Stream = "stderr"
	// Runtime es lo que escribe el propio CLI de contenedores (imagen que
	// falta, error al crear el contenedor).
	RuntimeLog Stream = "runtime"
)

// Line es una línea de log de una ejecución.
type Line struct {
	Stream Stream
	Text   string
}

const (
	logBuffer    = 256
	maxLine      = 64 << 10
	stderrPoll   = 50 * time.Millisecond
	rmTimeout    = 30 * time.Second
	abandonAfter = 2 * time.Second
)

// Container implementa Sandbox con Podman o Docker.
type Container struct {
	// Runtime es el CLI a usar; si es nil se detecta en la primera ejecución.
	Runtime *Runtime
	// Prefer elige "docker" antes que "podman" al detectar.
	Prefer string
	// RunsDir es la carpeta de salidas; vacío usa <cache del usuario>/wari/runs.
	RunsDir string

	mu sync.Mutex
}

var _ Sandbox = (*Container)(nil)

func (c *Container) runtime(ctx context.Context) (Runtime, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Runtime == nil {
		rt, err := DetectRuntime(ctx, c.Prefer)
		if err != nil {
			return Runtime{}, err
		}
		c.Runtime = &rt
	}
	return *c.Runtime, nil
}

func (c *Container) runsDir() (string, error) {
	if c.RunsDir != "" {
		return c.RunsDir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("sandbox: carpeta de caché: %w", err)
	}
	return filepath.Join(base, "wari", "runs"), nil
}

// HasImage dice si la imagen ya está descargada; nunca la descarga.
func (c *Container) HasImage(ctx context.Context, image string) bool {
	rt, err := c.runtime(ctx)
	if err != nil || !imageRe.MatchString(image) {
		return false
	}
	_, err = quick(ctx, 10*time.Second, rt.Path, "image", "inspect", "--format", "{{.Id}}", image)
	return err == nil
}

// Run valida el Spec, crea la carpeta de salida y lanza el contenedor.
func (c *Container) Run(ctx context.Context, spec Spec) (*Run, error) {
	rt, err := c.runtime(ctx)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(spec.Root)
	if err != nil {
		return nil, fmt.Errorf("sandbox: raíz del proyecto: %w", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("sandbox: la raíz %q no es un directorio", spec.Root)
	}
	spec.Root = root
	for i := range spec.ExtraMounts {
		spec.ExtraMounts[i].Host = filepath.Clean(spec.ExtraMounts[i].Host)
	}
	r, err := spec.resolve(rt.Name)
	if err != nil {
		return nil, err
	}

	id, err := newID()
	if err != nil {
		return nil, err
	}
	runs, err := c.runsDir()
	if err != nil {
		return nil, err
	}
	outDir := filepath.Join(runs, id)
	if err := safeMountPath(outDir); err != nil {
		return nil, fmt.Errorf("sandbox: carpeta de salida: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return nil, fmt.Errorf("sandbox: crear carpeta de salida: %w", err)
	}

	name := "wari-" + id
	p, err := proc.Start(ctx, proc.Spec{Path: rt.Path, Args: buildArgs(r, name, id, outDir)})
	if err != nil {
		os.RemoveAll(outDir)
		return nil, err
	}

	run := &Run{
		ID:       id,
		Name:     name,
		Root:     root,
		OutDir:   outDir,
		Image:    r.Image,
		Command:  r.Command,
		rt:       rt,
		p:        p,
		logs:     make(chan Line, logBuffer),
		logsDone: make(chan struct{}),
		abandon:  make(chan struct{}),
	}
	go run.pump()
	go func() {
		select {
		case <-ctx.Done():
			run.Stop()
		case <-p.Done():
		}
	}()
	return run, nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("sandbox: id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Run es una ejecución en curso o terminada.
type Run struct {
	ID      string
	Name    string // nombre único del contenedor
	Root    string // proyecto real, intacto
	OutDir  string // salidas en el anfitrión; dentro del contenedor es /out
	Image   string
	Command []string

	rt       Runtime
	p        *proc.Process
	logs     chan Line
	logsDone chan struct{}

	abandon     chan struct{}
	abandonOnce sync.Once

	stopOnce sync.Once
	stopErr  error
}

// Logs devuelve el flujo de líneas de stdout, stderr y del runtime. Se cierra
// cuando todo se leyó tras terminar la ejecución. Hay que leerlo hasta el
// final o llamar a Stop: un lector ausente frena al contenedor.
func (r *Run) Logs() <-chan Line { return r.logs }

// Wait espera a que el contenedor termine y devuelve su error de salida
// (*exec.ExitError con el código del contenedor). No espera a Logs.
func (r *Run) Wait() error { return r.p.Wait() }

// Done se cierra cuando el proceso terminó y Logs fue vaciado y cerrado.
func (r *Run) Done() <-chan struct{} { return r.logsDone }

// Stop detiene la ejecución: mata el grupo del CLI con sus descendientes y
// después ejecuta `<runtime> rm -f [-t 0] -v <nombre>` para no dejar el contenedor
// (ni su volumen anónimo) vivo aunque el CLI haya muerto sin avisarle. Es
// idempotente.
func (r *Run) Stop() error {
	r.stopOnce.Do(func() {
		var errs []error
		if err := r.p.Kill(); err != nil {
			errs = append(errs, err)
		}
		out, err := quick(context.Background(), rmTimeout, r.rt.Path, r.rmArgs()...)
		if err != nil && !noSuchContainer(out+err.Error()) {
			errs = append(errs, err)
		}
		// Si nadie lee los logs, no se espera para siempre.
		select {
		case <-r.logsDone:
		case <-time.After(abandonAfter):
			r.abandonOnce.Do(func() { close(r.abandon) })
			r.p.Close()
		}
		r.stopErr = errors.Join(errs...)
	})
	return r.stopErr
}

// rmArgs son los argumentos de `rm`. Podman espera 10 s antes de matar si el
// proceso ignora SIGTERM (un PID 1 sin manejador); con -t 0 es inmediato.
// Docker mata de inmediato con -f.
func (r *Run) rmArgs() []string {
	if r.rt.Name == "podman" {
		return []string{"rm", "-f", "-t", "0", "-v", r.Name}
	}
	return []string{"rm", "-f", "-v", r.Name}
}

// Kill es un alias de Stop.
func (r *Run) Kill() error { return r.Stop() }

// Cleanup borra la carpeta de salidas. Hay que llamarlo con la ejecución
// terminada; lo promovido al proyecto ya se copió.
func (r *Run) Cleanup() error {
	select {
	case <-r.p.Done():
	default:
		return errors.New("sandbox: la ejecución sigue en marcha")
	}
	if filepath.Base(r.OutDir) != r.ID {
		return fmt.Errorf("sandbox: carpeta de salida inesperada %q", r.OutDir)
	}
	return os.RemoveAll(r.OutDir)
}

func noSuchContainer(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "no such") || strings.Contains(s, "no container")
}

// emit entrega una línea o la descarta si Stop abandonó a los lectores.
func (r *Run) emit(s Stream, text string) {
	select {
	case r.logs <- Line{Stream: s, Text: text}:
	case <-r.abandon:
	}
}

// pump transmite stdout y stderr hasta que ambos terminan y cierra Logs.
func (r *Run) pump() {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sp := splitter{emit: func(t string) { r.emit(Stdout, t) }}
		buf := make([]byte, 32<<10)
		out := r.p.Stdout()
		for {
			n, err := out.Read(buf)
			sp.write(buf[:n])
			if err != nil {
				break
			}
		}
		sp.flush()
	}()
	go func() {
		defer wg.Done()
		r.tailStderr()
	}()
	wg.Wait()
	<-r.p.Done()
	if tail := strings.TrimRight(r.p.Stderr(), "\r\n"); tail != "" {
		for _, l := range strings.Split(tail, "\n") {
			r.emit(RuntimeLog, strings.TrimRight(l, "\r"))
		}
	}
	close(r.logs)
	close(r.logsDone)
}

// tailStderr sigue el archivo donde el guion del contenedor redirige el error
// estándar del comando. Termina cuando el proceso terminó y se leyó todo.
func (r *Run) tailStderr() {
	sp := splitter{emit: func(t string) { r.emit(Stderr, t) }}
	defer sp.flush()
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	buf := make([]byte, 32<<10)
	tick := time.NewTicker(stderrPoll)
	defer tick.Stop()
	for {
		finished := false
		select {
		case <-r.p.Done():
			finished = true
		default:
		}
		if f == nil {
			f, _ = os.Open(filepath.Join(r.OutDir, stderrFile))
		}
		if f != nil {
			for {
				n, err := f.Read(buf)
				sp.write(buf[:n])
				if err != nil || n == 0 {
					break
				}
			}
		}
		if finished {
			return
		}
		select {
		case <-tick.C:
		case <-r.p.Done():
		}
	}
}

// splitter parte un flujo en líneas; las más largas que maxLine se trocean.
type splitter struct {
	buf  []byte
	emit func(string)
}

func (s *splitter) write(p []byte) {
	s.buf = append(s.buf, p...)
	for {
		i := indexNL(s.buf)
		if i < 0 {
			break
		}
		s.emit(strings.TrimSuffix(string(s.buf[:i]), "\r"))
		s.buf = s.buf[i+1:]
	}
	for len(s.buf) > maxLine {
		s.emit(string(s.buf[:maxLine]))
		s.buf = s.buf[maxLine:]
	}
	if len(s.buf) == 0 {
		s.buf = nil
	}
}

func (s *splitter) flush() {
	if len(s.buf) > 0 {
		s.emit(strings.TrimSuffix(string(s.buf), "\r"))
		s.buf = nil
	}
}

func indexNL(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}
