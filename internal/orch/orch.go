// Package orch supervisa a los agentes de WARI: limita cuántos corren a la
// vez (cola FIFO para el resto), aplica presupuestos de turnos, tokens y
// tiempo, y ofrece pausa, parada por agente y parada global. Solo conoce el
// contrato de internal/agent; no sabe nada del editor ni de ninguna CLI.
//
// Concurrencia: todo el estado vive tras un único mutex y las llamadas a los
// agentes (Start, Send, Cancel, Pause, Close...) se hacen siempre con el
// mutex suelto, porque pueden tardar (matar un grupo de procesos). La
// goroutine que lee los eventos de un agente nunca llama a un agente en línea:
// lo que haya que hacer (cancelar por presupuesto, arrancar al siguiente de la
// cola) lo lanza en otra goroutine.
package orch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dpa23/wari/internal/agent"
)

// updateBuffer es la capacidad del canal de Updates.
const updateBuffer = 4096

var (
	// ErrNotFound indica un ID desconocido.
	ErrNotFound = errors.New("orch: agente desconocido")
	// ErrClosed indica que el supervisor ya se cerró.
	ErrClosed = errors.New("orch: supervisor cerrado")
	// ErrState indica que la operación no es válida en el estado actual.
	ErrState = errors.New("orch: operación no válida en el estado actual")
	// ErrBudget indica que el agente superó su presupuesto y no acepta más
	// turnos.
	ErrBudget = errors.New("orch: presupuesto superado")
)

// Budget limita a un agente. El valor cero de un campo significa sin límite.
//
// Semántica del límite. A mitad de un turno, el agente se corta solo al
// superar el límite (estrictamente más): llegar justo a MaxTokens o a
// MaxDuration no interrumpe el turno en curso. Pero con el límite ya
// alcanzado (tokens >= MaxTokens, tiempo >= MaxDuration o turnos >= MaxTurns),
// el siguiente Send se rechaza con ErrBudget y el agente queda
// BudgetExceeded.
type Budget struct {
	// MaxTurns es el máximo de turnos (cada Start o Send cuenta uno).
	MaxTurns int
	// MaxDuration es el tiempo máximo con un turno corriendo. No cuenta el
	// tiempo en cola, en pausa ni entre turnos.
	MaxDuration time.Duration
	// MaxTokens es el máximo de tokens de entrada más salida, sumados.
	MaxTokens int64
}

// ID identifica a un agente dentro del supervisor. Los IDs válidos empiezan
// en 1.
type ID int

// State es el estado de un agente supervisado.
type State int

const (
	// Queued: espera un cupo.
	Queued State = iota
	// Running: tiene un turno en curso.
	Running
	// Paused: el turno está congelado. Conserva su cupo.
	Paused
	// Idle: terminó un turno y espera otro (Send).
	Idle
	// Stopped: detenido por el usuario. Admite Send.
	Stopped
	// Failed: el último turno falló. Admite Send.
	Failed
	// BudgetExceeded: superó su presupuesto. No admite más Send.
	BudgetExceeded
)

func (s State) String() string {
	switch s {
	case Queued:
		return "queued"
	case Running:
		return "running"
	case Paused:
		return "paused"
	case Idle:
		return "idle"
	case Stopped:
		return "stopped"
	case Failed:
		return "failed"
	case BudgetExceeded:
		return "budget-exceeded"
	}
	return "unknown"
}

// Snapshot es una copia del estado de un agente.
type Snapshot struct {
	ID    ID
	Name  string
	State State
	// Turns cuenta los turnos lanzados (Start y Send).
	Turns  int
	Tokens int64
	// CostUSD suma el costo que reportan los Usage.
	CostUSD float64
	// Elapsed es el tiempo acumulado con un turno corriendo.
	Elapsed time.Duration
	// Reason explica Failed, Stopped o BudgetExceeded; vacío si no aplica.
	Reason    string
	SessionID string
}

// Update es una novedad de un agente: un evento suyo o un cambio de estado.
type Update struct {
	ID ID
	// Event es nil si la novedad es solo un cambio de estado.
	Event agent.Event
	// Snapshot es el estado del agente justo después de la novedad.
	Snapshot Snapshot
}

type entry struct {
	id     ID
	name   string
	a      agent.Agent
	task   agent.Task
	budget Budget

	state   State
	holding bool // ocupa un cupo (también mientras se cancela su turno)
	turns   int
	tokens  int64
	cost    float64
	reason  string
	session string

	turnSeq int    // se incrementa en cada turno lanzado
	turnErr string // último agent.Error del turno en curso

	// pendSend y pendMsg: lo que hay que lanzar cuando salga de la cola.
	pendSend bool
	pendMsg  string

	acc      time.Duration // tiempo corrido acumulado, sin el tramo actual
	runSince time.Time     // inicio del tramo actual; cero si no corre
	timer    *time.Timer
	timerSeq int

	// launching está abierto mientras un turno reservado todavía no terminó
	// su llamada a Start/Send; cancelAndRelease lo espera para que Cancel vea
	// el turno ya lanzado.
	launching chan struct{}
}

// launch es un turno ya reservado (con cupo y contadores) pendiente de
// lanzarse en el agente.
type launch struct {
	e    *entry
	seq  int
	send bool
	msg  string
	task agent.Task
	done chan struct{} // se cierra al terminar run
}

// Supervisor controla a un conjunto de agentes.
type Supervisor struct {
	maxActive int
	now       func() time.Time
	ctx       context.Context
	cancelCtx context.CancelFunc
	// agentsClosed se cierra cuando Close ya cerró a todos los agentes; hasta
	// entonces, los lectores siguen drenando (sin reenviar) para que ningún
	// agente quede bloqueado al emitir.
	agentsClosed chan struct{}
	wg           sync.WaitGroup
	closeDone    chan struct{}

	// hookBeforeLaunch es un gancho de pruebas: se llama justo antes de
	// lanzar un turno reservado, fuera del mutex.
	hookBeforeLaunch func(launch)

	mu      sync.Mutex
	entries []*entry // en orden de ID
	queue   []*entry
	closing bool
	closed  bool // el canal de updates ya se cerró
	dropped int64
	updates chan Update
}

// New crea un supervisor que admite maxActive agentes activos a la vez
// (Running o Paused). Con maxActive <= 0 usa 2.
func New(maxActive int) *Supervisor {
	if maxActive <= 0 {
		maxActive = 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{
		maxActive:    maxActive,
		now:          time.Now,
		ctx:          ctx,
		cancelCtx:    cancel,
		agentsClosed: make(chan struct{}),
		closeDone:    make(chan struct{}),
		updates:      make(chan Update, updateBuffer),
	}
}

// MaxActive devuelve cuántos agentes pueden estar activos (Running o Paused) a
// la vez.
func (s *Supervisor) MaxActive() int { return s.maxActive }

// Updates reenvía los eventos de cada agente y sus cambios de estado, en orden
// por agente. Se cierra con Close.
//
// Política si el consumidor no lee: el canal tiene un buffer generoso y
// nunca bloquea a nadie. Si se llena, se descarta la novedad más antigua para
// hacer sitio a la nueva (siempre llega lo último). Dropped cuenta los
// descartes y Snapshots permite resincronizar el estado.
func (s *Supervisor) Updates() <-chan Update { return s.updates }

// Dropped devuelve cuántas novedades se descartaron por tener el canal lleno.
func (s *Supervisor) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Submit registra al agente y arranca su primer turno con task, o lo encola si
// ya hay maxActive activos. El supervisor se hace dueño del agente: lo cierra
// en Close. Devuelve 0 si el supervisor ya está cerrado.
func (s *Supervisor) Submit(name string, a agent.Agent, task agent.Task, b Budget) ID {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return 0
	}
	e := &entry{
		id:     ID(len(s.entries) + 1),
		name:   name,
		a:      a,
		task:   task,
		budget: b,
		state:  Queued,
	}
	s.entries = append(s.entries, e)
	s.queue = append(s.queue, e)
	s.wg.Add(1)
	go s.read(e)
	s.emitLocked(e, nil)
	starts := s.scheduleLocked()
	s.mu.Unlock()
	s.dispatch(starts)
	return e.id
}

// Send lanza un turno nuevo del agente, que reanuda su sesión. Si no hay
// cupo, el turno espera en la cola. Cuenta contra el presupuesto de turnos.
func (s *Supervisor) Send(id ID, msg string) error {
	s.mu.Lock()
	e, err := s.findLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	switch e.state {
	case Idle, Stopped, Failed:
		if e.holding { // todavía se está cancelando su turno
			s.mu.Unlock()
			return fmt.Errorf("%w: el agente %d aún se está deteniendo", ErrState, id)
		}
	case BudgetExceeded:
		reason := e.reason
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrBudget, reason)
	default:
		st := e.state
		s.mu.Unlock()
		return fmt.Errorf("%w: el agente %d está %s", ErrState, id, st)
	}
	if reason := s.exhaustedLocked(e); reason != "" {
		s.setStateLocked(e, BudgetExceeded, reason)
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrBudget, reason)
	}
	e.pendSend, e.pendMsg = true, msg
	if s.activeLocked() >= s.maxActive || len(s.queue) > 0 {
		s.queue = append(s.queue, e)
		s.setStateLocked(e, Queued, "")
		s.mu.Unlock()
		return nil
	}
	l := s.reserveLocked(e)
	s.mu.Unlock()
	return s.run(l)
}

// exhaustedLocked devuelve por qué el agente ya no puede empezar otro turno
// ("" si puede): ya alcanzó (>=) alguno de sus límites.
func (s *Supervisor) exhaustedLocked(e *entry) string {
	b := e.budget
	switch {
	case b.MaxTokens > 0 && e.tokens >= b.MaxTokens:
		return fmt.Sprintf("presupuesto de tokens agotado: %d/%d", e.tokens, b.MaxTokens)
	case b.MaxDuration > 0 && s.elapsedLocked(e) >= b.MaxDuration:
		return fmt.Sprintf("presupuesto de tiempo agotado: %s/%s",
			s.elapsedLocked(e).Round(time.Millisecond), b.MaxDuration)
	case b.MaxTurns > 0 && e.turns >= b.MaxTurns:
		return fmt.Sprintf("presupuesto de turnos superado: %d/%d", e.turns+1, b.MaxTurns)
	}
	return ""
}

// Pause congela el turno en curso del agente. Sigue ocupando su cupo.
func (s *Supervisor) Pause(id ID) error {
	s.mu.Lock()
	e, err := s.findLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if e.state != Running {
		st := e.state
		s.mu.Unlock()
		return fmt.Errorf("%w: el agente %d está %s", ErrState, id, st)
	}
	s.mu.Unlock()
	if err := e.a.Pause(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.state == Running {
		s.stopClockLocked(e)
		s.setStateLocked(e, Paused, "")
	}
	return nil
}

// Resume reanuda el turno pausado del agente.
func (s *Supervisor) Resume(id ID) error {
	s.mu.Lock()
	e, err := s.findLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if e.state != Paused {
		st := e.state
		s.mu.Unlock()
		return fmt.Errorf("%w: el agente %d está %s", ErrState, id, st)
	}
	s.mu.Unlock()
	if err := e.a.Resume(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.state == Paused && !s.closing {
		s.startClockLocked(e)
		s.setStateLocked(e, Running, "")
	}
	return nil
}

// Stop cancela el turno en curso del agente: queda Stopped y, cuando su
// proceso ya terminó, libera el cupo. Si estaba en cola, sale de ella. Un
// agente Idle o Failed también pasa a Stopped; uno ya Stopped o
// BudgetExceeded no cambia. El agente sigue admitiendo Send.
func (s *Supervisor) Stop(id ID) error {
	s.mu.Lock()
	e, err := s.findLocked(id)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	cancel := s.stopLocked(e)
	s.mu.Unlock()
	if cancel {
		s.cancelAndRelease(e, false)
	}
	return nil
}

// StopAll detiene a todos los agentes a la vez y vuelve cuando todos los
// turnos en curso ya se cancelaron.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	var cancels []*entry
	for _, e := range s.entries {
		if s.stopLocked(e) {
			cancels = append(cancels, e)
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range cancels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.cancelAndRelease(e, false)
		}()
	}
	wg.Wait()
}

// Close cierra todos los agentes y después el canal de Updates. Es
// idempotente; las llamadas concurrentes esperan a la primera.
func (s *Supervisor) Close() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		<-s.closeDone
		return
	}
	s.closing = true
	s.queue = nil
	agents := make([]agent.Agent, 0, len(s.entries))
	for _, e := range s.entries {
		agents = append(agents, e.a)
		s.stopClockLocked(e)
		switch e.state {
		case Queued, Running, Paused, Idle:
			e.holding = false
			s.setStateLocked(e, Stopped, "supervisor cerrado")
		}
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.Close()
		}()
	}
	wg.Wait()
	close(s.agentsClosed)
	s.wg.Wait()

	s.mu.Lock()
	s.closed = true
	close(s.updates)
	s.mu.Unlock()
	s.cancelCtx()
	close(s.closeDone)
}

// Snapshots devuelve el estado de todos los agentes, ordenado por ID.
func (s *Supervisor) Snapshots() []Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Snapshot, len(s.entries))
	for i, e := range s.entries {
		out[i] = s.snapshotLocked(e)
	}
	return out
}

// --- internos (los *Locked requieren s.mu) ---

func (s *Supervisor) findLocked(id ID) (*entry, error) {
	if s.closing {
		return nil, ErrClosed
	}
	if id < 1 || int(id) > len(s.entries) {
		return nil, fmt.Errorf("%w: %d", ErrNotFound, id)
	}
	return s.entries[id-1], nil
}

func (s *Supervisor) activeLocked() int {
	n := 0
	for _, e := range s.entries {
		if e.holding {
			n++
		}
	}
	return n
}

func (s *Supervisor) snapshotLocked(e *entry) Snapshot {
	return Snapshot{
		ID:        e.id,
		Name:      e.name,
		State:     e.state,
		Turns:     e.turns,
		Tokens:    e.tokens,
		CostUSD:   e.cost,
		Elapsed:   s.elapsedLocked(e),
		Reason:    e.reason,
		SessionID: e.session,
	}
}

func (s *Supervisor) elapsedLocked(e *entry) time.Duration {
	d := e.acc
	if !e.runSince.IsZero() {
		d += s.now().Sub(e.runSince)
	}
	return d
}

// emitLocked publica una novedad sin bloquear nunca.
func (s *Supervisor) emitLocked(e *entry, ev agent.Event) {
	if s.closed {
		return
	}
	u := Update{ID: e.id, Event: ev, Snapshot: s.snapshotLocked(e)}
	select {
	case s.updates <- u:
		return
	default:
	}
	select {
	case <-s.updates:
		s.dropped++
	default:
	}
	select {
	case s.updates <- u:
	default:
		s.dropped++
	}
}

func (s *Supervisor) setStateLocked(e *entry, st State, reason string) {
	e.state = st
	e.reason = reason
	s.emitLocked(e, nil)
}

// startClockLocked abre un tramo de tiempo corrido y arma el temporizador del
// presupuesto de tiempo.
func (s *Supervisor) startClockLocked(e *entry) {
	e.runSince = s.now()
	e.timerSeq++
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	max := e.budget.MaxDuration
	if max <= 0 {
		return
	}
	seq := e.timerSeq
	e.timer = time.AfterFunc(max-e.acc, func() { s.onTimer(e, seq) })
}

// stopClockLocked cierra el tramo de tiempo corrido y desarma el temporizador.
func (s *Supervisor) stopClockLocked(e *entry) {
	if !e.runSince.IsZero() {
		e.acc += s.now().Sub(e.runSince)
		e.runSince = time.Time{}
	}
	e.timerSeq++
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// reserveLocked toma el cupo para el siguiente turno del agente y deja el
// lanzamiento pendiente para hacerlo con el mutex suelto.
func (s *Supervisor) reserveLocked(e *entry) launch {
	l := launch{e: e, send: e.pendSend, msg: e.pendMsg, task: e.task, done: make(chan struct{})}
	e.launching = l.done
	s.wg.Add(1) // lo cierra run; Close espera a los lanzamientos reservados
	e.pendSend, e.pendMsg = false, ""
	e.holding = true
	e.turns++
	e.turnSeq++
	e.turnErr = ""
	l.seq = e.turnSeq
	s.startClockLocked(e)
	s.setStateLocked(e, Running, "")
	return l
}

// scheduleLocked reserva cupo para los primeros de la cola mientras haya.
func (s *Supervisor) scheduleLocked() []launch {
	var out []launch
	for !s.closing && len(s.queue) > 0 && s.activeLocked() < s.maxActive {
		e := s.queue[0]
		s.queue = s.queue[1:]
		out = append(out, s.reserveLocked(e))
	}
	return out
}

// goLocked lanza f en una goroutine que Close espera. No hace nada si el
// supervisor se está cerrando.
func (s *Supervisor) goLocked(f func()) {
	if s.closing {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
}

// dispatch lanza los turnos reservados, uno tras otro, en la goroutine del
// llamador.
func (s *Supervisor) dispatch(ls []launch) {
	for _, l := range ls {
		_ = s.run(l)
	}
}

// errAborted indica que el turno reservado se detuvo antes de lanzarse.
var errAborted = fmt.Errorf("%w: el turno se detuvo antes de arrancar", ErrState)

// run lanza un turno reservado en el agente. Antes de llamarlo comprueba, bajo
// el mutex, que el turno sigue vigente (no se cerró el supervisor ni se
// detuvo mientras tanto); si no, no lo lanza. Si el agente lo rechaza, queda
// Failed y se libera su cupo. Mientras dura la llamada, cancelAndRelease espera
// (e.launching), así que un Stop concurrente cancela el turno ya lanzado en
// lugar de liberar el cupo antes de que exista.
func (s *Supervisor) run(l launch) error {
	defer s.wg.Done()
	if s.hookBeforeLaunch != nil {
		s.hookBeforeLaunch(l)
	}
	e := l.e
	s.mu.Lock()
	ok := !s.closing && e.turnSeq == l.seq && (e.state == Running || e.state == Paused)
	s.mu.Unlock()

	var err error
	if ok {
		if l.send {
			err = e.a.Send(l.msg)
		} else {
			err = e.a.Start(s.ctx, l.task)
		}
	}

	s.mu.Lock()
	close(l.done)
	if e.launching == l.done {
		e.launching = nil
	}
	closing := s.closing
	if ok && err != nil && e.turnSeq == l.seq && (e.state == Running || e.state == Paused) {
		s.failLocked(e, err.Error())
		s.afterReleaseLocked()
	}
	s.mu.Unlock()

	switch {
	case !ok:
		return errAborted
	case err == nil && closing:
		// Close pudo llamar al agente antes de que este lanzara el turno.
		_ = e.a.Close()
	}
	return err
}

// failLocked deja al agente Failed y sin cupo.
func (s *Supervisor) failLocked(e *entry, reason string) {
	s.stopClockLocked(e)
	e.holding = false
	s.setStateLocked(e, Failed, reason)
}

// afterReleaseLocked arranca, en segundo plano, a los de la cola que quepan
// tras liberarse un cupo.
func (s *Supervisor) afterReleaseLocked() {
	if starts := s.scheduleLocked(); len(starts) > 0 {
		s.goLocked(func() { s.dispatch(starts) })
	}
}

// stopLocked aplica Stop a un agente y dice si hay que cancelar su turno (con
// el mutex suelto, después).
func (s *Supervisor) stopLocked(e *entry) bool {
	switch e.state {
	case Queued:
		for i, q := range s.queue {
			if q == e {
				s.queue = append(s.queue[:i:i], s.queue[i+1:]...)
				break
			}
		}
		e.pendSend, e.pendMsg = false, ""
		s.setStateLocked(e, Stopped, "detenido por el usuario")
	case Running, Paused:
		s.stopClockLocked(e)
		s.setStateLocked(e, Stopped, "detenido por el usuario")
		return true
	case Idle, Failed:
		s.setStateLocked(e, Stopped, "detenido por el usuario")
	}
	return false
}

// cancelAndRelease cancela el turno en curso del agente y, cuando el
// proceso ya terminó, libera su cupo y arranca al siguiente de la cola. El
// estado ya lo fijó quien lo llama. Con async, el arranque del siguiente va a
// otra goroutine (para llamadas desde el lector de eventos).
func (s *Supervisor) cancelAndRelease(e *entry, async bool) {
	s.mu.Lock()
	launching := e.launching
	s.mu.Unlock()
	if launching != nil {
		<-launching // que Start/Send termine: Cancel debe ver el turno
	}
	err := e.a.Cancel()
	s.mu.Lock()
	e.holding = false
	if err != nil && !s.closing {
		e.reason += fmt.Sprintf(" (Cancel falló: %v)", err)
		s.emitLocked(e, nil)
	}
	if async {
		s.afterReleaseLocked()
		s.mu.Unlock()
		return
	}
	starts := s.scheduleLocked()
	s.mu.Unlock()
	s.dispatch(starts)
}

// exceedLocked marca al agente BudgetExceeded si tiene un turno en curso y
// dice si hay que cancelarlo.
func (s *Supervisor) exceedLocked(e *entry, reason string) bool {
	if e.state != Running && e.state != Paused {
		return false
	}
	s.stopClockLocked(e)
	s.setStateLocked(e, BudgetExceeded, reason)
	return true
}

// onTimer es el vencimiento del presupuesto de tiempo.
func (s *Supervisor) onTimer(e *entry, seq int) {
	s.mu.Lock()
	if s.closing || e.timerSeq != seq || e.state != Running {
		s.mu.Unlock()
		return
	}
	max := e.budget.MaxDuration
	if el := s.elapsedLocked(e); el < max {
		// Disparó antes de tiempo (no debería): se rearma por el resto.
		e.timer = time.AfterFunc(max-el, func() { s.onTimer(e, seq) })
		s.mu.Unlock()
		return
	}
	reason := fmt.Sprintf("presupuesto de tiempo superado: %s/%s",
		s.elapsedLocked(e).Round(time.Millisecond), max)
	cancel := s.exceedLocked(e, reason)
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if cancel {
		s.cancelAndRelease(e, false)
	}
}

// read reenvía los eventos de un agente hasta que cierra su canal. Cuando el
// supervisor se cierra deja de reenviar pero sigue leyendo (handle los
// descarta) hasta que el agente cierre su canal o Close haya cerrado a todos.
func (s *Supervisor) read(e *entry) {
	defer s.wg.Done()
	events := e.a.Events()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				s.eventsClosed(e)
				return
			}
			s.handle(e, ev)
		case <-s.agentsClosed:
			return
		}
	}
}

func (s *Supervisor) eventsClosed(e *entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	if e.state == Running || e.state == Paused {
		s.failLocked(e, "el agente cerró su canal de eventos")
		s.afterReleaseLocked()
	}
}

func (s *Supervisor) handle(e *entry, ev agent.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	switch ev := ev.(type) {
	case agent.Started:
		if ev.SessionID != "" {
			e.session = ev.SessionID
		}
		s.emitLocked(e, ev)
	case agent.Usage:
		e.tokens += ev.InputTokens + ev.OutputTokens
		e.cost += ev.CostUSD
		s.emitLocked(e, ev)
		if max := e.budget.MaxTokens; max > 0 && e.tokens > max {
			reason := fmt.Sprintf("presupuesto de tokens superado: %d/%d", e.tokens, max)
			if s.exceedLocked(e, reason) {
				s.goLocked(func() { s.cancelAndRelease(e, true) })
			}
		}
	case agent.Error:
		if ev.Err != nil {
			e.turnErr = ev.Err.Error()
		}
		s.emitLocked(e, ev)
	case agent.Done:
		if ev.SessionID != "" {
			e.session = ev.SessionID
		}
		s.emitLocked(e, ev)
		if (e.state != Running && e.state != Paused) || !e.holding {
			return
		}
		s.stopClockLocked(e)
		e.holding = false
		if ev.IsError {
			reason := e.turnErr
			if reason == "" {
				reason = ev.Result
			}
			if reason == "" {
				reason = "el turno terminó con error"
			}
			s.setStateLocked(e, Failed, reason)
		} else {
			s.setStateLocked(e, Idle, "")
		}
		s.afterReleaseLocked()
	default:
		s.emitLocked(e, ev)
	}
}
