// Package turn es el ejecutor genérico de agentes que lanzan un proceso por
// turno (claude -p, agy, ...). El adaptador de cada CLI solo aporta un Driver:
// el comando del turno y el parser de su salida. Aquí viven la serialización
// de turnos, el Done retenido hasta que el proceso salió, la cancelación de
// un turno y el cierre definitivo del agente.
package turn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/proc"
)

// eventBuffer es la capacidad del canal de eventos.
const eventBuffer = 256

// Driver es lo que aporta el adaptador de una CLI.
type Driver interface {
	// Spec devuelve el comando de un turno. Reanudar la sesión
	// (Task.SessionID) es cosa del comando.
	Spec(task agent.Task) (proc.Spec, error)
	// Parse traduce una línea de la salida del turno. Puede devolver
	// eventos y un error a la vez; los eventos se entregan primero.
	Parse(line []byte) ([]agent.Event, error)
	// Name es el nombre de la CLI para los mensajes de error.
	Name() string
}

// turnState es el estado de un turno. cancel lo cierra Cancel; done lo cierra
// el propio turno cuando dejó de emitir eventos.
type turnState struct {
	proc   *proc.Process
	cancel chan struct{}
	done   chan struct{}
}

func (t *turnState) cancelled() bool {
	select {
	case <-t.cancel:
		return true
	default:
		return false
	}
}

func (t *turnState) finished() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// Runner implementa agent.Agent sobre un Driver.
type Runner struct {
	d      Driver
	events chan agent.Event
	// quit se cierra al empezar Close, para desbloquear envíos a un canal
	// lleno; closed, al terminar Close, para que las llamadas concurrentes
	// también esperen a que no quede ningún proceso.
	quit   chan struct{}
	closed chan struct{}

	mu         sync.Mutex
	status     agent.Status
	proc       *proc.Process
	last       *turnState // último turno lanzado; nil antes del primero
	cancelling int        // Cancel en curso: no se admiten turnos nuevos
	task       agent.Task
	sessionID  string
}

var _ agent.Agent = (*Runner)(nil)

// New crea un agente que ejecuta un proceso por turno con el driver d.
func New(d Driver) *Runner {
	return &Runner{
		d:      d,
		events: make(chan agent.Event, eventBuffer),
		quit:   make(chan struct{}),
		closed: make(chan struct{}),
	}
}

// Start lanza un turno.
func (r *Runner) Start(ctx context.Context, task agent.Task) error {
	r.mu.Lock()
	if err := r.startable(); err != nil {
		r.mu.Unlock()
		return err
	}
	prev := r.last
	r.mu.Unlock()

	// El turno anterior ya liberó su proceso, pero puede estar emitiendo su
	// Done: se espera para que los eventos de dos turnos nunca se mezclen.
	if prev != nil {
		select {
		case <-prev.done:
		case <-r.quit:
			return agent.ErrDone
		}
	}
	spec, err := r.d.Spec(task)
	if err != nil {
		return fmt.Errorf("%s: %w", r.d.Name(), err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.startable(); err != nil {
		return err
	}
	if r.last != prev {
		// Otro Start ganó mientras se esperaba.
		return agent.ErrBusy
	}
	p, err := proc.Start(ctx, spec)
	if err != nil {
		return fmt.Errorf("%s: %w", r.d.Name(), err)
	}
	t := &turnState{proc: p, cancel: make(chan struct{}), done: make(chan struct{})}
	r.proc = p
	r.last = t
	r.task = task
	r.status = agent.StatusRunning
	go r.runTurn(t, task.SessionID)
	return nil
}

// startable indica si se puede lanzar un turno. Requiere r.mu.
func (r *Runner) startable() error {
	switch {
	case r.status == agent.StatusDone:
		return agent.ErrDone
	case r.proc != nil, r.cancelling > 0:
		return agent.ErrBusy
	}
	return nil
}

// Send lanza un turno nuevo que reanuda la última sesión.
func (r *Runner) Send(msg string) error {
	r.mu.Lock()
	task, sessionID := r.task, r.sessionID
	r.mu.Unlock()
	if sessionID == "" {
		return agent.ErrNoSession
	}
	task.Prompt = msg
	task.SessionID = sessionID
	return r.Start(context.Background(), task)
}

// Events devuelve el canal de eventos de todos los turnos.
func (r *Runner) Events() <-chan agent.Event { return r.events }

// Status devuelve el estado actual.
func (r *Runner) Status() agent.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// PGID devuelve el grupo de procesos del turno en curso, o 0 si no hay.
func (r *Runner) PGID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.proc == nil {
		return 0
	}
	return r.proc.PGID()
}

// Pause congela el turno en curso.
func (r *Runner) Pause() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != agent.StatusRunning {
		return fmt.Errorf("%s: no se puede pausar en estado %s", r.d.Name(), r.status)
	}
	if err := r.proc.Pause(); err != nil {
		return fmt.Errorf("%s: %w", r.d.Name(), err)
	}
	r.status = agent.StatusPaused
	return nil
}

// Resume reanuda el turno pausado.
func (r *Runner) Resume() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status != agent.StatusPaused {
		return fmt.Errorf("%s: no se puede reanudar en estado %s", r.d.Name(), r.status)
	}
	if err := r.proc.Resume(); err != nil {
		return fmt.Errorf("%s: %w", r.d.Name(), err)
	}
	r.status = agent.StatusRunning
	return nil
}

// Cancel termina el turno en curso con todos sus descendientes. El agente
// queda en StatusIdle y se puede lanzar otro turno. El turno cancelado no
// emite Done, y cuando Cancel vuelve ya no hay procesos suyos ni eventos
// suyos pendientes en el canal. Sin turno en curso no hace nada.
func (r *Runner) Cancel() error {
	r.mu.Lock()
	t := r.last
	if t == nil || r.status == agent.StatusDone || t.finished() {
		r.mu.Unlock()
		return nil
	}
	if !t.cancelled() {
		close(t.cancel)
	}
	r.status = agent.StatusIdle
	r.cancelling++
	p := r.proc
	r.mu.Unlock()

	var err error
	if p != nil {
		err = p.Kill()
		// Desbloquea al lector si un descendiente que escapó del grupo
		// mantiene abierto stdout.
		p.Close()
	}
	select {
	case <-t.done:
	case <-r.quit:
		// Close se ocupa de esperar al turno y de cerrar el canal.
	}
	// El turno ya no emite: lo que quede en el buffer es suyo.
	r.drain()

	r.mu.Lock()
	r.cancelling--
	r.mu.Unlock()
	return err
}

// drain descarta los eventos pendientes del canal sin bloquear.
func (r *Runner) drain() {
	for {
		select {
		case _, ok := <-r.events:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// Close termina el agente: mata el turno en curso con todos sus
// descendientes y cierra el canal de eventos. Las llamadas siguientes o
// concurrentes esperan a que la primera termine.
func (r *Runner) Close() error {
	r.mu.Lock()
	if r.status == agent.StatusDone {
		r.mu.Unlock()
		<-r.closed
		return nil
	}
	r.status = agent.StatusDone
	p, t := r.proc, r.last
	close(r.quit)
	r.mu.Unlock()

	var err error
	if p != nil {
		err = p.Kill()
		p.Close()
	}
	// Aunque no haya proceso, el último turno puede estar emitiendo su Done:
	// cerrar el canal antes provocaría un envío a un canal cerrado.
	if t != nil {
		<-t.done
	}
	close(r.events)
	close(r.closed)
	return err
}

// emit envía un evento del turno t salvo que el turno haya sido cancelado o
// el agente esté cerrándose: un turno interrumpido no reporta nada más.
func (r *Runner) emit(t *turnState, ev agent.Event) {
	select {
	case <-r.quit:
		return
	case <-t.cancel:
		return
	default:
	}
	select {
	case r.events <- ev:
	case <-r.quit:
	case <-t.cancel:
	}
}

// runTurn lee la salida de un turno hasta EOF y la traduce a eventos. El
// Done se retiene hasta que el proceso salió y el estado quedó actualizado,
// para que quien lo reciba pueda lanzar el turno siguiente en el acto.
func (r *Runner) runTurn(t *turnState, resumeID string) {
	defer close(t.done)
	p := t.proc
	name := r.d.Name()

	var done *agent.Done
	started := false
	br := bufio.NewReader(p.Stdout())
	for {
		line, readErr := br.ReadBytes('\n')
		if len(line) > 0 {
			events, err := r.d.Parse(line)
			for _, ev := range events {
				switch e := ev.(type) {
				case agent.Started:
					started = true
					r.mu.Lock()
					r.sessionID = e.SessionID
					r.mu.Unlock()
				case agent.Done:
					done = &e
					continue
				}
				r.emit(t, ev)
			}
			if err != nil {
				r.emit(t, agent.Error{Err: err})
			}
		}
		if readErr != nil {
			// os.ErrClosed: Cancel o Close cerraron stdout para desbloquear
			// la lectura.
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, os.ErrClosed) {
				r.emit(t, agent.Error{Err: fmt.Errorf("%s: leer salida: %w", name, readErr)})
			}
			break
		}
	}
	waitErr := p.Wait()
	p.Close()

	r.mu.Lock()
	closing := r.status == agent.StatusDone
	cancelled := t.cancelled()
	r.proc = nil
	switch {
	case closing, cancelled:
		// El estado ya es el definitivo (Done por Close, Idle por Cancel) y
		// el turno no emite Done.
	case done == nil || done.IsError:
		r.status = agent.StatusError
	default:
		r.status = agent.StatusIdle
	}
	sessionID := r.sessionID
	r.mu.Unlock()
	if closing || cancelled {
		return
	}

	// La CLI falla antes de anunciar la sesión cuando no puede reanudarla
	// (verificado con claude 2.1.302).
	if resumeID != "" && !started && (done == nil || done.IsError) {
		r.emit(t, agent.Error{Err: fmt.Errorf("%w: %s", agent.ErrSessionNotFound, resumeID)})
	}
	if done == nil {
		err := fmt.Errorf("%s terminó sin resultado (%v)", name, waitErr)
		if tail := strings.TrimSpace(p.Stderr()); tail != "" {
			err = fmt.Errorf("%w: %s", err, tail)
		}
		r.emit(t, agent.Error{Err: err})
		done = &agent.Done{SessionID: sessionID, IsError: true}
	}
	r.emit(t, *done)
}
