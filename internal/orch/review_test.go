package orch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
)

func isClosing(s *Supervisor) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func waitClosing(t *testing.T, s *Supervisor) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !isClosing(s) {
		if time.Now().After(deadline) {
			t.Fatal("Close no llegó a marcar closing")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitChan(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("tiempo agotado esperando: %s", what)
	}
}

// Revisión 1: Stop entre la reserva del cupo y la llamada a Start. El turno
// no debe arrancar después de que Stop terminó (proceso fuera de supervisión),
// ni liberar el cupo antes de tiempo.
func TestStopBetweenReserveAndStart(t *testing.T) {
	s := New(1)
	defer s.Close()
	f := newFake()
	reached := make(chan struct{}, 1)
	gate := make(chan struct{})
	var once sync.Once
	s.hookBeforeLaunch = func(launch) {
		once.Do(func() {
			reached <- struct{}{}
			<-gate
		})
	}
	submitted := make(chan struct{})
	go func() { s.Submit("a", f, agent.Task{}, Budget{}); close(submitted) }()
	waitChan(t, reached, "el turno reservado")

	stopped := make(chan struct{})
	go func() { _ = s.Stop(1); close(stopped) }()
	waitState(t, s, 1, Stopped)
	close(gate)
	waitChan(t, stopped, "Stop")
	waitChan(t, submitted, "Submit")

	ops := f.opsCopy()
	start := slices.Index(ops, "start")
	cancel := slices.Index(ops, "cancel")
	if start >= 0 && (cancel < 0 || cancel < start) {
		t.Fatalf("el turno arrancó tras cancelarse: %v", ops)
	}
	if got := s.activeCount(); got != 0 {
		t.Fatalf("cupos ocupados = %d", got)
	}
	// El cupo quedó libre de verdad: otro agente arranca.
	f2 := newFake()
	id := s.Submit("b", f2, agent.Task{}, Budget{})
	waitState(t, s, id, Running)
}

func (s *Supervisor) activeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeLocked()
}

// Revisión 5: un Close concurrente con un turno ya reservado (tras liberarse
// un cupo) no debe dejar que arranque después de volver.
func TestCloseDoesNotLaunchReservedTurn(t *testing.T) {
	s := New(1)
	fa, fb := newFake(), newFake()
	reached := make(chan struct{}, 1)
	gate := make(chan struct{})
	s.hookBeforeLaunch = func(l launch) {
		if l.e.name == "b" {
			reached <- struct{}{}
			<-gate
		}
	}
	s.Submit("a", fa, agent.Task{}, Budget{})
	s.Submit("b", fb, agent.Task{}, Budget{})
	stopped := make(chan struct{})
	go func() { _ = s.Stop(1); close(stopped) }() // libera el cupo: b se reserva
	waitChan(t, reached, "b reservado")

	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	waitClosing(t, s)
	close(gate)
	waitChan(t, closed, "Close")
	waitChan(t, stopped, "Stop")
	time.Sleep(20 * time.Millisecond)
	if n := fb.nStarts(); n != 0 {
		t.Fatalf("b arrancó %d vez/veces tras Close", n)
	}
}

// Revisión 5: Close espera a los lanzamientos en curso y deja cerrado al
// agente aunque Start termine después de su Close.
func TestCloseWaitsForInFlightStart(t *testing.T) {
	s := New(2)
	f := newFake()
	f.gate = make(chan struct{})
	f.entered = make(chan struct{}, 1)
	submitted := make(chan struct{})
	go func() { s.Submit("a", f, agent.Task{}, Budget{}); close(submitted) }()
	waitChan(t, f.entered, "Start")

	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	waitClosing(t, s)
	select {
	case <-closed:
		t.Fatal("Close volvió con un Start en curso")
	case <-time.After(30 * time.Millisecond):
	}
	close(f.gate)
	waitChan(t, closed, "Close")
	waitChan(t, submitted, "Submit")
	ops := f.opsCopy()
	end := slices.Index(ops, "start-end")
	if end < 0 || slices.Index(ops[end:], "close") < 0 {
		t.Fatalf("no hubo Close tras terminar Start: %v", ops)
	}
}

// stuckFake bloquea al emitir (canal sin buffer) y su Close espera a que su
// emisor termine, como un adaptador que no puede cortar un envío pendiente.
type stuckFake struct {
	events      chan agent.Event
	more        chan struct{}
	emitterDone chan struct{}
}

func newStuckFake() *stuckFake {
	f := &stuckFake{
		events:      make(chan agent.Event),
		more:        make(chan struct{}),
		emitterDone: make(chan struct{}),
	}
	go func() {
		defer close(f.emitterDone)
		f.events <- agent.TextDelta{Text: "primero"}
		<-f.more
		for i := 0; i < 3; i++ {
			f.events <- agent.TextDelta{Text: "pendiente"}
		}
	}()
	return f
}

func (f *stuckFake) Start(context.Context, agent.Task) error { return nil }
func (f *stuckFake) Send(string) error                       { return nil }
func (f *stuckFake) Events() <-chan agent.Event              { return f.events }
func (f *stuckFake) Pause() error                            { return nil }
func (f *stuckFake) Resume() error                           { return nil }
func (f *stuckFake) Cancel() error                           { return nil }
func (f *stuckFake) Status() agent.Status                    { return agent.StatusIdle }
func (f *stuckFake) Close() error {
	<-f.emitterDone
	close(f.events)
	return nil
}

// Revisión 2: si el agente bloquea al emitir hasta que alguien lea, Close no
// debe dejar de leer antes de que el agente cierre.
func TestCloseDrainsAgentBlockedOnEmit(t *testing.T) {
	s := New(2)
	f := newStuckFake()
	s.Submit("a", f, agent.Task{}, Budget{})
	select {
	case u := <-s.Updates():
		_ = u
	case <-time.After(3 * time.Second):
		t.Fatal("no llegó el primer evento")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	waitClosing(t, s)
	close(f.more) // ahora el emisor tiene envíos pendientes
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		go func() { // no dejar goroutines colgadas
			for range f.events {
			}
		}()
		t.Fatal("Close se colgó: nadie leyó los eventos pendientes del agente")
	}
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Revisión 3: un turno que agotó exactamente el presupuesto de tokens y
// terminó en Idle no admite otro.
func TestSendRejectedWhenTokensExhausted(t *testing.T) {
	s := New(1)
	defer s.Close()
	f := newFake()
	a := s.Submit("a", f, agent.Task{}, Budget{MaxTokens: 1000})
	f.emit(agent.Usage{InputTokens: 600, OutputTokens: 400}) // == límite
	f.emit(agent.Done{})
	waitState(t, s, a, Idle)
	err := s.Send(a, "otro")
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("Send = %v, se esperaba ErrBudget", err)
	}
	sn := snap(t, s, a)
	if sn.State != BudgetExceeded || !strings.Contains(sn.Reason, "tokens") {
		t.Fatalf("snapshot = %+v", sn)
	}
	if f.nSends() != 0 {
		t.Fatal("no debe lanzar otro turno")
	}
}

// Revisión 3: igual con el presupuesto de tiempo, con reloj controlado.
func TestSendRejectedWhenTimeExhausted(t *testing.T) {
	s := New(1)
	defer s.Close()
	clk := &fakeClock{t: time.Unix(1000, 0)}
	s.now = clk.Now
	f := newFake()
	a := s.Submit("a", f, agent.Task{}, Budget{MaxDuration: time.Hour})
	clk.add(time.Hour) // el turno termina justo en el límite
	f.emit(agent.Done{})
	waitState(t, s, a, Idle)
	err := s.Send(a, "otro")
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("Send = %v, se esperaba ErrBudget", err)
	}
	if sn := snap(t, s, a); sn.State != BudgetExceeded || !strings.Contains(sn.Reason, "tiempo") {
		t.Fatalf("snapshot = %+v", sn)
	}
	if f.nSends() != 0 {
		t.Fatal("no debe lanzar otro turno")
	}
}

// Revisión 4: llegar exactamente al límite no corta el turno en curso; solo
// pasarse (>) lo corta.
func TestExactLimitDoesNotCutRunningTurn(t *testing.T) {
	s := New(1)
	defer s.Close()
	f := newFake()
	a := s.Submit("a", f, agent.Task{}, Budget{MaxTokens: 1000})
	f.emit(agent.Usage{InputTokens: 1000})
	f.emit(agent.TextDelta{Text: "sigo"})
	// El TextDelta llega después del Usage: cuando se ve, el Usage ya se aplicó.
	deadline := time.Now().Add(3 * time.Second)
	for snap(t, s, a).Tokens != 1000 {
		if time.Now().After(deadline) {
			t.Fatal("no llegó el Usage")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if sn := snap(t, s, a); sn.State != Running {
		t.Fatalf("estado = %v (%s); el límite exacto no corta el turno", sn.State, sn.Reason)
	}
	if f.get(&f.cancels) != 0 {
		t.Fatal("no debe cancelar")
	}
	f.emit(agent.Usage{OutputTokens: 1}) // 1001 > 1000
	waitState(t, s, a, BudgetExceeded)
}
