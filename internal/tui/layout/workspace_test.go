package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/tui"
)

// efake es un agente falso con eventos inyectables.
type efake struct{ qfake }

func newEfake() *efake { return &efake{qfake{ch: make(chan agent.Event, 16)}} }

// pump entrega al layout las novedades del supervisor hasta que cond se cumpla.
func pump(t *testing.T, m Model, cond func(Model) bool) Model {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond(m) {
		if time.Now().After(deadline) {
			t.Fatalf("no se cumplió la condición:\n%s", screen(m))
		}
		cmd := m.Init() // escucha las novedades del supervisor
		done := make(chan tea.Msg, 1)
		go func() { done <- cmd() }()
		select {
		case msg := <-done:
			m, _ = m.update(msg)
		case <-time.After(50 * time.Millisecond):
			// Nada nuevo: la goroutine queda esperando; la siguiente vuelta
			// vuelve a leer del mismo canal, así que se recoge después.
			select {
			case msg := <-done:
				m, _ = m.update(msg)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	return m
}

func TestStatusBarShowsFileConflictAlert(t *testing.T) {
	root := project(t)
	sup := orch.New(2)
	defer sup.Close()
	if err := sup.AttachWorkspace(orch.Workspace{Root: root}); err != nil {
		t.Fatal(err)
	}
	var made []*efake
	m := New(Config{Root: root, Chat: tui.Config{
		Supervisor: sup,
		NewAgent:   func() agent.Agent { f := newEfake(); made = append(made, f); return f },
	}})
	m, _ = m.update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = m.focusPanel(focusChat)

	m, _ = send(m, typeKeys("uno")...)
	m, _ = send(m, press(tea.KeyEnter, 0))
	m, _ = send(m, press('t', tea.ModCtrl))
	m, _ = send(m, typeKeys("dos")...)
	m, _ = send(m, press(tea.KeyEnter, 0))
	if len(made) != 2 {
		t.Fatalf("agentes = %d", len(made))
	}

	bar := func(m Model) string {
		ls := lines(m)
		return plain(ls[len(ls)-1])
	}
	if strings.Contains(bar(m), "choque") {
		t.Fatal("no debe haber alerta todavía")
	}
	target := filepath.Join(root, "notes.txt")
	made[0].ch <- agent.FileEditProposed{Tool: "Edit", Path: target}
	for sup.Holder(target) == 0 { // el primero reclama antes de que llegue el segundo
		time.Sleep(time.Millisecond)
	}
	made[1].ch <- agent.FileEditProposed{Tool: "Edit", Path: target}

	m = pump(t, m, func(m Model) bool { return strings.Contains(bar(m), "choque") })
	if want := "⚠ choque: agente 2 quiere editar notes.txt, reclamado por agente 1"; !strings.Contains(bar(m), want) {
		t.Fatalf("barra = %q", bar(m))
	}
	// La alerta se ve aunque el panel de agentes esté plegado.
	m, _ = send(m, press('g', tea.ModCtrl))
	if m.chatOn {
		t.Fatal("el panel de agentes debía plegarse")
	}
	if !strings.Contains(bar(m), "choque") {
		t.Fatalf("barra sin panel = %q", bar(m))
	}
	checkDims(t, m, 140, 30, "con alerta")
}

func typeKeys(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, text(string(r)))
	}
	return out
}

func TestSaveIsNotAttributedToRunningAgent(t *testing.T) {
	root := project(t)
	notes := filepath.Join(root, "notes.txt")
	sup := orch.New(2)
	defer sup.Close()
	if err := sup.AttachWorkspace(orch.Workspace{Root: root, Watch: true, Debounce: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	f := newEfake()
	sup.Submit("a", f, agent.Task{Prompt: "x"}, orch.Budget{}) // único agente corriendo
	m := New(Config{Root: root, File: notes, Chat: tui.Config{Supervisor: sup}})
	m, _ = m.update(tea.WindowSizeMsg{Width: 120, Height: 30})

	changed := func() []orch.Notice {
		var out []orch.Notice
		deadline := time.After(400 * time.Millisecond)
		for {
			select {
			case u := <-sup.Updates():
				if u.Notice != nil && u.Notice.Kind == orch.NoticeChanged && u.Notice.Path == "notes.txt" {
					out = append(out, *u.Notice)
				}
			case <-deadline:
				return out
			}
		}
	}

	m, _ = send(m, text("!"))
	m, cmd := send(m, press('s', tea.ModCtrl))
	_ = feed(m, cmd)
	if got := changed(); len(got) != 0 {
		t.Fatalf("el guardado del usuario se atribuyó: %+v", got)
	}
	// Control: la misma escritura desde fuera sí se atribuye al agente.
	if err := os.WriteFile(notes, []byte("externo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2100 * time.Millisecond) // vence la ventana de escritura propia
	if err := os.WriteFile(notes, []byte("externo 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := changed()
	if len(got) == 0 || got[0].Attr != orch.AttrSoleActive {
		t.Fatalf("avisos = %+v", got)
	}
}
