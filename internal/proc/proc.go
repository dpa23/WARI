// Package proc lanza procesos como líderes de su propio grupo y los controla
// junto con todos sus descendientes: pausar, reanudar y terminar sin dejar
// huérfanos. El código de cada plataforma vive en proc_<so>.go.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// DefaultKillGrace es cuánto espera Kill entre la terminación ordenada y la
// forzada.
const DefaultKillGrace = 2 * time.Second

// stderrDrain acota cuánto se espera a que stderr llegue a EOF después de
// que el líder salió: un descendiente que escapó del grupo podría mantenerlo
// abierto.
const stderrDrain = time.Second

// stderrTail es cuántos bytes finales de stderr se conservan.
const stderrTail = 8 << 10

// ErrNotRunning indica que el grupo ya no tiene procesos.
var ErrNotRunning = errors.New("proc: el grupo ya terminó")

// Spec describe el comando a lanzar.
type Spec struct {
	Path string
	Args []string
	// Dir es el directorio de trabajo; vacío usa el actual.
	Dir string
	// Env es el entorno completo; nil hereda el del proceso actual.
	Env []string
	// KillGrace es la espera entre la terminación ordenada y la forzada;
	// 0 usa DefaultKillGrace.
	KillGrace time.Duration
}

// Process es un proceso lanzado como líder de su propio grupo. Stdin es
// /dev/null, stdout se lee con Stdout y de stderr se guarda el final.
//
// Cuando el líder termina, lo que quede del grupo (procesos en segundo plano,
// servidores auxiliares) quedaría huérfano: recibe la terminación ordenada y,
// si sigue vivo al vencer el plazo de gracia, la forzada. Mientras el grupo
// tenga miembros su PGID no puede reutilizarse, así que esas señales son
// seguras; una vez cerrado Done, Pause, Resume y Kill ya no envían señales.
type Process struct {
	cmd    *exec.Cmd
	stdout *os.File
	stderr *os.File
	tail   *tailBuffer
	grace  time.Duration

	done    chan struct{}
	waitErr error

	killMu sync.Mutex
}

// Start lanza el comando. Si ctx se cancela, el grupo completo se termina
// como con Kill.
func Start(ctx context.Context, spec Spec) (*Process, error) {
	if spec.Path == "" {
		return nil, errors.New("proc: falta Path")
	}
	// Pipes propios en lugar de StdoutPipe o un io.Writer: así Wait vuelve
	// en cuanto el líder sale, sin esperar a lectores ni a restos del grupo.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("proc: crear pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, fmt.Errorf("proc: crear pipe: %w", err)
	}

	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdout = outW
	cmd.Stderr = errW
	setGroup(cmd)

	err = cmd.Start()
	// El hijo tiene sus copias; las nuestras impedirían llegar a EOF.
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		return nil, fmt.Errorf("proc: lanzar %s: %w", spec.Path, err)
	}

	grace := spec.KillGrace
	if grace <= 0 {
		grace = DefaultKillGrace
	}
	p := &Process{
		cmd:    cmd,
		stdout: outR,
		stderr: errR,
		tail:   &tailBuffer{max: stderrTail},
		grace:  grace,
		done:   make(chan struct{}),
	}

	stderrDone := make(chan struct{})
	go func() {
		io.Copy(p.tail, errR)
		close(stderrDone)
	}()
	go func() {
		p.waitErr = cmd.Wait()
		cleanupGroup(p)
		timer := time.NewTimer(stderrDrain)
		select {
		case <-stderrDone:
		case <-timer.C:
			errR.Close()
		}
		timer.Stop()
		close(p.done)
	}()
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				p.Kill()
			case <-p.done:
			}
		}()
	}
	return p, nil
}

// PGID devuelve el identificador del grupo. En Unix es el PID del líder.
func (p *Process) PGID() int { return p.cmd.Process.Pid }

// Stdout devuelve la salida estándar. Llega a EOF cuando todos los procesos
// que la heredaron la cierran.
func (p *Process) Stdout() io.Reader { return p.stdout }

// Stderr devuelve los últimos bytes escritos en la salida de error. Después
// de Wait está completo.
func (p *Process) Stderr() string { return p.tail.String() }

// Done se cierra cuando el líder terminó, fue recogido y el resto del grupo
// recibió la señal de terminación.
func (p *Process) Done() <-chan struct{} { return p.done }

// Wait espera a Done y devuelve el error de salida del líder.
func (p *Process) Wait() error {
	<-p.done
	return p.waitErr
}

// Pause congela el grupo completo.
func (p *Process) Pause() error {
	if p.exited() {
		return ErrNotRunning
	}
	return stopGroup(p)
}

// Resume reanuda un grupo pausado.
func (p *Process) Resume() error {
	if p.exited() {
		return ErrNotRunning
	}
	return contGroup(p)
}

// Kill termina el grupo completo: primero de forma ordenada (también si
// estaba pausado), y si el líder no sale dentro del plazo de gracia, de forma
// forzada. Vuelve cuando el líder fue recogido y el resto del grupo recibió
// la señal forzada. Se puede llamar más de una vez y después de que el
// proceso terminó.
func (p *Process) Kill() error {
	p.killMu.Lock()
	defer p.killMu.Unlock()
	if p.exited() {
		return nil
	}

	if err := terminateGroup(p); err != nil {
		return fmt.Errorf("proc: terminar grupo %d: %w", p.PGID(), err)
	}
	timer := time.NewTimer(p.grace)
	defer timer.Stop()
	select {
	case <-p.done:
		// La goroutine de Wait ya mató al resto del grupo.
		return nil
	case <-timer.C:
	}
	if err := killGroup(p); err != nil {
		return fmt.Errorf("proc: matar grupo %d: %w", p.PGID(), err)
	}
	<-p.done
	return nil
}

// Close cierra los extremos de lectura de stdout y stderr. Sirve para
// desbloquear a un lector cuando un descendiente que escapó del grupo
// mantiene el pipe abierto.
func (p *Process) Close() error {
	p.stderr.Close()
	return p.stdout.Close()
}

// cleanupGroup termina lo que quede del grupo tras la salida del líder:
// primero de forma ordenada, y de forma forzada solo si algo sigue vivo al
// vencer el plazo de gracia. Sin restos no envía ninguna señal.
func cleanupGroup(p *Process) {
	if !groupAlive(p) {
		return
	}
	terminateGroup(p)
	deadline := time.Now().Add(p.grace)
	for groupAlive(p) {
		if time.Now().After(deadline) {
			killGroup(p)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *Process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// tailBuffer es un io.Writer concurrente que conserva los últimos max bytes.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0:0], t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
