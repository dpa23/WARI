// Package claude es el adaptador de la CLI oficial `claude` (Claude Code).
// Cada turno es un proceso `claude -p` en modo stream-json; los turnos
// siguientes reanudan la sesión con --resume.
package claude

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/proc"
)

// eventBuffer es la capacidad del canal de eventos.
const eventBuffer = 256

// Claude implementa agent.Agent sobre la CLI `claude`.
type Claude struct {
	bin    string
	events chan agent.Event
	// quit se cierra al empezar Kill, para desbloquear envíos a un canal
	// lleno; killed, al terminar Kill, para que las llamadas concurrentes
	// también esperen a que no quede ningún proceso.
	quit   chan struct{}
	killed chan struct{}

	mu        sync.Mutex
	status    agent.Status
	proc      *proc.Process
	turnDone  chan struct{}
	task      agent.Task
	sessionID string
}

var _ agent.Agent = (*Claude)(nil)

// New crea un agente que usa el ejecutable bin; vacío busca `claude` en PATH.
func New(bin string) *Claude {
	if bin == "" {
		bin = "claude"
	}
	return &Claude{
		bin:    bin,
		events: make(chan agent.Event, eventBuffer),
		quit:   make(chan struct{}),
		killed: make(chan struct{}),
	}
}

// Args devuelve los argumentos de `claude` para un turno. El prompt va
// después de "--" para que nunca se interprete como un flag.
func Args(t agent.Task) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if t.SessionID != "" {
		args = append(args, "--resume", t.SessionID)
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	switch t.Permissions {
	case agent.PermissionAcceptEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case agent.PermissionPlan:
		args = append(args, "--permission-mode", "plan")
	default:
		args = append(args, "--permission-mode", "manual")
	}
	// Sin superficie de aprobación, lo que requiera permiso se deniega y
	// llega como NeedsApproval en lugar de bloquear el turno.
	args = append(args, "--permission-prompts", "none")
	return append(args, "--", t.Prompt)
}

// Start lanza un turno.
func (c *Claude) Start(ctx context.Context, task agent.Task) error {
	c.mu.Lock()
	if err := c.startable(); err != nil {
		c.mu.Unlock()
		return err
	}
	prev := c.turnDone
	c.mu.Unlock()

	// El turno anterior ya liberó su proceso, pero puede estar emitiendo su
	// Done: se espera para que los eventos de dos turnos nunca se mezclen.
	if prev != nil {
		select {
		case <-prev:
		case <-c.quit:
			return agent.ErrDone
		}
	}
	path, err := exec.LookPath(c.bin)
	if err != nil {
		return fmt.Errorf("claude: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.startable(); err != nil {
		return err
	}
	if c.turnDone != prev {
		// Otro Start ganó mientras se esperaba.
		return agent.ErrBusy
	}
	p, err := proc.Start(ctx, proc.Spec{Path: path, Args: Args(task), Dir: task.Dir})
	if err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	c.proc = p
	c.turnDone = make(chan struct{})
	c.task = task
	c.status = agent.StatusRunning
	go c.runTurn(p, c.turnDone)
	return nil
}

// startable indica si se puede lanzar un turno. Requiere c.mu.
func (c *Claude) startable() error {
	switch {
	case c.status == agent.StatusDone:
		return agent.ErrDone
	case c.proc != nil:
		return agent.ErrBusy
	}
	return nil
}

// Send lanza un turno nuevo que reanuda la última sesión.
func (c *Claude) Send(msg string) error {
	c.mu.Lock()
	task, sessionID := c.task, c.sessionID
	c.mu.Unlock()
	if sessionID == "" {
		return agent.ErrNoSession
	}
	task.Prompt = msg
	task.SessionID = sessionID
	return c.Start(context.Background(), task)
}

// Events devuelve el canal de eventos de todos los turnos.
func (c *Claude) Events() <-chan agent.Event { return c.events }

// Status devuelve el estado actual.
func (c *Claude) Status() agent.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// SessionID devuelve la última sesión anunciada por la CLI.
func (c *Claude) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// PGID devuelve el grupo de procesos del turno en curso, o 0 si no hay.
func (c *Claude) PGID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == nil {
		return 0
	}
	return c.proc.PGID()
}

// Pause congela el turno en curso.
func (c *Claude) Pause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status != agent.StatusRunning {
		return fmt.Errorf("claude: no se puede pausar en estado %s", c.status)
	}
	if err := c.proc.Pause(); err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	c.status = agent.StatusPaused
	return nil
}

// Resume reanuda el turno pausado.
func (c *Claude) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status != agent.StatusPaused {
		return fmt.Errorf("claude: no se puede reanudar en estado %s", c.status)
	}
	if err := c.proc.Resume(); err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	c.status = agent.StatusRunning
	return nil
}

// Kill termina el turno en curso con todos sus descendientes y cierra el
// canal de eventos. Las llamadas siguientes o concurrentes esperan a que la
// primera termine.
func (c *Claude) Kill() error {
	c.mu.Lock()
	if c.status == agent.StatusDone {
		c.mu.Unlock()
		<-c.killed
		return nil
	}
	c.status = agent.StatusDone
	p, turnDone := c.proc, c.turnDone
	close(c.quit)
	c.mu.Unlock()

	var err error
	if p != nil {
		err = p.Kill()
		// Desbloquea al lector si un descendiente que escapó del grupo
		// mantiene abierto stdout.
		p.Close()
	}
	// Aunque no haya proceso, el último turno puede estar emitiendo su Done:
	// cerrar el canal antes provocaría un envío a un canal cerrado.
	if turnDone != nil {
		<-turnDone
	}
	close(c.events)
	close(c.killed)
	return err
}

// emit envía un evento salvo que el agente esté terminando. Si Kill ya
// empezó, el evento se descarta: un turno interrumpido no reporta nada más.
func (c *Claude) emit(ev agent.Event) {
	select {
	case <-c.quit:
		return
	default:
	}
	select {
	case c.events <- ev:
	case <-c.quit:
	}
}

// runTurn lee la salida de un turno hasta EOF y la traduce a eventos. El
// Done se retiene hasta que el proceso salió y el estado quedó actualizado,
// para que quien lo reciba pueda lanzar el turno siguiente en el acto.
func (c *Claude) runTurn(p *proc.Process, turnDone chan struct{}) {
	defer close(turnDone)

	var done *agent.Done
	r := bufio.NewReader(p.Stdout())
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			events, err := ParseLine(line)
			for _, ev := range events {
				switch e := ev.(type) {
				case agent.Started:
					c.mu.Lock()
					c.sessionID = e.SessionID
					c.mu.Unlock()
				case agent.Done:
					done = &e
					continue
				}
				c.emit(ev)
			}
			if err != nil {
				c.emit(agent.Error{Err: err})
			}
		}
		if readErr != nil {
			// os.ErrClosed: Kill cerró stdout para desbloquear la lectura.
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, os.ErrClosed) {
				c.emit(agent.Error{Err: fmt.Errorf("claude: leer salida: %w", readErr)})
			}
			break
		}
	}
	waitErr := p.Wait()
	p.Close()

	c.mu.Lock()
	killed := c.status == agent.StatusDone
	c.proc = nil
	switch {
	case killed:
		// Terminado por Kill: el estado ya es definitivo y no hay Done.
	case done == nil || done.IsError:
		c.status = agent.StatusError
	default:
		c.status = agent.StatusIdle
	}
	sessionID := c.sessionID
	c.mu.Unlock()
	if killed {
		return
	}

	if done == nil {
		err := fmt.Errorf("claude terminó sin resultado (%v)", waitErr)
		if tail := strings.TrimSpace(p.Stderr()); tail != "" {
			err = fmt.Errorf("%w: %s", err, tail)
		}
		c.emit(agent.Error{Err: err})
		done = &agent.Done{SessionID: sessionID, IsError: true}
	}
	c.emit(*done)
}
