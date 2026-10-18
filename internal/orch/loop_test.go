package orch

import (
	"encoding/json"
	"testing"

	"github.com/dpa23/wari/internal/agent"
)

func toolCall(name, input string) agent.ToolCall {
	return agent.ToolCall{ID: "t", Name: name, Input: json.RawMessage(input)}
}

// finishTurn cierra el turno en curso y espera a que el agente quede Idle.
func finishTurn(t *testing.T, s *Supervisor, f *fake, id ID) {
	t.Helper()
	f.emit(agent.Done{Result: "ok"})
	waitState(t, s, id, Idle)
}

func TestLoopSameToolPausesWithNotice(t *testing.T) {
	s := New(2)
	defer s.Close()
	col := collect(s)
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x"}, Budget{})
	waitState(t, s, id, Running)

	for range 3 {
		f.emit(toolCall("Bash", `{"command":"ls"}`))
	}
	waitState(t, s, id, Paused)
	n := col.waitNotice(t, NoticeLoop, nil)
	if n.Agent != id || n.Loop != LoopTool || n.Text == "" {
		t.Fatalf("aviso inesperado: %+v", n)
	}
	if got := f.get(&f.pauses); got != 1 {
		t.Fatalf("pauses = %d, se esperaba 1", got)
	}
}

func TestLoopSameToolNeedsConsecutiveIdenticalCalls(t *testing.T) {
	s := New(2)
	defer s.Close()
	col := collect(s)
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x"}, Budget{})
	waitState(t, s, id, Running)

	// Dos iguales, una distinta, dos iguales: nunca tres seguidas. El espaciado
	// del JSON no distingue argumentos.
	f.emit(toolCall("Bash", `{"command":"ls"}`))
	f.emit(toolCall("Bash", `{ "command": "ls" }`))
	f.emit(toolCall("Bash", `{"command":"pwd"}`))
	f.emit(toolCall("Bash", `{"command":"ls"}`))
	f.emit(toolCall("Bash", `{"command":"ls"}`))
	finishTurn(t, s, f, id)
	if n := col.notices(NoticeLoop); len(n) != 0 {
		t.Fatalf("avisos de bucle inesperados: %+v", n)
	}
	if got := f.get(&f.pauses); got != 0 {
		t.Fatalf("pauses = %d", got)
	}
}

func TestLoopSameToolResetsEachTurn(t *testing.T) {
	s := New(2)
	defer s.Close()
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x"}, Budget{})
	waitState(t, s, id, Running)
	f.emit(toolCall("Read", `{"p":"a"}`))
	f.emit(toolCall("Read", `{"p":"a"}`))
	finishTurn(t, s, f, id)
	if err := s.Send(id, "otra vez"); err != nil {
		t.Fatal(err)
	}
	f.emit(toolCall("Read", `{"p":"a"}`))
	f.emit(toolCall("Read", `{"p":"a"}`))
	finishTurn(t, s, f, id)
	if got := f.get(&f.pauses); got != 0 {
		t.Fatalf("pauses = %d: la cuenta no debe cruzar turnos", got)
	}
}

func TestLoopLimitsConfigurable(t *testing.T) {
	s := New(2)
	defer s.Close()
	s.SetLoopLimits(LoopLimits{SameTool: 2})
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x"}, Budget{})
	waitState(t, s, id, Running)
	f.emit(toolCall("Bash", `{"c":1}`))
	f.emit(toolCall("Bash", `{"c":1}`))
	waitState(t, s, id, Paused)

	off := New(2)
	defer off.Close()
	off.SetLoopLimits(LoopLimits{SameTool: -1, IdleTurns: -1})
	g := newFake()
	id2 := off.Submit("b", g, agent.Task{Prompt: "x"}, Budget{})
	waitState(t, off, id2, Running)
	for range 10 {
		g.emit(toolCall("Bash", `{"c":1}`))
	}
	finishTurn(t, off, g, id2)
	if got := g.get(&g.pauses); got != 0 {
		t.Fatalf("con la detección desactivada, pauses = %d", got)
	}
}

func TestLoopIdleTurnsPausesNextTurn(t *testing.T) {
	s := New(2)
	defer s.Close()
	col := collect(s)
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x", Permissions: agent.PermissionAcceptEdits}, Budget{})
	waitState(t, s, id, Running)
	finishTurn(t, s, f, id) // 1
	for range 2 {
		if err := s.Send(id, "sigue"); err != nil {
			t.Fatal(err)
		}
		waitState(t, s, id, Running)
		finishTurn(t, s, f, id) // 2 y 3
	}
	if n := col.notices(NoticeLoop); len(n) != 0 {
		t.Fatalf("aviso antes de tiempo: %+v", n)
	}
	// El cuarto turno arranca pausado, con aviso.
	if err := s.Send(id, "sigue"); err != nil {
		t.Fatal(err)
	}
	wantState(t, s, id, Paused)
	n := col.waitNotice(t, NoticeLoop, nil)
	if n.Agent != id || n.Loop != LoopTurns {
		t.Fatalf("aviso inesperado: %+v", n)
	}
	if got := f.get(&f.pauses); got != 1 {
		t.Fatalf("pauses = %d", got)
	}
}

func TestLoopIdleTurnsEditResetsCount(t *testing.T) {
	s := New(2)
	defer s.Close()
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x", Permissions: agent.PermissionAcceptEdits}, Budget{})
	waitState(t, s, id, Running)
	finishTurn(t, s, f, id)
	for i := range 6 {
		if err := s.Send(id, "sigue"); err != nil {
			t.Fatal(err)
		}
		wantState(t, s, id, Running)
		if i%2 == 0 { // un turno de cada dos edita
			f.emit(agent.FileEditProposed{ToolCallID: "e", Tool: "Edit", Path: "a.go"})
		}
		finishTurn(t, s, f, id)
	}
	if got := f.get(&f.pauses); got != 0 {
		t.Fatalf("pauses = %d: editar reinicia la cuenta", got)
	}
}

func TestLoopIdleTurnsOnlyForEditingAgents(t *testing.T) {
	s := New(2)
	defer s.Close()
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x"}, Budget{}) // PermissionAsk: no edita
	waitState(t, s, id, Running)
	finishTurn(t, s, f, id)
	for range 5 {
		if err := s.Send(id, "sigue"); err != nil {
			t.Fatal(err)
		}
		wantState(t, s, id, Running)
		finishTurn(t, s, f, id)
	}
	if got := f.get(&f.pauses); got != 0 {
		t.Fatalf("pauses = %d", got)
	}
}

func TestObserveSeesEveryUpdateInOrder(t *testing.T) {
	s := New(2)
	var seen []State
	var texts []string
	s.Observe(func(u Update) {
		if u.Event == nil && u.Notice == nil {
			seen = append(seen, u.Snapshot.State)
		}
		if ev, ok := u.Event.(agent.TextDelta); ok {
			texts = append(texts, ev.Text)
		}
	})
	f := newFake()
	id := s.Submit("a", f, agent.Task{Prompt: "x"}, Budget{})
	waitState(t, s, id, Running)
	f.emit(agent.TextDelta{Text: "hola"})
	finishTurn(t, s, f, id)
	s.Close()
	if len(seen) < 3 || seen[0] != Queued || seen[1] != Running || seen[2] != Idle {
		t.Fatalf("estados observados = %v", seen)
	}
	if len(texts) != 1 || texts[0] != "hola" {
		t.Fatalf("textos = %v", texts)
	}
}
