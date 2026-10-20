package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
)

var (
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyRight = tea.KeyPressMsg{Code: tea.KeyRight}
)

// formHarness es un panel con formulario (Infos), cuyos agentes falsos guardan
// la Task que recibieron.
func formHarness(t *testing.T, cfgTask agent.Task) (*phar, map[string]*pfake) {
	h := &phar{t: t, sup: orch.New(2)}
	made := map[string]*pfake{}
	mk := func(name string) func() agent.Agent {
		return func() agent.Agent {
			f := newPfake()
			made[name] = f
			h.made = append(h.made, f)
			return f
		}
	}
	h.m = NewPanel(Config{
		Supervisor: h.sup,
		NewAgent:   mk("claude"),
		AgentName:  "claude",
		Agents:     map[string]func() agent.Agent{"claude": mk("claude"), "codex": mk("codex")},
		Infos: map[string]agent.Info{
			"claude": {Name: "claude", Models: []agent.ModelOption{{ID: "sonnet", Label: "Sonnet", Note: "equilibrado"}, {ID: "opus", Label: "Opus"}},
				ModelHint: "alias", Efforts: []string{"low", "high"}},
			"codex": {Name: "codex", ModelHint: "nombre del modelo"},
		},
		Task: cfgTask,
	})
	h.m.spinning = true
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	t.Cleanup(h.sup.Close)
	return h, made
}

func TestFirstMessageOpensFormAndNeverUsesRememberedModel(t *testing.T) {
	h, made := formHarness(t, agent.Task{Dir: "/p"})
	h.say("hola")
	if h.m.form == nil {
		t.Fatal("el primer mensaje sin modelo elegido debe abrir el formulario")
	}
	if got := h.m.cur().id; got != 0 {
		t.Fatalf("no debe haber arrancado nada todavía (id %d)", got)
	}
	// Enter sin elegir modelo: error, sigue abierto.
	h.press(keyEnter)
	if h.m.form == nil || !strings.Contains(h.m.form.err, "elige un modelo") {
		t.Fatalf("debía exigir modelo: %+v", h.m.form)
	}
	// Elegir Opus (derecha ×2), potencia high, y confirmar.
	h.press(keyRight)
	h.press(keyRight)
	h.press(keyDown) // potencia
	h.press(keyRight)
	h.press(keyRight)
	h.press(keyEnter)
	if h.m.form != nil {
		t.Fatalf("el formulario debía cerrarse: %+v", h.m.form)
	}
	f := made["claude"]
	f.mu.Lock()
	starts := append([]agent.Task(nil), f.starts...)
	f.mu.Unlock()
	if len(starts) != 1 {
		t.Fatalf("starts = %d", len(starts))
	}
	got := starts[0]
	if got.Model != "opus" || got.Effort != "high" || got.Prompt != "hola" || got.Permissions != agent.PermissionAsk {
		t.Errorf("task = %+v", got)
	}
}

func TestFormOtherCLIWithCustomModelAndOrchestratorRole(t *testing.T) {
	h, made := formHarness(t, agent.Task{Dir: "/p"})
	h.press(keyCtrlT) // agente nuevo: abre el formulario
	if h.m.form == nil || len(h.m.views) != 2 {
		t.Fatalf("ctrl+t debía abrir el formulario con una vista nueva: form=%v vistas=%d", h.m.form, len(h.m.views))
	}
	h.press(keyUp)    // CLI
	h.press(keyRight) // claude -> codex
	h.press(keyDown)  // modelo: solo escritura
	h.typeText("gpt-x")
	h.press(keyDown)  // potencia (no admitida)
	h.press(keyDown)  // permisos
	h.press(keyDown)  // rol
	h.press(keyRight) // orquestador
	h.press(keyEnter)
	if h.m.form != nil {
		t.Fatalf("formulario abierto: %+v", h.m.form.err)
	}
	v := h.m.cur()
	if v.cli != "codex" || v.taskModel != "gpt-x" || v.role != roleOrchestrator || !v.configured {
		t.Fatalf("vista = %+v", v)
	}
	h.say("planifica")
	f := made["codex"]
	f.mu.Lock()
	starts := append([]agent.Task(nil), f.starts...)
	f.mu.Unlock()
	if len(starts) != 1 {
		t.Fatalf("starts = %d", len(starts))
	}
	task := starts[0]
	if task.Model != "gpt-x" || task.Permissions != agent.PermissionPlan || !strings.HasPrefix(task.Prompt, orchestratorPreamble) || !strings.HasSuffix(task.Prompt, "planifica") {
		t.Errorf("task = %+v", task)
	}
}

func TestFormEscCancelsAndKeepsText(t *testing.T) {
	h, _ := formHarness(t, agent.Task{Dir: "/p"})
	h.say("mi pregunta")
	h.press(keyEsc)
	if h.m.form != nil || h.m.input.Value() != "mi pregunta" || h.m.cur().id != 0 {
		t.Errorf("Esc debe cerrar sin enviar y devolver el texto: form=%v texto=%q", h.m.form, h.m.input.Value())
	}
	h.press(keyCtrlT)
	h.press(keyEsc)
	if len(h.m.views) != 1 || h.m.form != nil {
		t.Errorf("Esc en un agente nuevo debe descartarlo: vistas=%d", len(h.m.views))
	}
}

func TestModelFlagSkipsFormForFirstAgent(t *testing.T) {
	h, made := formHarness(t, agent.Task{Dir: "/p", Model: "sonnet"})
	h.say("hola")
	if h.m.form != nil {
		t.Fatal("con -model el primer agente no pregunta")
	}
	f := made["claude"]
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != 1 || f.starts[0].Model != "sonnet" {
		t.Errorf("starts = %+v", f.starts)
	}
	// Un agente nuevo (ctrl+t) sí pregunta.
	h.m.form = nil
	h.press(keyCtrlT)
	if h.m.form == nil {
		t.Error("ctrl+t debe preguntar siempre")
	}
}

func TestDetailLineShowsWhatTheAgentRunsWith(t *testing.T) {
	h, _ := formHarness(t, agent.Task{Dir: "/p"})
	if got := plainText(h.m.detailLine(h.m.cur())); !strings.Contains(got, "sin configurar") {
		t.Errorf("antes de elegir: %q", got)
	}
	v := h.m.cur()
	mode := agent.PermissionAcceptEdits
	v.cli, v.taskModel, v.effort, v.perm, v.configured = "claude", "opus", "high", &mode, true
	got := plainText(h.m.detailLine(v))
	for _, want := range []string{"claude", "opus", "potencia high", "edita sin preguntar"} {
		if !strings.Contains(got, want) {
			t.Errorf("detalle %q sin %q", got, want)
		}
	}
	v.role = roleOrchestrator
	if got := plainText(h.m.detailLine(v)); !strings.Contains(got, "ORQUESTADOR") {
		t.Errorf("orquestador: %q", got)
	}
}

func plainText(s string) string { return ansiRe.ReplaceAllString(s, "") }
