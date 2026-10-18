package dap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Errores de Session.
var (
	// ErrNotStopped indica una operación que exige el depurado detenido.
	ErrNotStopped = errors.New("dap: el depurado no está detenido")
	// ErrStarted indica un segundo Start sobre la misma sesión.
	ErrStarted = errors.New("dap: la sesión ya se inició")
)

// DefaultStopTimeout es el plazo de Stop para el disconnect antes de matar
// el adaptador.
const DefaultStopTimeout = 3 * time.Second

// State es el estado del depurado según la sesión.
type State int

// Estados de una sesión.
const (
	StateIdle       State = iota // antes de Start
	StateRunning                 // en ejecución
	StateStopped                 // detenido en un breakpoint, paso o pausa
	StateTerminated              // terminó o el adaptador cayó
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateStopped:
		return "stopped"
	case StateTerminated:
		return "terminated"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// SessionOptions configura una Session.
type SessionOptions struct {
	// AdapterID se envía en initialize (por ejemplo "coreclr" o "java").
	AdapterID string
	// StopTimeout acota el disconnect de Stop; 0 usa DefaultStopTimeout.
	StopTimeout time.Duration
}

// BreakpointInfo es un breakpoint pedido y lo que el adaptador dice de él.
type BreakpointInfo struct {
	File string
	// Line es la línea pedida; ActualLine la que el adaptador usó (puede
	// moverse a la siguiente línea ejecutable), 0 si aún no lo informó.
	Line       int
	ActualLine int
	Verified   bool
	Message    string
	ID         int
}

// Session es el flujo de alto nivel de un depurador sobre un Client. Todos
// los métodos son seguros para uso concurrente. Eventos: se leen con
// Events (del cliente); la sesión los sigue por su cuenta para mantener
// State, StoppedThread y los breakpoints verificados.
type Session struct {
	c    *Client
	opts SessionOptions

	bpSend sync.Mutex // serializa los setBreakpoints para que gane el último

	mu       sync.Mutex
	started  bool
	state    State
	thread   int
	stopGen  int
	lastStop StoppedEvent
	bps      map[string]map[int]*BreakpointInfo

	stopOnce sync.Once
	stopErr  error
}

// NewSession crea una sesión sobre una conexión ya abierta. Registra el
// gancho de eventos del cliente.
func NewSession(c *Client, opts SessionOptions) *Session {
	s := &Session{c: c, opts: opts, bps: map[string]map[int]*BreakpointInfo{}}
	c.SetEventHook(s.onEvent)
	return s
}

// Open conecta con un adaptador (ver Connect) y crea la sesión.
func Open(ctx context.Context, a Adapter, o ConnectOptions) (*Session, error) {
	c, err := Connect(ctx, a, o)
	if err != nil {
		return nil, err
	}
	return NewSession(c, SessionOptions{AdapterID: a.AdapterID}), nil
}

// Client devuelve la conexión subyacente.
func (s *Session) Client() *Client { return s.c }

// Events devuelve el canal de eventos del cliente.
func (s *Session) Events() <-chan Event { return s.c.Events() }

// Done se cierra cuando la conexión terminó.
func (s *Session) Done() <-chan struct{} { return s.c.Done() }

// State devuelve el estado actual.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateTerminated && s.c.Err() != nil {
		return StateTerminated
	}
	return s.state
}

// StoppedThread devuelve el hilo de la última parada y si el depurado sigue
// detenido.
func (s *Session) StoppedThread() (thread int, stopped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.thread, s.state == StateStopped
}

// LastStop devuelve el último evento stopped recibido.
func (s *Session) LastStop() StoppedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStop
}

// onEvent corre en la goroutine de lectura del cliente: solo toma el cerrojo.
func (s *Session) onEvent(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch e := ev.(type) {
	case StoppedEvent:
		s.state = StateStopped
		s.thread = e.ThreadID
		s.lastStop = e
		s.stopGen++
	case ContinuedEvent:
		if s.state == StateStopped {
			s.state = StateRunning
		}
	case TerminatedEvent, ExitedEvent:
		s.state = StateTerminated
	case BreakpointEvent:
		s.applyBreakpointLocked(e.Breakpoint)
	}
}

func (s *Session) applyBreakpointLocked(b Breakpoint) {
	if b.ID == 0 {
		return
	}
	for _, lines := range s.bps {
		for _, info := range lines {
			if info.ID == b.ID {
				info.Verified = b.Verified
				info.Message = b.Message
				if b.Line != 0 {
					info.ActualLine = b.Line
				}
				return
			}
		}
	}
}

// Start inicia el depurado con launch (args son los argumentos propios del
// adaptador). Hace el baile completo: initialize, launch, espera de
// initialized, envío de los breakpoints ya registrados, configurationDone y
// respuesta de launch. Los breakpoints puestos antes de Start se envían aquí.
// Si falla, llama a Stop para liberar el adaptador.
func (s *Session) Start(ctx context.Context, args json.RawMessage) error {
	return s.start(ctx, "launch", args)
}

// StartAttach es Start con attach en lugar de launch.
func (s *Session) StartAttach(ctx context.Context, args json.RawMessage) error {
	return s.start(ctx, "attach", args)
}

func (s *Session) start(ctx context.Context, command string, args json.RawMessage) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return ErrStarted
	}
	s.started = true
	s.state = StateRunning
	s.mu.Unlock()

	err := s.startSequence(ctx, command, args)
	if err != nil {
		sctx, cancel := context.WithTimeout(context.Background(), s.stopTimeout())
		_ = s.Stop(sctx)
		cancel()
	}
	return err
}

func (s *Session) startSequence(ctx context.Context, command string, args json.RawMessage) error {
	caps, err := s.c.Initialize(ctx, InitializeArgs{AdapterID: s.opts.AdapterID})
	if err != nil {
		return fmt.Errorf("dap: initialize: %w", err)
	}
	// launch no se espera todavía: muchos adaptadores no responden hasta
	// configurationDone, y otros solo emiten initialized tras recibirlo.
	launch := s.c.Go(command, args)
	launchDone := launch.Done()
	for ready := false; !ready; {
		select {
		case <-s.c.Initialized():
			ready = true
		case <-launchDone:
			if _, err := launch.Wait(ctx); err != nil {
				return fmt.Errorf("dap: %s: %w", command, err)
			}
			launchDone = nil // respondió antes de initialized: seguimos esperándolo
		case <-s.c.Done():
			return s.c.deadErr()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := s.syncAll(ctx); err != nil {
		return err
	}
	if caps.SupportsConfigurationDoneRequest {
		if err := s.c.ConfigurationDone(ctx); err != nil {
			return fmt.Errorf("dap: configurationDone: %w", err)
		}
	}
	if _, err := launch.Wait(ctx); err != nil {
		return fmt.Errorf("dap: %s: %w", command, err)
	}
	return nil
}

func (s *Session) stopTimeout() time.Duration {
	if s.opts.StopTimeout > 0 {
		return s.opts.StopTimeout
	}
	return DefaultStopTimeout
}

// Stop cierra la sesión: disconnect con terminateDebuggee (acotado por ctx y
// StopTimeout) y cierre de la conexión, que termina el grupo del adaptador
// si lo lanzamos nosotros. Es idempotente.
func (s *Session) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() { s.stopErr = s.stop(ctx) })
	return s.stopErr
}

func (s *Session) stop(ctx context.Context) error {
	var first error
	s.c.mu.Lock()
	inited := s.c.inited
	s.c.mu.Unlock()
	if inited && s.c.Err() == nil {
		dctx, cancel := context.WithTimeout(ctx, s.stopTimeout())
		if err := s.c.Disconnect(dctx, true); err != nil {
			first = fmt.Errorf("dap: disconnect: %w", err)
		}
		cancel()
	}
	if err := s.c.Close(); err != nil && first == nil {
		first = err
	}
	s.mu.Lock()
	s.state = StateTerminated
	s.mu.Unlock()
	return first
}

// SetBreakpoint añade un breakpoint en file:line (ruta absoluta, línea desde
// 1). Antes de Start solo se registra; después se envía de inmediato.
func (s *Session) SetBreakpoint(ctx context.Context, file string, line int) error {
	if line < 1 {
		return fmt.Errorf("dap: línea %d inválida", line)
	}
	file = filepath.Clean(file)
	s.mu.Lock()
	lines := s.bps[file]
	if lines == nil {
		lines = map[int]*BreakpointInfo{}
		s.bps[file] = lines
	}
	if _, dup := lines[line]; !dup {
		lines[line] = &BreakpointInfo{File: file, Line: line}
	}
	started := s.started
	s.mu.Unlock()
	if !started {
		return nil
	}
	return s.syncFile(ctx, file)
}

// ClearBreakpoint quita el breakpoint de file:line (no es error si no existía).
func (s *Session) ClearBreakpoint(ctx context.Context, file string, line int) error {
	file = filepath.Clean(file)
	s.mu.Lock()
	lines := s.bps[file]
	_, had := lines[line]
	delete(lines, line)
	if len(lines) == 0 && !had {
		delete(s.bps, file)
	}
	started := s.started
	s.mu.Unlock()
	if !started || !had {
		return nil
	}
	return s.syncFile(ctx, file)
}

// ToggleBreakpoint pone el breakpoint si no existe y lo quita si existe; devuelve
// si quedó puesto.
func (s *Session) ToggleBreakpoint(ctx context.Context, file string, line int) (bool, error) {
	file = filepath.Clean(file)
	s.mu.Lock()
	_, has := s.bps[file][line]
	s.mu.Unlock()
	if has {
		return false, s.ClearBreakpoint(ctx, file, line)
	}
	return true, s.SetBreakpoint(ctx, file, line)
}

// Breakpoints devuelve los breakpoints de file (todos si file es ""),
// ordenados por archivo y línea.
func (s *Session) Breakpoints(file string) []BreakpointInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BreakpointInfo
	for f, lines := range s.bps {
		if file != "" && f != filepath.Clean(file) {
			continue
		}
		for _, info := range lines {
			out = append(out, *info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func (s *Session) syncAll(ctx context.Context) error {
	s.mu.Lock()
	files := make([]string, 0, len(s.bps))
	for f := range s.bps {
		files = append(files, f)
	}
	s.mu.Unlock()
	sort.Strings(files)
	for _, f := range files {
		if err := s.syncFile(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// syncFile envía el conjunto actual de líneas de file. El cerrojo bpSend
// garantiza que la última modificación sea la última en llegar al adaptador.
func (s *Session) syncFile(ctx context.Context, file string) error {
	s.bpSend.Lock()
	defer s.bpSend.Unlock()
	s.mu.Lock()
	lines := make([]int, 0, len(s.bps[file]))
	for l := range s.bps[file] {
		lines = append(lines, l)
	}
	s.mu.Unlock()
	sort.Ints(lines)
	res, err := s.c.SetBreakpoints(ctx, file, lines)
	if err != nil {
		return fmt.Errorf("dap: setBreakpoints %s: %w", file, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range lines {
		info := s.bps[file][l]
		if info == nil || i >= len(res) {
			continue // quitado mientras tanto, o el adaptador respondió de menos
		}
		info.ID = res[i].ID
		info.Verified = res[i].Verified
		info.Message = res[i].Message
		info.ActualLine = res[i].Line
	}
	return nil
}

// target resuelve el hilo de una operación: thread > 0 se usa tal cual; si
// no, el hilo de la última parada.
func (s *Session) target(thread int) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if thread > 0 {
		return thread, s.stopGen, nil
	}
	if s.state != StateStopped {
		return 0, 0, ErrNotStopped
	}
	return s.thread, s.stopGen, nil
}

// markRunning pasa a StateRunning tras reanudar, salvo que ya haya llegado
// una parada nueva (los eventos pueden adelantarse a la respuesta).
func (s *Session) markRunning(gen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopGen == gen && s.state == StateStopped {
		s.state = StateRunning
	}
}

// Continue reanuda el hilo (thread <= 0: el de la última parada).
func (s *Session) Continue(ctx context.Context, thread int) error {
	t, gen, err := s.target(thread)
	if err != nil {
		return err
	}
	if _, err := s.c.Continue(ctx, t); err != nil {
		return err
	}
	s.markRunning(gen)
	return nil
}

// Next avanza una línea sin entrar en llamadas (thread <= 0: el de la
// última parada).
func (s *Session) Next(ctx context.Context, thread int) error {
	return s.step(ctx, thread, s.c.Next)
}

// StepIn avanza entrando en la llamada.
func (s *Session) StepIn(ctx context.Context, thread int) error {
	return s.step(ctx, thread, s.c.StepIn)
}

// StepOut avanza hasta salir de la función actual.
func (s *Session) StepOut(ctx context.Context, thread int) error {
	return s.step(ctx, thread, s.c.StepOut)
}

func (s *Session) step(ctx context.Context, thread int, f func(context.Context, int) error) error {
	t, gen, err := s.target(thread)
	if err != nil {
		return err
	}
	if err := f(ctx, t); err != nil {
		return err
	}
	s.markRunning(gen)
	return nil
}

// Pause detiene el depurado. Con thread <= 0 usa el primer hilo que informe
// el adaptador.
func (s *Session) Pause(ctx context.Context, thread int) error {
	if thread <= 0 {
		threads, err := s.c.Threads(ctx)
		if err != nil {
			return err
		}
		if len(threads) == 0 {
			return errors.New("dap: el depurado no tiene hilos")
		}
		thread = threads[0].ID
	}
	return s.c.Pause(ctx, thread)
}

// Threads lista los hilos del depurado.
func (s *Session) Threads(ctx context.Context) ([]Thread, error) {
	return s.c.Threads(ctx)
}

// Stack devuelve los marcos del hilo (thread <= 0: el de la última parada),
// del más reciente al más antiguo.
func (s *Session) Stack(ctx context.Context, thread int) ([]StackFrame, error) {
	t, _, err := s.target(thread)
	if err != nil {
		return nil, err
	}
	frames, _, err := s.c.StackTrace(ctx, t, 0, 0)
	return frames, err
}

// Scopes devuelve los ámbitos de un marco.
func (s *Session) Scopes(ctx context.Context, frameID int) ([]Scope, error) {
	return s.c.Scopes(ctx, frameID)
}

// Variables devuelve los hijos de una variablesReference. Las referencias de
// una parada dejan de valer al reanudar.
func (s *Session) Variables(ctx context.Context, ref int) ([]Variable, error) {
	if ref <= 0 {
		return nil, nil
	}
	return s.c.Variables(ctx, ref)
}

// Evaluate evalúa una expresión en el marco dado (0: contexto global).
func (s *Session) Evaluate(ctx context.Context, expr string, frameID int) (EvaluateResult, error) {
	return s.c.Evaluate(ctx, expr, frameID, "repl")
}

// Tree arma el árbol navegable de variables de un marco: un nodo por ámbito
// y, ya expandidos, los ámbitos no costosos (locales, argumentos).
func (s *Session) Tree(ctx context.Context, frameID int) (*Tree, error) {
	scopes, err := s.c.Scopes(ctx, frameID)
	if err != nil {
		return nil, err
	}
	t := NewTree(s.Variables)
	for _, sc := range scopes {
		n := &Node{Variable: Variable{Name: sc.Name, VariablesReference: sc.VariablesReference}, Scope: true}
		t.Roots = append(t.Roots, n)
		if !sc.Expensive && sc.VariablesReference > 0 {
			if err := t.Expand(ctx, n); err != nil {
				return t, err
			}
		}
	}
	return t, nil
}
