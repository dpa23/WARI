package orch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
)

// fake es un agent.Agent en memoria que permite inyectar eventos y observar
// qué se le pidió.
type fake struct {
	mu       sync.Mutex
	events   chan agent.Event
	starts   []agent.Task
	sends    []string
	cancels  int
	pauses   int
	resumes  int
	closes   int
	startErr error
	closed   bool

	// ops registra, en orden, start, start-end, cancel y close. gate (si no es
	// nil) retiene a Start hasta que se cierre; entered avisa de que Start
	// empezó.
	ops     []string
	gate    chan struct{}
	entered chan struct{}
}

var _ agent.Agent = (*fake)(nil)

func newFake() *fake { return &fake{events: make(chan agent.Event, 64)} }

func (f *fake) Start(_ context.Context, t agent.Task) error {
	f.mu.Lock()
	if f.startErr != nil {
		defer f.mu.Unlock()
		return f.startErr
	}
	f.starts = append(f.starts, t)
	f.ops = append(f.ops, "start")
	gate, entered := f.gate, f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	f.log("start-end")
	return nil
}

func (f *fake) log(op string) {
	f.mu.Lock()
	f.ops = append(f.ops, op)
	f.mu.Unlock()
}

func (f *fake) opsCopy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

func (f *fake) Send(msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, msg)
	return nil
}

func (f *fake) Events() <-chan agent.Event { return f.events }

func (f *fake) Pause() error  { f.count(&f.pauses); return nil }
func (f *fake) Resume() error { f.count(&f.resumes); return nil }
func (f *fake) Cancel() error { f.log("cancel"); f.count(&f.cancels); return nil }

func (f *fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	f.ops = append(f.ops, "close")
	if !f.closed {
		f.closed = true
		close(f.events)
	}
	return nil
}

func (f *fake) Status() agent.Status { return agent.StatusIdle }

func (f *fake) count(n *int) {
	f.mu.Lock()
	*n++
	f.mu.Unlock()
}

func (f *fake) get(n *int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *n
}

func (f *fake) nStarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func (f *fake) nSends() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func (f *fake) emit(ev agent.Event) { f.events <- ev }

func snap(t *testing.T, s *Supervisor, id ID) Snapshot {
	t.Helper()
	for _, sn := range s.Snapshots() {
		if sn.ID == id {
			return sn
		}
	}
	t.Fatalf("no hay snapshot del agente %d", id)
	return Snapshot{}
}

func waitState(t *testing.T, s *Supervisor, id ID, want State) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		sn := snap(t, s, id)
		if sn.State == want {
			return sn
		}
		if time.Now().After(deadline) {
			t.Fatalf("agente %d: estado %v, se esperaba %v (%s)", id, sn.State, want, sn.Reason)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func wantState(t *testing.T, s *Supervisor, id ID, want State) {
	t.Helper()
	if got := snap(t, s, id).State; got != want {
		t.Fatalf("agente %d: estado %v, se esperaba %v", id, got, want)
	}
}

func TestAcceptancePhase5(t *testing.T) {
	s := New(2)
	defer s.Close()
	fa, fb, fc, fd := newFake(), newFake(), newFake(), newFake()

	a := s.Submit("a", fa, agent.Task{Prompt: "a"}, Budget{})
	b := s.Submit("b", fb, agent.Task{Prompt: "b"}, Budget{})
	c := s.Submit("c", fc, agent.Task{Prompt: "c"}, Budget{})
	wantState(t, s, a, Running)
	wantState(t, s, b, Running)
	wantState(t, s, c, Queued)
	if fc.nStarts() != 0 {
		t.Fatal("el tercero no debe arrancar")
	}

	// Pausar uno: sigue activo, el tercero sigue en cola.
	if err := s.Pause(b); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, b, Paused)
	wantState(t, s, c, Queued)
	if fb.get(&fb.pauses) != 1 {
		t.Fatal("Pause no llegó al agente")
	}

	// Detener otro: el tercero arranca.
	if err := s.Stop(a); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, a, Stopped)
	if fa.get(&fa.cancels) != 1 {
		t.Fatal("Stop no canceló el turno")
	}
	waitState(t, s, c, Running)
	if fc.nStarts() != 1 {
		t.Fatalf("starts de c = %d", fc.nStarts())
	}

	// Un agente con presupuesto de tokens se pasa.
	d := s.Submit("d", fd, agent.Task{Prompt: "d"}, Budget{MaxTokens: 1000})
	wantState(t, s, d, Queued)
	if err := s.Stop(b); err != nil { // b estaba pausado: libera el cupo
		t.Fatal(err)
	}
	waitState(t, s, d, Running)
	fd.emit(agent.Usage{InputTokens: 700, OutputTokens: 500})
	sn := waitState(t, s, d, BudgetExceeded)
	if sn.Reason != "presupuesto de tokens superado: 1200/1000" {
		t.Fatalf("reason = %q", sn.Reason)
	}
	deadline := time.Now().Add(3 * time.Second)
	for fd.get(&fd.cancels) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("no se llamó a Cancel tras superar el presupuesto")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := s.Send(d, "más"); !errors.Is(err, ErrBudget) {
		t.Fatalf("Send tras presupuesto: %v", err)
	}
	if fd.nSends() != 0 {
		t.Fatal("no debe aceptar más turnos")
	}
}

func TestDoneFreesSlotAndStartsQueueFIFO(t *testing.T) {
	s := New(1)
	defer s.Close()
	f1, f2, f3 := newFake(), newFake(), newFake()
	a := s.Submit("a", f1, agent.Task{}, Budget{})
	b := s.Submit("b", f2, agent.Task{}, Budget{})
	c := s.Submit("c", f3, agent.Task{}, Budget{})
	wantState(t, s, b, Queued)
	f1.emit(agent.Done{})
	waitState(t, s, a, Idle)
	waitState(t, s, b, Running)
	wantState(t, s, c, Queued)

	// Un Send con la cola ocupada espera su turno.
	if err := s.Send(a, "otra"); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, a, Queued)
	f2.emit(agent.Done{})
	waitState(t, s, c, Running)
	f3.emit(agent.Done{})
	waitState(t, s, a, Running)
	if f1.nSends() != 1 {
		t.Fatalf("sends de a = %d", f1.nSends())
	}
	if sn := snap(t, s, a); sn.Turns != 2 {
		t.Fatalf("turnos de a = %d", sn.Turns)
	}
}

func TestStartErrorFailsAndFreesSlot(t *testing.T) {
	s := New(1)
	defer s.Close()
	f1, f2 := newFake(), newFake()
	f1.startErr = errors.New("boom")
	a := s.Submit("a", f1, agent.Task{}, Budget{})
	b := s.Submit("b", f2, agent.Task{}, Budget{})
	sn := waitState(t, s, a, Failed)
	if !strings.Contains(sn.Reason, "boom") {
		t.Fatalf("reason = %q", sn.Reason)
	}
	waitState(t, s, b, Running)
}

func TestDoneWithErrorFails(t *testing.T) {
	s := New(1)
	defer s.Close()
	f := newFake()
	a := s.Submit("a", f, agent.Task{}, Budget{})
	f.emit(agent.Error{Err: errors.New("se cayó")})
	f.emit(agent.Done{IsError: true})
	sn := waitState(t, s, a, Failed)
	if sn.Reason != "se cayó" {
		t.Fatalf("reason = %q", sn.Reason)
	}
	if err := s.Send(a, "reintento"); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, a, Running)
}

func TestTurnBudget(t *testing.T) {
	s := New(1)
	defer s.Close()
	f := newFake()
	a := s.Submit("a", f, agent.Task{}, Budget{MaxTurns: 2})
	f.emit(agent.Done{})
	waitState(t, s, a, Idle)
	if err := s.Send(a, "dos"); err != nil {
		t.Fatal(err)
	}
	f.emit(agent.Done{})
	waitState(t, s, a, Idle)
	err := s.Send(a, "tres")
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("Send = %v, se esperaba ErrBudget", err)
	}
	sn := snap(t, s, a)
	if sn.State != BudgetExceeded || sn.Reason != "presupuesto de turnos superado: 3/2" {
		t.Fatalf("snapshot = %+v", sn)
	}
	if f.nSends() != 1 || sn.Turns != 2 {
		t.Fatalf("sends = %d, turnos = %d", f.nSends(), sn.Turns)
	}
}

func TestTimeBudgetExcludesPause(t *testing.T) {
	s := New(1)
	defer s.Close()
	f := newFake()
	const budget = 400 * time.Millisecond
	a := s.Submit("a", f, agent.Task{}, Budget{MaxDuration: budget})
	time.Sleep(60 * time.Millisecond)
	if err := s.Pause(a); err != nil {
		t.Fatal(err)
	}
	time.Sleep(budget + 150*time.Millisecond) // más que el presupuesto entero
	sn := snap(t, s, a)
	if sn.State != Paused {
		t.Fatalf("estado = %v (%s); la pausa no debe contar", sn.State, sn.Reason)
	}
	if sn.Elapsed >= budget/2 {
		t.Fatalf("elapsed = %v, la pausa se contó", sn.Elapsed)
	}
	if err := s.Resume(a); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, a, Running)
	sn = waitState(t, s, a, BudgetExceeded)
	if !strings.HasPrefix(sn.Reason, "presupuesto de tiempo superado: ") {
		t.Fatalf("reason = %q", sn.Reason)
	}
	if sn.Elapsed < budget {
		t.Fatalf("elapsed = %v < %v", sn.Elapsed, budget)
	}
	deadline := time.Now().Add(3 * time.Second)
	for f.get(&f.cancels) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("no se canceló el turno")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestTimeBudgetFreesSlot(t *testing.T) {
	s := New(1)
	defer s.Close()
	f1, f2 := newFake(), newFake()
	a := s.Submit("a", f1, agent.Task{}, Budget{MaxDuration: 30 * time.Millisecond})
	b := s.Submit("b", f2, agent.Task{}, Budget{})
	waitState(t, s, a, BudgetExceeded)
	waitState(t, s, b, Running)
}

func TestStopQueued(t *testing.T) {
	s := New(1)
	defer s.Close()
	f1, f2, f3 := newFake(), newFake(), newFake()
	a := s.Submit("a", f1, agent.Task{}, Budget{})
	b := s.Submit("b", f2, agent.Task{}, Budget{})
	c := s.Submit("c", f3, agent.Task{}, Budget{})
	if err := s.Stop(b); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, b, Stopped)
	if f2.get(&f2.cancels) != 0 {
		t.Fatal("un agente en cola no tiene turno que cancelar")
	}
	f1.emit(agent.Done{})
	waitState(t, s, a, Idle)
	waitState(t, s, c, Running) // b salió de la cola
	if f2.nStarts() != 0 {
		t.Fatal("b no debe arrancar")
	}
}

func TestStopAll(t *testing.T) {
	s := New(2)
	defer s.Close()
	fs := []*fake{newFake(), newFake(), newFake()}
	var ids []ID
	for i, f := range fs {
		ids = append(ids, s.Submit(fmt.Sprint(i), f, agent.Task{}, Budget{}))
	}
	if err := s.Pause(ids[1]); err != nil {
		t.Fatal(err)
	}
	s.StopAll()
	for i, id := range ids {
		wantState(t, s, id, Stopped)
		want := 1
		if i == 2 {
			want = 0
		}
		if got := fs[i].get(&fs[i].cancels); got != want {
			t.Fatalf("cancels[%d] = %d, se esperaba %d", i, got, want)
		}
	}
	if fs[2].nStarts() != 0 {
		t.Fatal("el encolado no debe arrancar tras StopAll")
	}
	// Con los cupos liberados, un Send vuelve a funcionar.
	if err := s.Send(ids[0], "sigue"); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, ids[0], Running)
}

func TestClose(t *testing.T) {
	s := New(2)
	fs := []*fake{newFake(), newFake(), newFake()}
	var ids []ID
	for i, f := range fs {
		ids = append(ids, s.Submit(fmt.Sprint(i), f, agent.Task{}, Budget{}))
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	s.Close() // concurrente e idempotente
	<-done

	drained := make(chan struct{})
	go func() {
		for range s.Updates() {
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("Updates no se cerró")
	}
	for i, f := range fs {
		if got := f.get(&f.closes); got != 1 {
			t.Fatalf("closes[%d] = %d", i, got)
		}
	}
	if err := s.Send(ids[0], "x"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Send tras Close = %v", err)
	}
	if id := s.Submit("z", newFake(), agent.Task{}, Budget{}); id != 0 {
		t.Fatalf("Submit tras Close = %d", id)
	}
	if len(s.Snapshots()) != 3 {
		t.Fatal("Snapshots debe seguir disponible tras Close")
	}
}

func TestUpdatesOrder(t *testing.T) {
	s := New(1)
	defer s.Close()
	f := newFake()
	a := s.Submit("a", f, agent.Task{}, Budget{})
	f.emit(agent.Started{SessionID: "s1"})
	f.emit(agent.TextDelta{Text: "hola"})
	f.emit(agent.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.5})
	f.emit(agent.Done{SessionID: "s1", Result: "ok"})

	var got []string
	var last Update
	for len(got) < 7 {
		select {
		case u := <-s.Updates():
			if u.ID != a {
				t.Fatalf("update de otro agente: %d", u.ID)
			}
			if u.Event == nil {
				got = append(got, "state:"+u.Snapshot.State.String())
			} else {
				got = append(got, fmt.Sprintf("%T", u.Event))
			}
			last = u
		case <-time.After(3 * time.Second):
			t.Fatalf("faltan updates: %v", got)
		}
	}
	want := []string{
		"state:queued", "state:running", "agent.Started", "agent.TextDelta",
		"agent.Usage", "agent.Done", "state:idle",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("updates = %v\nesperado = %v", got, want)
	}
	sn := last.Snapshot
	if sn.Tokens != 15 || sn.CostUSD != 0.5 || sn.SessionID != "s1" || sn.Turns != 1 {
		t.Fatalf("snapshot final = %+v", sn)
	}
}

func TestUpdatesNeverBlockWhenFull(t *testing.T) {
	s := New(1)
	f := newFake()
	s.Submit("a", f, agent.Task{}, Budget{})
	n := updateBuffer + 500
	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			f.emit(agent.TextDelta{Text: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("el agente se bloqueó con Updates sin leer")
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.Dropped() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no se descartó nada con el canal lleno")
		}
		time.Sleep(2 * time.Millisecond)
	}
	s.Close()
}

func TestInvalidOperations(t *testing.T) {
	s := New(0)
	defer s.Close()
	if err := s.Pause(7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Pause desconocido = %v", err)
	}
	f := newFake()
	a := s.Submit("a", f, agent.Task{}, Budget{})
	if err := s.Resume(a); !errors.Is(err, ErrState) {
		t.Fatalf("Resume de uno en marcha = %v", err)
	}
	if err := s.Send(a, "x"); !errors.Is(err, ErrState) {
		t.Fatalf("Send con turno en curso = %v", err)
	}
}
