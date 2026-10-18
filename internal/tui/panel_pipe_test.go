package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/orch/pipeline"
	"github.com/dpa23/wari/internal/workspace/checkpoint"
)

var (
	keyAltP = tea.KeyPressMsg{Code: 'p', Mod: tea.ModAlt}
	keyAltY = tea.KeyPressMsg{Code: 'y', Mod: tea.ModAlt}
	keyAltN = tea.KeyPressMsg{Code: 'n', Mod: tea.ModAlt}
	keyAltE = tea.KeyPressMsg{Code: 'e', Mod: tea.ModAlt}
)

// pipeHarness es un phar con espacio de trabajo real y un pipeline de tres
// pasos Semi (agentes falsos).
type pipeHarness struct {
	*phar
	root string
	r    *pipeline.Runner

	mu    sync.Mutex
	steps []*pfake // agentes creados por el pipeline, en orden
}

func newPipeHarness(t *testing.T) *pipeHarness {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	h := &pipeHarness{root: root, phar: &phar{t: t, sup: orch.New(2)}}
	if err := h.sup.AttachWorkspace(orch.Workspace{Root: root, Store: store, RunID: "t-"}); err != nil {
		t.Fatal(err)
	}
	fn := func() agent.Agent {
		f := newPfake()
		h.mu.Lock()
		h.steps = append(h.steps, f)
		h.mu.Unlock()
		return f
	}
	task := agent.Task{Dir: root, Permissions: agent.PermissionAcceptEdits}
	h.r, err = pipeline.New(h.sup, pipeline.Pipeline{Name: "demo", Steps: []pipeline.Step{
		{Name: "plan", Agent: fn, Prompt: "planea", Mode: pipeline.Semi},
		{Name: "impl", Agent: fn, Prompt: "implementa: {{prev}}", Mode: pipeline.Semi},
		{Name: "review", Agent: fn, Prompt: "revisa: {{prev}}", Mode: pipeline.Semi},
	}}, pipeline.Config{Task: task})
	if err != nil {
		t.Fatal(err)
	}
	h.m = NewPanel(Config{
		Supervisor: h.sup,
		Pipeline:   h.r,
		NewAgent:   func() agent.Agent { return newPfake() },
		Task:       task,
	})
	h.m.spinning = true
	h.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	t.Cleanup(func() {
		h.r.Close()
		h.sup.Close()
	})
	return h
}

func (h *pipeHarness) step(i int) *pfake {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if i >= len(h.steps) {
		h.t.Fatalf("el pipeline creó %d agentes, se pidió el %d", len(h.steps), i)
	}
	return h.steps[i]
}

func (h *pipeHarness) nSteps() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.steps)
}

// syncPipe entrega al panel el estado del pipeline y ejecuta los comandos de
// diff que pida (nunca el de escucha: no pasa por pipeMsg).
func (h *pipeHarness) syncPipe() {
	for _, cmd := range h.m.syncPipeline() {
		if msg := cmd(); msg != nil {
			h.update(msg)
		}
	}
}

func (h *pipeHarness) untilPipe(what string, cond func() bool) {
	h.t.Helper()
	h.until(what, func() bool {
		h.syncPipe()
		return cond()
	})
}

func (h *pipeHarness) untilPipeState(want pipeline.State) {
	h.t.Helper()
	h.untilPipe("pipeline "+want.String(), func() bool { return h.m.ps.State == want })
}

// runFirstStep lanza el pipeline y hace que el paso 1 edite a.txt y termine.
func (h *pipeHarness) runFirstStep() {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(h.root, "a.txt"), []byte("vieja\n"), 0o644); err != nil {
		h.t.Fatal(err)
	}
	h.press(keyAltP)
	h.untilPipe("agente del paso 1", func() bool { return h.nSteps() == 1 && len(h.m.views) == 2 })
	a := h.step(0)
	a.emit(agent.FileEditProposed{Tool: "Edit", Path: filepath.Join(h.root, "a.txt")})
	h.untilPipe("edición vista", func() bool {
		v := h.m.views[1]
		return len(v.entries) > 0 && strings.Contains(v.entries[len(v.entries)-1].text, "a.txt")
	})
	if err := os.WriteFile(filepath.Join(h.root, "a.txt"), []byte("nueva\n"), 0o644); err != nil {
		h.t.Fatal(err)
	}
	a.emit(agent.Done{Result: "PLAN LISTO", Turns: 1})
	h.untilPipeState(pipeline.GateWaiting)
}

func TestPipelineRowsAndGateWithSummaryAndDiff(t *testing.T) {
	h := newPipeHarness(t)
	h.wantView("demo · sin lanzar")
	h.wantView("· 1 plan")
	h.wantView("alt+p lanzar pipeline")

	h.runFirstStep()

	// La lista adoptó al agente del paso; el pipeline lo muestra en curso y
	// luego en el gate.
	h.wantView("1·plan")
	h.wantView("⏸ 1 plan")
	h.wantView("demo · esperando aprobación")
	// El gate sustituye al historial: resultado, archivo y diff.
	h.until("diff del gate", func() bool { return strings.Contains(h.view(), "+nueva") })
	h.wantView("gate: «plan» → «impl»")
	h.wantView("PLAN LISTO")
	h.wantView("✎ a.txt (turno 1)")
	h.wantView("-vieja")
	h.wantView("implementa: {{prev}}")
	h.wantView("alt+y aprobar")
	h.wantView("alt+n rechazar")
	h.wantView("alt+e editar prompt")
	if h.nSteps() != 1 {
		t.Fatal("el paso 2 no debe arrancar antes de aprobar")
	}

	// Aprobar sigue con el resultado del paso 1 en el prompt.
	h.press(keyAltY)
	h.untilPipe("paso 2 en curso", func() bool { return h.m.ps.State == pipeline.Running && h.m.ps.Cur == 1 })
	h.untilPipe("agente del paso 2", func() bool { return h.nSteps() == 2 && len(h.m.views) == 3 })
	if h.m.gate != nil {
		t.Fatal("el gate debe cerrarse al aprobar")
	}
	h.wantView("2·impl")
	h.wantNoView("gate: «plan»")
	if got := h.step(1).starts[0].Prompt; got != "implementa: PLAN LISTO" {
		t.Fatalf("prompt del paso 2 = %q", got)
	}
	// El historial del agente adoptado muestra su prompt.
	if v := h.m.views[2]; len(v.entries) == 0 || v.entries[0].kind != kindUser || v.entries[0].text != "implementa: PLAN LISTO" {
		t.Fatalf("historial del paso 2 = %+v", v.entries)
	}
}

func TestRejectingTheGateStopsThePipeline(t *testing.T) {
	h := newPipeHarness(t)
	h.runFirstStep()
	h.press(keyAltN)
	h.untilPipeState(pipeline.Stopped)
	h.wantView("demo · detenido")
	h.wantNoView("gate: «plan»")
	if h.m.gate != nil || h.nSteps() != 1 {
		t.Fatal("rechazar cierra el gate y no lanza nada")
	}
	// Aprobar después da un error visible, sin romper nada.
	h.press(keyAltY)
	h.until("error de aprobar", func() bool {
		return strings.Contains(h.m.cur().entries[len(h.m.cur().entries)-1].text, "no se pudo aprobar")
	})
}

func TestEditPromptAtTheGate(t *testing.T) {
	h := newPipeHarness(t)
	h.runFirstStep()
	h.m.input.SetValue("borrador a medias")

	h.press(keyAltE)
	if !h.m.gateEdit || h.m.input.Value() != "implementa: {{prev}}" {
		t.Fatalf("gateEdit=%v entrada=%q", h.m.gateEdit, h.m.input.Value())
	}
	h.wantView("editando el prompt: enter guarda, esc cancela")

	// Esc cancela y devuelve el borrador.
	h.press(keyEsc)
	if h.m.gateEdit || h.m.input.Value() != "borrador a medias" {
		t.Fatalf("tras cancelar: gateEdit=%v entrada=%q", h.m.gateEdit, h.m.input.Value())
	}
	if got := h.r.Snapshot().Gate.NextPrompt; got != "implementa: {{prev}}" {
		t.Fatalf("cancelar no debe cambiar el prompt: %q", got)
	}

	// Editar y guardar con Enter; el gate sigue abierto con el prompt nuevo.
	h.press(keyAltE)
	h.m.input.SetValue("hazlo con cuidado: {{prev}}")
	h.press(keyEnter)
	if h.m.gateEdit || h.m.input.Value() != "borrador a medias" {
		t.Fatalf("tras guardar: gateEdit=%v entrada=%q", h.m.gateEdit, h.m.input.Value())
	}
	h.untilPipe("prompt nuevo en el gate", func() bool {
		return h.m.gate != nil && h.m.gate.gate.NextPrompt == "hazlo con cuidado: {{prev}}"
	})
	h.wantView("hazlo con cuidado: {{prev}}")
	if h.nSteps() != 1 {
		t.Fatal("editar no debe lanzar el paso")
	}
	h.press(keyAltY)
	h.untilPipe("agente del paso 2", func() bool { return h.nSteps() == 2 })
	if got := h.step(1).starts[0].Prompt; got != "hazlo con cuidado: PLAN LISTO" {
		t.Fatalf("prompt = %q", got)
	}
}

func TestEscHidesTheGateAndPipelineRunShowsItAgain(t *testing.T) {
	h := newPipeHarness(t)
	h.runFirstStep()
	h.until("gate visible", func() bool { return strings.Contains(h.view(), "gate: «plan»") })

	h.press(keyEsc)
	h.wantNoView("gate: «plan»")
	if got := h.step(0).get(&h.step(0).cancels); got != 0 {
		t.Fatal("Esc con el gate abierto solo lo oculta")
	}
	// Con el gate oculto, aprobar y rechazar siguen valiendo; lo visible es el
	// aviso de la línea de estado.
	h.wantView("gate: alt+y aprobar")

	h.press(keyAltP)
	h.wantView("gate: «plan» → «impl»")
}

func TestGateWithoutCheckpointShowsNoDiffNote(t *testing.T) {
	h := newPipeHarness(t)
	h.press(keyAltP)
	h.untilPipe("agente del paso 1", func() bool { return h.nSteps() == 1 })
	a := h.step(0)
	// Una edición fuera de la raíz no tiene checkpoint.
	a.emit(agent.FileEditProposed{Tool: "Edit", Path: "/fuera/de/la/raiz.txt"})
	a.emit(agent.Done{Result: "ok"})
	h.untilPipeState(pipeline.GateWaiting)
	h.wantView("/fuera/de/la/raiz.txt")
	h.wantView("(sin checkpoint: no hay diff)")
}

func TestPipelineFailureIsShown(t *testing.T) {
	h := newPipeHarness(t)
	h.press(keyAltP)
	h.untilPipe("agente del paso 1", func() bool { return h.nSteps() == 1 })
	h.step(0).emit(agent.Done{Result: "reventó", IsError: true})
	h.untilPipeState(pipeline.Failed)
	h.wantView("demo · falló")
	h.wantView("✗ 1 plan")
}

func TestPipelineLayoutFitsAnySize(t *testing.T) {
	h := newPipeHarness(t)
	for _, size := range [][2]int{{80, 40}, {80, 24}, {60, 12}, {40, 9}, {30, 7}, {20, 5}, {20, 3}, {10, 1}} {
		h.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		if got := lipgloss.Height(h.m.Body()); got > size[1] {
			t.Errorf("%dx%d: el cuerpo ocupa %d filas", size[0], size[1], got)
		}
		if c := h.m.Cursor(); c != nil && c.Position.Y >= size[1] {
			t.Errorf("%dx%d: el cursor cae en la fila %d", size[0], size[1], c.Position.Y)
		}
	}
	// Sin espacio para un paso por fila, una sola línea compacta.
	h.update(tea.WindowSizeMsg{Width: 100, Height: 8})
	if h.m.pipeH != 1 {
		t.Fatalf("pipeH = %d, se esperaba la línea compacta", h.m.pipeH)
	}
	h.wantView("demo · sin lanzar · ·plan ·impl ·review")
}

func TestPipelineKeysAreFreeAndConfigurable(t *testing.T) {
	km := DefaultKeyMap()
	for name, b := range map[string]interface{ Enabled() bool }{
		"pipeline_run": km.PipelineRun, "gate_approve": km.GateApprove,
		"gate_reject": km.GateReject, "gate_edit": km.GateEdit,
	} {
		if !b.Enabled() {
			t.Errorf("%s sin tecla en el mapa predeterminado", name)
		}
	}
}

func TestLoopNoticeShowsAlertAndPausesAgent(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.say("hola")
	h.untilState(0, "running")
	for range 3 {
		h.made[0].emit(agent.ToolCall{ID: "t", Name: "Bash", Input: json.RawMessage(`{"command":"make"}`)})
	}
	h.untilState(0, "paused")
	if !strings.Contains(h.m.Alert(), "bucle") {
		t.Fatalf("Alert() = %q", h.m.Alert())
	}
	h.wantView("⚠ bucle")
	h.wantView("⚠ bucle: agente 1 llamó a Bash con los mismos argumentos 3 veces seguidas")
	h.wantView("ctrl+o reanuda")

	// Reanudar quita la alerta.
	h.press(keyCtrlO)
	h.untilState(0, "running")
	if h.m.Alert() != "" {
		t.Fatalf("Alert() tras reanudar = %q", h.m.Alert())
	}
}
