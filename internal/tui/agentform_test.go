package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/tui/theme"
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

func TestFormBlocksMissingCLIAndWarnsAboutGuarantees(t *testing.T) {
	h, _ := formHarness(t, agent.Task{Dir: "/p"})
	info := h.m.cfg.Infos["claude"]
	info.Missing = true
	h.m.cfg.Infos["claude"] = info
	h.say("hola")
	h.press(keyRight) // modelo
	h.press(keyEnter)
	if h.m.form == nil || !strings.Contains(h.m.form.err, "no está en el PATH") {
		t.Fatalf("una CLI no instalada no puede crear el agente: %+v", h.m.form)
	}
	if rows := strings.Join(h.m.formRows(100, 12), "\n"); !strings.Contains(rows, "✗ claude (no instalado)") {
		t.Errorf("el formulario debe marcar las CLIs no instaladas:\n%s", rows)
	}

	// Una nota de la CLI aparece cuando se elige lo que no garantiza.
	info.Missing = false
	info.PlanNote = "planear no impide escribir"
	h.m.cfg.Infos["claude"] = info
	h.m.form.infos[h.m.form.cli] = info
	h.m.form.err = ""
	h.m.form.perm = 2 // solo planear
	if rows := strings.Join(h.m.formRows(100, 12), "\n"); !strings.Contains(rows, "planear no impide escribir") {
		t.Errorf("falta la nota de PlanNote:\n%s", rows)
	}
}

func TestPromptHistoryWithArrows(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.say("uno")
	if got := h.m.promptHist; len(got) != 1 || got[0] != "uno" {
		t.Fatalf("enviar debe guardar el mensaje: %v", got)
	}
	h.m.promptHist = append(h.m.promptHist, "dos")
	h.press(keyUp)
	if h.m.input.Value() != "dos" {
		t.Fatalf("↑ debía traer el último: %q", h.m.input.Value())
	}
	h.press(keyUp)
	if h.m.input.Value() != "uno" {
		t.Fatalf("↑ otra vez trae el anterior: %q", h.m.input.Value())
	}
	h.press(keyDown)
	h.press(keyDown)
	if h.m.input.Value() != "" {
		t.Fatalf("↓ al final vuelve a la entrada vacía: %q", h.m.input.Value())
	}
	// Con texto propio, ↑ no pisa lo escrito.
	h.typeText("borrador")
	h.press(keyUp)
	if h.m.input.Value() != "borrador" {
		t.Errorf("↑ con texto propio lo cambió: %q", h.m.input.Value())
	}
}

func TestSlashThemeAppliesAndPersists(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", cfg)
	defer theme.SetDark(true)
	theme.SetDark(true)

	h := newPharness(t, 2, orch.Budget{})
	h.say("/theme light")
	if theme.Dark() {
		t.Error("/theme light debía dejar el tema claro")
	}
	dir, _ := config.UserDir()
	if got, err := config.LoadSettings(dir, ""); err != nil || got.Theme != "light" {
		t.Errorf("settings guardados = %+v, %v", got, err)
	}
	if h.m.cur().id != 0 {
		t.Error("un comando / de WARI no se envía al agente")
	}
	h.say("/theme rojo")
	if !strings.Contains(strings.Join(entryTexts(h.m.cur()), "\n"), "no es auto, dark ni light") {
		t.Error("un valor inválido debe avisar")
	}
	h.say("/keys")
	if !strings.Contains(strings.Join(entryTexts(h.m.cur()), "\n"), "keys.json") {
		t.Error("/keys debe decir dónde se cambian los atajos")
	}
}

func entryTexts(v *agentView) []string {
	var out []string
	for _, e := range v.entries {
		out = append(out, e.text)
	}
	return out
}

func TestDiscoveredModelsReplaceFallbackAndKeepChoice(t *testing.T) {
	h, _ := formHarness(t, agent.Task{Dir: "/p"})
	calls := 0
	info := h.m.cfg.Infos["claude"]
	info.Discover = func(context.Context) ([]agent.ModelOption, error) {
		calls++
		return []agent.ModelOption{{ID: "nuevo-1", Label: "Nuevo 1"}, {ID: "opus", Label: "Opus real"}}, nil
	}
	h.m.cfg.Infos["claude"] = info

	cmd := h.update(tea.KeyPressMsg{Code: tea.KeyEnter}) // sin texto: nada
	_ = cmd
	h.m.input.SetValue("hola")
	m, cmd := h.m.send()
	h.m = m
	if h.m.form == nil || cmd == nil {
		t.Fatalf("abrir el formulario debe lanzar la consulta de modelos: form=%v cmd=%v", h.m.form, cmd != nil)
	}
	if rows := strings.Join(h.m.formRows(100, 12), "\n"); !strings.Contains(rows, "buscando los modelos de claude") {
		t.Errorf("debe avisar que busca:\n%s", rows)
	}
	h.press(keyRight) // Sonnet (respaldo) -> índice 0
	h.press(keyRight) // Opus (respaldo) -> índice 1
	h.update(cmd())
	if calls != 1 {
		t.Fatalf("Discover llamado %d veces", calls)
	}
	f := h.m.form
	if len(f.info().Models) != 2 || f.info().Models[0].ID != "nuevo-1" {
		t.Fatalf("la lista descubierta debe reemplazar la de respaldo: %+v", f.info().Models)
	}
	if f.chosenModel() != "opus" {
		t.Errorf("el modelo elegido (opus) sigue existiendo y se conserva: %q", f.chosenModel())
	}
	// Una segunda apertura usa la lista guardada y no vuelve a consultar.
	h.press(keyEsc)
	m, cmd = h.m.openForm(h.m.cur(), "", false)
	h.m = m
	if cmd != nil || h.m.form.info().Models[0].ID != "nuevo-1" {
		t.Errorf("la segunda apertura debe usar la caché: cmd=%v", cmd != nil)
	}
}

func TestDiscoverFailureKeepsFallback(t *testing.T) {
	h, _ := formHarness(t, agent.Task{Dir: "/p"})
	info := h.m.cfg.Infos["claude"]
	info.Discover = func(context.Context) ([]agent.ModelOption, error) { return nil, errors.New("sin red") }
	h.m.cfg.Infos["claude"] = info
	h.m.input.SetValue("hola")
	m, cmd := h.m.send()
	h.m = m
	h.update(cmd())
	f := h.m.form
	if len(f.info().Models) != 2 || f.info().Models[0].ID != "sonnet" {
		t.Errorf("con error se queda la lista de respaldo: %+v", f.info().Models)
	}
	if rows := strings.Join(h.m.formRows(100, 12), "\n"); !strings.Contains(rows, "lista de respaldo") {
		t.Errorf("debe avisar del respaldo:\n%s", rows)
	}
}
