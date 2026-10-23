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
	"github.com/dpa23/wari/internal/orch/pipeline"
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
	m := New(Config{ShowChat: true, Root: root, Chat: tui.Config{
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
	if m.chatOn() {
		t.Fatal("el panel de agentes debía plegarse")
	}
	if !strings.Contains(bar(m), "choque") {
		t.Fatalf("barra sin panel = %q", bar(m))
	}
	checkDims(t, m, 140, 30, "con alerta")
}

func TestConflictBannerFoldedAndAssistedResolution(t *testing.T) {
	root := project(t)
	sup := orch.New(2)
	defer sup.Close()
	if err := sup.AttachWorkspace(orch.Workspace{Root: root}); err != nil {
		t.Fatal(err)
	}
	var made []*efake
	m := New(Config{ShowChat: true, Root: root, Chat: tui.Config{
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

	target := filepath.Join(root, "notes.txt")
	made[0].ch <- agent.FileEditProposed{Tool: "Edit", Path: target}
	for sup.Holder(target) == 0 {
		time.Sleep(time.Millisecond)
	}
	made[1].ch <- agent.FileEditProposed{Tool: "Edit", Path: target}

	m = pump(t, m, func(m Model) bool { return strings.Contains(bar(m), "choque") })
	if !strings.Contains(bar(m), "[f6 resolver]") {
		t.Fatalf("banner debía incluir el atajo del resolver: %q", bar(m))
	}

	// Plegar panel de agentes
	m, _ = send(m, press('g', tea.ModCtrl))
	if m.chatOn() {
		t.Fatal("el panel de agentes debía estar plegado")
	}
	// El banner sigue visible con panel plegado
	if !strings.Contains(bar(m), "choque") || !strings.Contains(bar(m), "[f6 resolver]") {
		t.Fatalf("banner sin panel = %q", bar(m))
	}

	// F6 despliega el panel, lo enfoca y muestra las opciones asistidas
	m, cmd := send(m, press(tea.KeyF6, 0))
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m, _ = m.update(msg)
		}
	}
	if !m.chatOn() {
		t.Fatal("F6 debía desplegar el panel de agentes")
	}
	if m.focus != focusChat {
		t.Fatalf("F6 debía enfocar el panel de agentes: foco = %v", m.focus)
	}
	if !strings.Contains(plain(m.render()), "[1] ceder archivo (reanudar)") {
		t.Fatalf("render debía mostrar opción 1 ceder: %q", plain(m.render()))
	}
	if !strings.Contains(plain(m.render()), "[2] detener") {
		t.Fatalf("render debía mostrar opción 2 detener: %q", plain(m.render()))
	}

	// Pulsar 1 cede el archivo y reanuda
	m, cmd = send(m, text("1"))
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m, _ = m.update(msg)
		}
	}
	m = pump(t, m, func(m Model) bool { return !strings.Contains(bar(m), "choque") })
	if strings.Contains(bar(m), "choque") {
		t.Fatalf("tras ceder, choque debía desaparecer: %q", bar(m))
	}
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
	m := New(Config{ShowChat: true, Root: root, File: notes, Chat: tui.Config{Supervisor: sup}})
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

// Las teclas del pipeline y del gate valen con cualquier foco y, si no hay
// pipeline, siguen su camino normal al panel con foco.
func TestPipelineKeysAreGlobalOnlyWithPipeline(t *testing.T) {
	root := project(t)
	sup := orch.New(2)
	defer sup.Close()
	fn := func() agent.Agent { return newEfake() }
	r, err := pipeline.New(sup, pipeline.Pipeline{Name: "p", Steps: []pipeline.Step{
		{Name: "uno", Agent: fn, Prompt: "x", Mode: pipeline.Semi},
		{Name: "dos", Agent: fn, Prompt: "y", Mode: pipeline.Semi},
	}}, pipeline.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	m := New(Config{ShowChat: true, Root: root, Chat: tui.Config{Supervisor: sup, Pipeline: r, NewAgent: fn}})
	m, _ = m.update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = m.focusPanel(focusTree)

	// alt+p lanza el pipeline aunque el foco esté en el árbol.
	m, cmd := send(m, press('p', tea.ModAlt))
	if cmd == nil {
		t.Fatal("alt+p debe llegar al panel de agentes")
	}
	if msg := cmd(); msg != nil {
		m, _ = m.update(msg)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(sup.Snapshots()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("alt+p no lanzó el primer paso")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if m.focus != focusTree {
		t.Fatal("lanzar el pipeline no debe mover el foco")
	}

	// Sin pipeline la tecla no se intercepta.
	noPipe := New(Config{ShowChat: true, Root: root, Chat: tui.Config{Supervisor: sup, NewAgent: fn}})
	noPipe, _ = noPipe.update(tea.WindowSizeMsg{Width: 140, Height: 30})
	noPipe = noPipe.focusPanel(focusTree)
	if _, cmd := send(noPipe, press('e', tea.ModAlt)); cmd != nil {
		t.Fatal("sin pipeline, alt+e no debe hacer nada")
	}
}

// TestDirectPanelFocusEscapingEditor comprueba que las acciones directas de foco
// (focus_tree con alt+1, focus_editor con alt+2, focus_agents con alt+3)
// escapan del editor aunque esté recibiendo texto y desplegan los paneles si estaban ocultos.
func TestDirectPanelFocusEscapingEditor(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 140, 30)
	target := filepath.Join(root, "notes.txt")
	m.openFile(target)
	if !m.hasEd || m.focus != focusEditor {
		t.Fatalf("editor debió abrirse con foco, focus=%v", m.focus)
	}

	// El usuario escribe en el editor; tab normalmente insertaría sangría
	m, _ = send(m, typeKeys("texto")...)
	if m.focus != focusEditor {
		t.Fatalf("foco debió permanecer en editor al escribir, focus=%v", m.focus)
	}

	// alt+1 salta directamente al árbol de archivos
	m, _ = send(m, press('1', tea.ModAlt))
	if m.focus != focusTree {
		t.Fatalf("alt+1 debió enfocar el árbol, focus=%v", m.focus)
	}

	// alt+3 salta directamente al panel de agentes
	m, _ = send(m, press('3', tea.ModAlt))
	if m.focus != focusChat {
		t.Fatalf("alt+3 debió enfocar agentes, focus=%v", m.focus)
	}

	// alt+2 salta de vuelta al editor
	m, _ = send(m, press('2', tea.ModAlt))
	if m.focus != focusEditor {
		t.Fatalf("alt+2 debió enfocar editor, focus=%v", m.focus)
	}

	// Con el árbol oculto, alt+1 lo despliega y le da el foco
	m.setTreeOn(false)
	m.relayout()
	if m.treeOn() {
		t.Fatal("árbol debía estar plegado")
	}
	m, _ = send(m, press('1', tea.ModAlt))
	if !m.treeOn() || m.focus != focusTree {
		t.Fatalf("alt+1 debió desplegar y enfocar árbol: treeOn=%v focus=%v", m.treeOn(), m.focus)
	}

	// Con el chat oculto, alt+3 lo despliega y le da el foco
	m.setChatOn(false)
	m.relayout()
	if m.chatOn() {
		t.Fatal("chat debía estar plegado")
	}
	m, _ = send(m, press('3', tea.ModAlt))
	if !m.chatOn() || m.focus != focusChat {
		t.Fatalf("alt+3 debió desplegar y enfocar chat: chatOn=%v focus=%v", m.chatOn(), m.focus)
	}
}

// TestTreeMarkersAndEditorHeaderAgentIndicators comprueba que los reclamos y
// ediciones de agentes se reflejan en el árbol de archivos (●1, ✎1, ⚠) y en la cabecera
// del editor ([reclamado por agente 1], ⚠ [choque]).
func TestTreeMarkersAndEditorHeaderAgentIndicators(t *testing.T) {
	root := project(t)
	sup := orch.New(2)
	defer sup.Close()
	if err := sup.AttachWorkspace(orch.Workspace{Root: root}); err != nil {
		t.Fatal(err)
	}
	var made []*efake
	m := New(Config{ShowChat: true, Root: root, Chat: tui.Config{
		Supervisor: sup,
		NewAgent:   func() agent.Agent { f := newEfake(); made = append(made, f); return f },
	}})
	m, _ = m.update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = m.focusPanel(focusChat)

	// Iniciar agente 1 y agente 2
	m, _ = send(m, typeKeys("uno")...)
	m, _ = send(m, press(tea.KeyEnter, 0))
	m, _ = send(m, press('t', tea.ModCtrl))
	m, _ = send(m, typeKeys("dos")...)
	m, _ = send(m, press(tea.KeyEnter, 0))
	if len(made) != 2 {
		t.Fatalf("agentes = %d", len(made))
	}

	target := filepath.Join(root, "notes.txt")
	made[0].ch <- agent.FileEditProposed{Tool: "Edit", Path: target}
	for sup.Holder(target) == 0 {
		time.Sleep(time.Millisecond)
	}

	// Abrir el archivo en el editor
	m.openFile(target)
	m.updateTreeMarkers()

	// 1. Título del editor debe indicar que el archivo está reclamado por el agente 1
	title := m.editorTitle()
	if !strings.Contains(title, "[reclamado por agente 1]") {
		t.Fatalf("título del editor = %q, want [reclamado por agente 1]", title)
	}

	// 2. Vista del árbol debe contener el indicador ●1
	_ = m.render()
	treeView := plain(m.tree.View())
	if !strings.Contains(treeView, "●1") {
		t.Fatalf("árbol debe mostrar ●1 para notes.txt:\n%s", treeView)
	}

	// 3. Provocar choque con el segundo agente
	made[1].ch <- agent.FileEditProposed{Tool: "Edit", Path: target}
	m = pump(t, m, func(m Model) bool {
		return m.chat.Alert() != ""
	})

	_ = m.render()
	// Título del editor ahora debe indicar el choque
	titleWithConflict := m.editorTitle()
	if !strings.Contains(titleWithConflict, "[reclamado por agente 1]") || !strings.Contains(titleWithConflict, "⚠ [choque]") {
		t.Fatalf("título del editor en choque = %q, want [reclamado por agente 1] y ⚠ [choque]", titleWithConflict)
	}

	// Árbol debe mostrar el indicador de choque ⚠
	treeViewWithConflict := plain(m.tree.View())
	if !strings.Contains(treeViewWithConflict, "⚠") {
		t.Fatalf("árbol debe mostrar ⚠ tras choque:\n%s", treeViewWithConflict)
	}
}
