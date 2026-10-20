package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
)

// fakeAgent es un agent.Agent de pruebas: registra Start, Cancel y Close, y
// expone su canal de eventos. Close cierra el canal, como los adaptadores
// reales.
type fakeAgent struct {
	mu       sync.Mutex
	starts   []agent.Task
	cancels  int
	closes   int
	status   agent.Status
	startErr error
	ch       chan agent.Event
	once     sync.Once
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{ch: make(chan agent.Event, 16)}
}

func (f *fakeAgent) Start(_ context.Context, t agent.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, t)
	if f.startErr != nil {
		return f.startErr
	}
	f.status = agent.StatusRunning
	return nil
}
func (f *fakeAgent) Send(string) error          { return nil }
func (f *fakeAgent) Pause() error               { return nil }
func (f *fakeAgent) Resume() error              { return nil }
func (f *fakeAgent) Events() <-chan agent.Event { return f.ch }
func (f *fakeAgent) Cancel() error {
	f.mu.Lock()
	f.cancels++
	f.status = agent.StatusIdle
	f.mu.Unlock()
	return nil
}
func (f *fakeAgent) Close() error {
	f.mu.Lock()
	f.closes++
	f.status = agent.StatusDone
	f.mu.Unlock()
	f.once.Do(func() { close(f.ch) })
	return nil
}
func (f *fakeAgent) Status() agent.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}
func (f *fakeAgent) startsCopy() []agent.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.Task(nil), f.starts...)
}
func (f *fakeAgent) cancelCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancels
}
func (f *fakeAgent) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// harness conduce el modelo como lo haría Bubble Tea.
type harness struct {
	t  *testing.T
	m  Model
	ag *fakeAgent
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, ag: newFakeAgent()}
	h.m = New(Config{
		Agent: h.ag,
		Task:  agent.Task{Dir: "/proyecto", Model: "m1"},
	})
	h.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	t.Cleanup(func() {
		_ = h.ag.Close() // libera los Cmd de escucha bloqueados
	})
	return h
}

func (h *harness) update(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	next, cmd := h.m.Update(msg)
	h.m = next.(Model)
	return cmd
}

// cmdTimeout es lo que se espera a un Cmd antes de darlo por bloqueado (el de
// escucha bloquea hasta que haya un evento).
const cmdTimeout = 100 * time.Millisecond

// run ejecuta cmd (aplanando Batch y Sequence), y devuelve los mensajes de los
// Cmd que terminaron a tiempo.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	results := make(chan tea.Msg, 8)
	pending := 0
	launch := func(c tea.Cmd) {
		if c == nil {
			return
		}
		pending++
		go func() { results <- c() }()
	}
	launch(cmd)
	var out []tea.Msg
	deadline := time.After(cmdTimeout)
	for pending > 0 {
		select {
		case msg := <-results:
			pending--
			switch b := msg.(type) {
			case tea.BatchMsg:
				for _, c := range b {
					launch(c)
				}
			default:
				out = append(out, msg)
			}
		case <-deadline:
			return out
		}
	}
	return out
}

// pump ejecuta cmd y entrega sus mensajes al modelo (sin seguir los Cmd que
// devuelva el modelo). Devuelve los mensajes.
func (h *harness) pump(cmd tea.Cmd) []tea.Msg {
	h.t.Helper()
	msgs := run(cmd)
	for _, msg := range msgs {
		h.update(msg)
	}
	return msgs
}

func (h *harness) press(k tea.KeyPressMsg) tea.Cmd { return h.update(k) }

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

// submit escribe text y pulsa enter, y entrega los mensajes de los Cmd.
func (h *harness) submit(text string) {
	h.t.Helper()
	h.typeText(text)
	h.pump(h.press(keyEnter))
}

func (h *harness) event(ev agent.Event) tea.Cmd {
	return h.update(eventsMsg{evs: []agent.Event{ev}})
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;:?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

func (h *harness) view() string {
	return ansiRe.ReplaceAllString(h.m.View().Content, "")
}

func (h *harness) wantView(sub string) {
	h.t.Helper()
	if v := h.view(); !strings.Contains(v, sub) {
		h.t.Errorf("la vista no contiene %q:\n%s", sub, v)
	}
}

func (h *harness) wantNoView(sub string) {
	h.t.Helper()
	if v := h.view(); strings.Contains(v, sub) {
		h.t.Errorf("la vista contiene %q:\n%s", sub, v)
	}
}

func TestEnviarLanzaStart(t *testing.T) {
	h := newHarness(t)
	h.submit("hola")
	starts := h.ag.startsCopy()
	if len(starts) != 1 {
		t.Fatalf("Start llamado %d veces, quiero 1", len(starts))
	}
	if starts[0].Prompt != "hola" || starts[0].SessionID != "" {
		t.Errorf("Task = %+v, quiero Prompt hola y SessionID vacío", starts[0])
	}
	if starts[0].Dir != "/proyecto" || starts[0].Model != "m1" {
		t.Errorf("Task no conserva la plantilla: %+v", starts[0])
	}
	if !h.m.busy {
		t.Error("tras enviar debería haber un turno en curso")
	}
	if got := h.m.input.Value(); got != "" {
		t.Errorf("la entrada debería vaciarse, tiene %q", got)
	}
	h.wantView("hola")
	h.wantView("pensando")
}

func TestTurnoCompletoMuestraRespuestaYVuelveALibre(t *testing.T) {
	h := newHarness(t)
	h.submit("hola")
	h.event(agent.Started{SessionID: "S1-abcdefghij", Model: "m1"})
	h.event(agent.TextDelta{Text: "ho"})
	h.event(agent.TextDelta{Text: "la mundo"})
	h.event(agent.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: 0.01})
	h.event(agent.Done{SessionID: "S1-abcdefghij"})
	h.wantView("hola mundo")
	h.wantView("libre")
	h.wantView("sesión S1-abcde")
	h.wantNoView("S1-abcdef")
	h.wantView("↑10 ↓5")
	h.wantView("US$0.0100")
	if h.m.busy {
		t.Error("tras Done no debería haber turno en curso")
	}
	if h.m.sessionID != "S1-abcdefghij" {
		t.Errorf("sessionID = %q", h.m.sessionID)
	}
}

func TestSegundoEnvioReutilizaSesion(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.event(agent.Started{SessionID: "S1"})
	h.event(agent.Done{SessionID: "S1"})
	h.submit("dos")
	starts := h.ag.startsCopy()
	if len(starts) != 2 || starts[1].Prompt != "dos" || starts[1].SessionID != "S1" {
		t.Errorf("starts = %+v", starts)
	}
}

func TestCancelarYReanudar(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.event(agent.Started{SessionID: "S1"})
	h.event(agent.TextDelta{Text: "parcial"})

	cmd := h.press(keyEsc)
	if cmd == nil {
		t.Fatal("esc durante un turno debería devolver un Cmd (Cancel)")
	}
	if h.ag.cancelCount() != 0 {
		t.Fatal("Cancel no debe ejecutarse dentro de Update")
	}
	h.wantView("cancelando…")

	// Mientras se cancela, enter no envía.
	h.typeText("dos")
	h.pump(h.press(keyEnter))
	if len(h.ag.startsCopy()) != 1 {
		t.Fatal("no se debe enviar mientras se cancela")
	}

	msgs := h.pump(cmd)
	if len(msgs) != 1 {
		t.Fatalf("el Cmd de cancelar devolvió %d mensajes", len(msgs))
	}
	if h.ag.cancelCount() != 1 {
		t.Errorf("Cancel llamado %d veces, quiero 1", h.ag.cancelCount())
	}
	if h.ag.closeCount() != 0 {
		t.Error("cancelar no debe cerrar al agente")
	}
	h.wantView("turno cancelado")
	h.wantView("libre")

	h.pump(h.press(keyEnter)) // la entrada conservaba "dos"
	starts := h.ag.startsCopy()
	if len(starts) != 2 || starts[1].Prompt != "dos" || starts[1].SessionID != "S1" {
		t.Errorf("el agente se reutiliza y reanuda S1: %+v", starts)
	}
}

func TestEventosDuranteCancelacionSeDescartan(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.event(agent.Started{SessionID: "S1"})
	cancel := h.press(keyEsc)

	// Lo que llegue antes de que Cancel vuelva es del turno cancelado; el
	// panel sigue escuchando.
	if cmd := h.update(eventsMsg{evs: []agent.Event{agent.TextDelta{Text: "fantasma"}}}); cmd == nil {
		t.Error("tras descartar un lote el panel debe seguir escuchando")
	}
	h.update(eventsMsg{evs: []agent.Event{agent.Done{SessionID: "OTRA"}}})
	h.wantNoView("fantasma")
	if h.m.sessionID != "S1" {
		t.Errorf("sessionID = %q, un evento descartado no debe cambiarlo", h.m.sessionID)
	}

	h.pump(cancel)
	h.submit("dos")
	h.event(agent.TextDelta{Text: "real"})
	h.wantView("real")
}

func TestEscConStartEnVueloCancelaAlVolver(t *testing.T) {
	h := newHarness(t)
	h.typeText("uno")
	start := h.press(keyEnter)
	msgs := run(start) // incluye startMsg; todavía no se entrega al modelo
	if cmd := h.press(keyEsc); cmd != nil {
		t.Fatal("con Start en vuelo, Cancel todavía no tiene turno que cancelar")
	}
	h.wantView("cancelando…")
	var cancel tea.Cmd
	for _, msg := range msgs {
		if sm, ok := msg.(startMsg); ok {
			cancel = h.update(sm)
		}
	}
	if cancel == nil {
		t.Fatal("al volver Start debería lanzarse la cancelación pendiente")
	}
	h.pump(cancel)
	if h.ag.cancelCount() != 1 {
		t.Errorf("Cancel llamado %d veces, quiero 1", h.ag.cancelCount())
	}
	h.wantView("turno cancelado")
	h.wantView("libre")
}

func TestErrorYDoneConErrorSeMuestran(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.event(agent.Error{Err: errors.New("se rompió todo")})
	h.event(agent.Done{IsError: true, Result: "sin crédito"})
	h.wantView("error: se rompió todo")
	h.wantView("el turno terminó con error: sin crédito")
	if h.m.busy {
		t.Error("tras Done con error el estado debe quedar libre")
	}
}

func TestHerramientasEdicionesYDenegados(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	long := json.RawMessage(`{"command":"` + strings.Repeat("x", 400) + `"}`)
	h.event(agent.ToolCall{ID: "t1", Name: "Bash", Input: long})
	h.event(agent.ToolCall{ID: "t2", Name: "Read", Input: json.RawMessage("{\n \"file\": \"a.go\"\n}")})
	h.event(agent.FileEditProposed{ToolCallID: "t3", Tool: "Edit", Path: "internal/x.go"})
	h.event(agent.NeedsApproval{Tool: "Write", Input: json.RawMessage(`{"path":"z"}`), Denied: true})
	h.wantView("⚙ Bash")
	h.wantView("xxxx")
	h.wantView(`⚙ Read {"file":"a.go"}`)
	h.wantView("✎ internal/x.go")
	h.wantView("⛔ denegado: Write z")
	if got := summarize(long); len([]rune(got)) > maxSummary || !strings.HasSuffix(got, "…") {
		t.Errorf("summarize no trunca: %q", got)
	}
}

func TestSummarizeDecodificaEscapes(t *testing.T) {
	if got := summarize(json.RawMessage(`{"Path":"/a/b.go"}`)); got != "/a/b.go" {
		t.Errorf("clave con mayúscula: %q", got)
	}
	in := json.RawMessage(`{"command":"/bin/zsh -lc \"go build \u0026\u0026 go test\""}`)
	if got, want := summarize(in), `/bin/zsh -lc "go build && go test"`; got != want {
		t.Errorf("summarize = %q, quiero %q", got, want)
	}
	other := json.RawMessage(`{"a":"x\u0026y","b":1}`)
	if got := summarize(other); got != `{"a":"x&y","b":1}` {
		t.Errorf("sin campo principal: %q", got)
	}
}

func TestEntradaVaciaOEnTurnoNoEnvia(t *testing.T) {
	h := newHarness(t)
	if cmd := h.press(keyEnter); cmd != nil {
		t.Error("enter con la entrada vacía no debe hacer nada")
	}
	h.typeText("   ")
	if cmd := h.press(keyEnter); cmd != nil {
		t.Error("enter con solo espacios no debe hacer nada")
	}
	if n := len(h.ag.startsCopy()); n != 0 {
		t.Fatalf("no debería haberse lanzado ningún turno, hay %d", n)
	}

	h.m.input.Reset()
	h.submit("uno")
	h.typeText("dos")
	if cmd := h.press(keyEnter); cmd != nil {
		t.Error("enter durante un turno no debe lanzar otro")
	}
	if got := len(h.ag.startsCopy()); got != 1 {
		t.Errorf("Start llamado %d veces, quiero 1", got)
	}
	if h.m.input.Value() != "dos" {
		t.Errorf("la entrada debería conservar el borrador, tiene %q", h.m.input.Value())
	}
}

func TestCtrlCCierraAlAgenteYSale(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	cmd := h.press(keyCtrlC)
	if cmd == nil {
		t.Fatal("ctrl+c debería devolver un Cmd")
	}
	if h.ag.closeCount() != 0 {
		t.Fatal("Close no debe ejecutarse dentro de Update")
	}
	h.wantView("saliendo…")
	h.wantNoView("pensando…")
	var quitMsgs []tea.Msg
	for _, msg := range run(cmd) {
		if _, ok := msg.(quitMsg); ok {
			quitMsgs = append(quitMsgs, msg)
		}
	}
	if h.ag.closeCount() != 1 {
		t.Fatalf("Close llamado %d veces, quiero 1", h.ag.closeCount())
	}
	if len(quitMsgs) != 1 {
		t.Fatalf("quitMsg recibidos = %d, quiero 1", len(quitMsgs))
	}
	quit := h.update(quitMsgs[0])
	if quit == nil {
		t.Fatal("tras Close debería haber un Cmd de salida")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Error("el Cmd final debería ser tea.Quit")
	}
	// Un segundo ctrl+c mientras sale no vuelve a cerrar.
	if cmd := h.press(keyCtrlC); cmd != nil {
		t.Error("ctrl+c repetido no debe hacer nada")
	}
}

func TestCtrlCSinAgenteSaleDirecto(t *testing.T) {
	m := New(Config{})
	_, cmd := m.Update(keyCtrlC)
	if cmd == nil {
		t.Fatal("ctrl+c debería salir")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("debería ser tea.Quit")
	}
}

func TestCtrlCDuranteCancelacionCierraAlAgente(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.press(keyEsc) // Cancel todavía no corre
	run(h.press(keyCtrlC))
	if h.ag.closeCount() != 1 {
		t.Errorf("Close llamado %d veces, quiero 1", h.ag.closeCount())
	}
}

func TestListenReemiteTrasCadaEvento(t *testing.T) {
	h := newHarness(t)
	h.typeText("hola")
	cmd := h.press(keyEnter)
	h.ag.ch <- agent.Started{SessionID: "S9"}
	var got []tea.Msg
	for _, msg := range run(cmd) {
		if _, ok := msg.(eventsMsg); ok {
			got = append(got, msg)
		}
	}
	if len(got) != 1 {
		t.Fatalf("eventos recibidos = %d, quiero 1", len(got))
	}
	next := h.update(got[0])
	if next == nil {
		t.Fatal("tras un evento debe volver a escuchar")
	}
	h.ag.ch <- agent.TextDelta{Text: "x"}
	msgs := run(next)
	if len(msgs) != 1 {
		t.Fatalf("mensajes = %d", len(msgs))
	}
	if _, ok := msgs[0].(eventsMsg); !ok {
		t.Errorf("mensaje inesperado %#v", msgs[0])
	}
}

func TestCanalCerradoInesperado(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.update(eventsMsg{closed: true})
	h.wantView("terminó inesperadamente")
	if h.m.busy {
		t.Error("debería quedar libre")
	}
	h.typeText("dos")
	h.pump(h.press(keyEnter))
	h.wantView("hay que reiniciar WARI")
	if n := len(h.ag.startsCopy()); n != 1 {
		t.Errorf("con el agente muerto no se lanzan turnos, Start llamado %d veces", n)
	}
}

func TestErrorDeStartSeMuestra(t *testing.T) {
	h := newHarness(t)
	h.typeText("uno")
	cmd := h.press(keyEnter)
	h.ag.startErr = errors.New("sin binario")
	h.pump(cmd)
	h.wantView("no se pudo iniciar el turno: sin binario")
	if h.m.busy {
		t.Error("tras fallar Start no debe quedar turno en curso")
	}
}

func TestRedimensionarNoDesborda(t *testing.T) {
	h := newHarness(t)
	h.submit("una pregunta bastante larga para forzar el ajuste de línea en el historial")
	h.event(agent.Started{SessionID: "S1234567890"})
	h.event(agent.TextDelta{Text: strings.Repeat("palabra ", 40)})
	h.event(agent.ToolCall{Name: "Bash", Input: json.RawMessage(`{"command":"` + strings.Repeat("y", 100) + `"}`)})
	for _, size := range [][2]int{{40, 10}, {20, 6}, {5, 2}, {200, 60}} {
		h.update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := h.view()
		limit := max(size[0], minWidth)
		for i, line := range strings.Split(view, "\n") {
			if w := lipgloss.Width(line); w > limit {
				t.Errorf("%dx%d: línea %d ancha %d > %d: %q", size[0], size[1], i, w, limit, line)
			}
		}
		if n := len(strings.Split(view, "\n")); n > size[1] {
			t.Errorf("%dx%d: la vista tiene %d líneas", size[0], size[1], n)
		}
	}
}

func TestPaginarHistorial(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 40, Height: 10})
	h.submit("uno")
	for i := 0; i < 30; i++ {
		h.event(agent.ToolCall{Name: "Tool", Input: json.RawMessage(`{}`)})
	}
	if !h.m.vp.AtBottom() {
		t.Fatal("el historial debería estar al final")
	}
	h.press(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if h.m.vp.AtBottom() {
		t.Error("pgup debería subir")
	}
	h.press(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !h.m.vp.AtBottom() {
		t.Error("pgdown debería bajar")
	}
}

func TestSpinnerSoloMientrasHayTurno(t *testing.T) {
	h := newHarness(t)
	cmd := h.press(keyEnter)
	if cmd != nil {
		t.Fatal("sin texto no hay nada que hacer")
	}
	h.typeText("uno")
	h.press(keyEnter)
	if !h.m.spinning {
		t.Error("al enviar debería arrancar el spinner")
	}
	h.event(agent.Done{})
	if cmd := h.update(spinner.TickMsg{}); cmd != nil || h.m.spinning {
		t.Error("el spinner debe detenerse cuando no hay turno")
	}
}

// Arreglo A: el desplazamiento del usuario sobrevive al streaming.
func TestDesplazamientoDelUsuarioSobreviveAlStreaming(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 40, Height: 10})
	h.submit("uno")
	for i := 0; i < 30; i++ {
		h.event(agent.ToolCall{Name: "Tool", Input: json.RawMessage(`{}`)})
	}
	h.press(tea.KeyPressMsg{Code: tea.KeyPgUp})
	off := h.m.vp.YOffset()
	if h.m.vp.AtBottom() {
		t.Fatal("pgup debería haber salido del final")
	}
	for i := 0; i < 5; i++ {
		h.event(agent.TextDelta{Text: "más texto "})
	}
	if got := h.m.vp.YOffset(); got != off {
		t.Errorf("YOffset = %d tras el streaming, quiero %d", got, off)
	}
	if h.m.vp.AtBottom() {
		t.Error("los TextDelta no deben devolver la vista al final")
	}
	// Volver al final reanuda el seguimiento.
	h.press(tea.KeyPressMsg{Code: tea.KeyPgDown})
	h.press(tea.KeyPressMsg{Code: tea.KeyPgDown})
	h.press(tea.KeyPressMsg{Code: tea.KeyPgDown})
	h.event(agent.TextDelta{Text: "\n\n\n\nfin"})
	if !h.m.vp.AtBottom() {
		t.Error("al estar en el final, la vista debe seguirlo")
	}
	// Enviar un mensaje propio baja al final.
	h.event(agent.Done{})
	h.press(tea.KeyPressMsg{Code: tea.KeyPgUp})
	h.submit("otro")
	if !h.m.vp.AtBottom() {
		t.Error("enviar un mensaje debe bajar al final")
	}
}

// Arreglo B: la caché de render da lo mismo que renderizar desde cero.
func TestCacheDeRenderIgualARenderCompleto(t *testing.T) {
	h := newHarness(t)
	check := func(step string) {
		t.Helper()
		m := h.m
		want := renderEntries(m.entries, m.styles, m.vp.Width())
		if got := m.vp.GetContent(); got != want {
			t.Errorf("%s: el contenido con caché difiere del render completo\ncaché: %q\ncompleto: %q", step, got, want)
		}
		if m.asstOpen && m.cache.done != len(m.entries)-1 {
			t.Errorf("%s: la entrada abierta no debe estar en la caché (done=%d, entradas=%d)", step, m.cache.done, len(m.entries))
		}
	}
	h.submit("una pregunta algo larga para que se ajuste a varias líneas en el historial")
	check("enviar")
	h.event(agent.Started{SessionID: "S1"})
	h.event(agent.TextDelta{Text: "primera "})
	check("delta 1")
	h.event(agent.TextDelta{Text: strings.Repeat("palabra ", 30)})
	check("delta 2")
	h.event(agent.ToolCall{Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)})
	check("tool")
	h.event(agent.TextDelta{Text: "segunda"})
	check("delta 3")
	h.update(tea.WindowSizeMsg{Width: 30, Height: 12})
	check("ancho 30")
	h.event(agent.TextDelta{Text: " y más"})
	check("delta tras redimensionar")
	h.event(agent.Done{})
	h.update(tea.WindowSizeMsg{Width: 100, Height: 12})
	check("ancho 100")
	h.submit("otra")
	h.event(agent.TextDelta{Text: "respuesta"})
	check("segundo turno")
	if !strings.Contains(h.m.vp.GetContent(), "primera") {
		t.Error("el historial cerrado debe seguir presente")
	}
}

// Arreglo C: ctrl+c muestra "saliendo…".
func TestCtrlCMuestraSaliendo(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.wantView("pensando…")
	h.press(keyCtrlC)
	h.wantView("saliendo…")
	h.wantNoView("pensando…")
}

// Arreglo D: ctrl+n descarta la sesión.
var keyCtrlN = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}

func TestNuevaSesionDescartaSoloLaSesion(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.event(agent.Started{SessionID: "S1"})
	h.event(agent.Done{SessionID: "S1"})

	if cmd := h.press(keyCtrlN); cmd != nil {
		t.Error("ctrl+n sin turno en curso no tiene nada que ejecutar")
	}
	if h.m.sessionID != "" {
		t.Errorf("sessionID = %q, quiero vacío", h.m.sessionID)
	}
	h.wantView("sesión nueva")
	h.wantNoView("sesión S1")
	if h.ag.cancelCount() != 0 || h.ag.closeCount() != 0 {
		t.Error("ctrl+n sin turno no debe cancelar ni cerrar al agente")
	}

	h.submit("dos")
	starts := h.ag.startsCopy()
	if len(starts) != 2 || starts[1].SessionID != "" {
		t.Errorf("el turno nuevo no debe reanudar nada y usa el mismo agente: %+v", starts)
	}
}

func TestNuevaSesionConTurnoEnCursoLoCancela(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.event(agent.Started{SessionID: "S1"})
	cmd := h.press(keyCtrlN)
	if cmd == nil {
		t.Fatal("ctrl+n durante un turno debería cancelarlo")
	}
	if h.ag.cancelCount() != 0 {
		t.Fatal("Cancel no debe ejecutarse dentro de Update")
	}
	if h.m.sessionID != "" {
		t.Errorf("sessionID = %q, quiero vacío", h.m.sessionID)
	}
	h.wantView("sesión nueva")
	h.wantView("cancelando…")
	h.pump(cmd)
	if h.ag.cancelCount() != 1 || h.ag.closeCount() != 0 {
		t.Errorf("Cancel=%d Close=%d, quiero 1 y 0", h.ag.cancelCount(), h.ag.closeCount())
	}
	// Durante la cancelación solo limpia la sesión.
	h.submit("dos")
	h.event(agent.Started{SessionID: "S2"})
	h.press(keyEsc)
	if cmd := h.press(keyCtrlN); cmd != nil || h.m.sessionID != "" {
		t.Error("ctrl+n durante la cancelación solo limpia la sesión")
	}
}

func TestNuevaSesionSinTurnoSoloLimpia(t *testing.T) {
	h := newHarness(t)
	if cmd := h.press(keyCtrlN); cmd != nil {
		t.Error("sin turno no hay nada que ejecutar")
	}
	h.wantView("sesión nueva")
}

func TestAyudaSiNoSePuedeReanudar(t *testing.T) {
	h := newHarness(t)
	h.submit("uno")
	h.event(agent.Started{SessionID: "S1"})
	h.event(agent.Done{SessionID: "S1"})
	h.wantNoView("no se pudo reanudar")

	// El adaptador reporta que no pudo reanudar S1.
	h.submit("dos")
	h.event(agent.Error{Err: fmt.Errorf("%w: S1", agent.ErrSessionNotFound)})
	h.event(agent.Done{IsError: true})
	h.wantView("no se pudo reanudar la sesión; ctrl+n empieza una nueva")
	if help := DefaultKeyMap().helpLine(); !strings.Contains(help, "ctrl+n sesión nueva") {
		t.Errorf("la ayuda no menciona ctrl+n: %q", help)
	}
	h.wantView("ctrl+n s") // visible en la línea de estado (se recorta al ancho)

	// Otro error no sugiere nada.
	h.submit("tres")
	h.event(agent.Error{Err: errors.New("otro fallo")})
	h.event(agent.Done{IsError: true})
	if n := strings.Count(h.view(), "ctrl+n empieza una nueva"); n != 1 {
		t.Errorf("la ayuda aparece %d veces, quiero 1 (la del turno anterior)", n)
	}
}

// Arreglo E: la vista nunca supera el alto.
func TestAlturasPequenas(t *testing.T) {
	h := newHarness(t)
	h.submit("una pregunta")
	h.event(agent.TextDelta{Text: strings.Repeat("palabra ", 40)})
	for height := 1; height <= 6; height++ {
		h.update(tea.WindowSizeMsg{Width: 40, Height: height})
		v := h.m.View()
		plain := ansiRe.ReplaceAllString(v.Content, "")
		if n := len(strings.Split(plain, "\n")); n > height {
			t.Errorf("alto %d: la vista tiene %d líneas:\n%s", height, n, plain)
		}
		if v.Cursor != nil && v.Cursor.Position.Y >= height {
			t.Errorf("alto %d: el cursor está en Y=%d", height, v.Cursor.Position.Y)
		}
		if v.Cursor == nil {
			t.Errorf("alto %d: debería haber cursor", height)
		}
	}
}

// Arreglo F: el modelo se muestra en la línea de estado.
func TestModeloEnLineaDeEstado(t *testing.T) {
	h := newHarness(t)
	h.wantNoView("claude-test-1")
	h.submit("uno")
	h.event(agent.Started{SessionID: "S1", Model: "claude-test-1"})
	h.wantView("claude-test-1")
}

// Arreglo G: con poco contenido, el layout llena la pantalla y el cursor
// cae dentro de la entrada.
func TestVistaVaciaLlenaLaPantalla(t *testing.T) {
	h := newHarness(t)
	v := h.m.View()
	plain := ansiRe.ReplaceAllString(v.Content, "")
	lines := strings.Split(plain, "\n")
	if len(lines) != 24 {
		t.Fatalf("la vista tiene %d líneas, quiero 24", len(lines))
	}
	if !strings.Contains(lines[20], "libre") {
		t.Errorf("la línea de estado debería ser la 20: %q", lines[20])
	}
	if !strings.HasPrefix(lines[21], "│") || !strings.Contains(lines[21], "Escribe un mensaje") {
		t.Errorf("la entrada debería empezar en la línea 21: %q", lines[21])
	}
	if v.Cursor == nil {
		t.Fatal("debería haber cursor")
	}
	if y := v.Cursor.Position.Y; y < 21 || y > 23 {
		t.Errorf("el cursor está en Y=%d, quiero dentro de la entrada (21-23)", y)
	}
}
