package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/orch"
)

// pfake es un agent.Agent de pruebas para el panel multiagente.
type pfake struct {
	mu      sync.Mutex
	ch      chan agent.Event
	once    sync.Once
	starts  []agent.Task
	sends   []string
	cancels int
	pauses  int
	resumes int
	closes  int
}

func newPfake() *pfake { return &pfake{ch: make(chan agent.Event, 64)} }

func (f *pfake) Start(_ context.Context, t agent.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, t)
	return nil
}
func (f *pfake) Send(msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, msg)
	return nil
}
func (f *pfake) Events() <-chan agent.Event { return f.ch }
func (f *pfake) Pause() error               { f.bump(&f.pauses); return nil }
func (f *pfake) Resume() error              { f.bump(&f.resumes); return nil }
func (f *pfake) Cancel() error              { f.bump(&f.cancels); return nil }
func (f *pfake) Close() error {
	f.bump(&f.closes)
	f.once.Do(func() { close(f.ch) })
	return nil
}
func (f *pfake) Status() agent.Status { return agent.StatusIdle }

func (f *pfake) bump(n *int) {
	f.mu.Lock()
	*n++
	f.mu.Unlock()
}

func (f *pfake) get(n *int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *n
}

func (f *pfake) emit(ev agent.Event) { f.ch <- ev }

var (
	keyCtrlT   = tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	keyCtrlO   = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	keyCtrlX   = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	keyCtrlK   = tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	keyAltDown = tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModAlt}
	keyAltUp   = tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt}
)

// phar conduce el panel como lo haría Bubble Tea, con un supervisor real y
// agentes falsos. Las novedades del supervisor las entrega el propio
// harness (pull), nunca un Cmd de escucha.
type phar struct {
	t    *testing.T
	m    Panel
	sup  *orch.Supervisor
	made []*pfake
}

func newPharness(t *testing.T, maxActive int, b orch.Budget) *phar {
	h := &phar{t: t, sup: orch.New(maxActive)}
	h.m = NewPanel(Config{
		Supervisor: h.sup,
		NewAgent: func() agent.Agent {
			f := newPfake()
			h.made = append(h.made, f)
			return f
		},
		Task:   agent.Task{Dir: "/proyecto"},
		Budget: b,
	})
	h.m.spinning = true // sin Cmd de spinner: los Cmd que vuelven son operaciones
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	t.Cleanup(h.sup.Close)
	return h
}

func (h *phar) update(msg tea.Msg) tea.Cmd {
	h.t.Helper()
	m, cmd := h.m.Handle(msg)
	h.m = m
	return cmd
}

// press entrega una tecla y ejecuta el Cmd que devuelva (una operación del
// supervisor), entregando su resultado.
func (h *phar) press(k tea.KeyPressMsg) {
	h.t.Helper()
	if cmd := h.update(k); cmd != nil {
		if msg := cmd(); msg != nil {
			h.update(msg)
		}
	}
}

func (h *phar) typeText(s string) {
	for _, r := range s {
		h.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (h *phar) say(s string) {
	h.t.Helper()
	h.typeText(s)
	h.press(keyEnter)
}

// pull entrega al panel las novedades que ya tenga el supervisor.
func (h *phar) pull() {
	var ups []orch.Update
	for {
		select {
		case u := <-h.sup.Updates():
			ups = append(ups, u)
		default:
			if len(ups) > 0 {
				h.update(updatesMsg{ups: ups})
			}
			return
		}
	}
}

// until entrega novedades hasta que cond se cumpla.
func (h *phar) until(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.pull()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no se cumplió: %s\n%s", what, h.view())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *phar) stateOf(i int) string { return h.m.views[i].stateLabel() }

func (h *phar) untilState(i int, want string) {
	h.t.Helper()
	h.until(fmt.Sprintf("agente %d en %s", i+1, want), func() bool { return h.stateOf(i) == want })
}

func (h *phar) view() string { return ansiRe.ReplaceAllString(h.m.View().Content, "") }

func (h *phar) wantView(sub string) {
	h.t.Helper()
	if v := h.view(); !strings.Contains(v, sub) {
		h.t.Errorf("la vista no contiene %q:\n%s", sub, v)
	}
}

func (h *phar) wantNoView(sub string) {
	h.t.Helper()
	if v := h.view(); strings.Contains(v, sub) {
		h.t.Errorf("la vista contiene %q:\n%s", sub, v)
	}
}

func (h *phar) counts() (int, int, int) { return h.m.Counts() }

func (h *phar) wantCounts(active, queued, limit int) {
	h.t.Helper()
	a, q, l := h.counts()
	if a != active || q != queued || l != limit {
		h.t.Fatalf("activos %d/%d, en cola %d; se esperaba %d/%d, %d", a, l, q, active, limit, queued)
	}
}

// tres crea tres agentes con un mensaje cada uno, con límite 2 (el tercero
// queda en cola), y deja seleccionado el tercero.
func (h *phar) tres() {
	h.t.Helper()
	h.say("uno")
	h.press(keyCtrlT)
	h.say("dos")
	h.press(keyCtrlT)
	h.say("tres")
	h.untilState(0, "running")
	h.untilState(1, "running")
	h.untilState(2, "queued")
}

func TestTresAgentesConLimiteDosElTerceroQuedaEnCola(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.tres()
	h.wantCounts(2, 1, 2)
	if len(h.made) != 3 {
		t.Fatalf("agentes creados = %d", len(h.made))
	}
	if n := len(h.made[2].starts); n != 0 {
		t.Fatalf("el tercero no debe arrancar, starts = %d", n)
	}
	for _, want := range []string{"agente 1", "agente 2", "agente 3 ", "queued", "running"} {
		h.wantView(want)
	}
	h.wantView("en cola · ") // línea de estado del tercero
	if got := h.made[0].starts[0]; got.Prompt != "uno" || got.Dir != "/proyecto" {
		t.Fatalf("task del primero = %+v", got)
	}
}

func TestPausarConservaElCupoYReanudar(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.tres()
	h.press(keyAltUp)
	h.press(keyAltUp) // agente 1
	h.press(keyCtrlO)
	h.untilState(0, "paused")
	h.wantCounts(2, 1, 2) // pausado sigue activo; el tercero sigue en cola
	h.untilState(2, "queued")
	if got := h.made[0].get(&h.made[0].pauses); got != 1 {
		t.Fatalf("pauses = %d", got)
	}
	h.wantView("— pausado —")

	h.press(keyCtrlO)
	h.untilState(0, "running")
	if got := h.made[0].get(&h.made[0].resumes); got != 1 {
		t.Fatalf("resumes = %d", got)
	}
	h.wantView("— reanudado —")
}

func TestDetenerLiberaCupoYArrancaAlDeLaCola(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.tres()
	h.press(keyAltUp) // agente 2
	h.press(keyCtrlX)
	h.untilState(1, "stopped")
	h.untilState(2, "running")
	h.wantCounts(2, 0, 2) // el 1 y el 3
	if got := h.made[1].get(&h.made[1].cancels); got != 1 {
		t.Fatalf("cancels = %d", got)
	}
	if n := len(h.made[2].starts); n != 1 {
		t.Fatalf("starts del tercero = %d", n)
	}
	h.wantView("— detenido —")

	// Esc también cancela el turno del seleccionado, y un agente libre o
	// detenido no se vuelve a cancelar.
	h.press(keyEsc)
	h.press(keyCtrlX)
	if got := h.made[1].get(&h.made[1].cancels); got != 1 {
		t.Fatalf("cancels tras Esc sobre uno detenido = %d", got)
	}
	h.press(keyAltDown) // agente 3, en marcha
	h.press(keyEsc)
	h.untilState(2, "stopped")
	if got := h.made[2].get(&h.made[2].cancels); got != 1 {
		t.Fatalf("cancels del tercero = %d", got)
	}
}

func TestParadaGlobal(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.tres()
	h.press(keyCtrlK)
	for i := 0; i < 3; i++ {
		h.untilState(i, "stopped")
	}
	h.wantCounts(0, 0, 2)
	for i, want := range []int{1, 1, 0} {
		if got := h.made[i].get(&h.made[i].cancels); got != want {
			t.Fatalf("cancels[%d] = %d, quería %d", i, got, want)
		}
	}
	if n := len(h.made[2].starts); n != 0 {
		t.Fatal("el de la cola no debe arrancar tras la parada global")
	}
	// Tras parar, se puede seguir conversando con un agente (Send).
	h.press(keyAltUp)
	h.say("sigue")
	h.untilState(1, "running")
	if len(h.made[1].sends) != 1 || h.made[1].sends[0] != "sigue" {
		t.Fatalf("sends = %v", h.made[1].sends)
	}
}

func TestEventosSeEnrutanAlChatCorrecto(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.say("uno")
	h.press(keyCtrlT)
	h.say("dos")
	h.untilState(0, "running")
	h.untilState(1, "running")
	h.made[0].emit(agent.TextDelta{Text: "respuesta-del-A"})
	h.made[1].emit(agent.TextDelta{Text: "respuesta-del-B"})
	h.made[1].emit(agent.Usage{InputTokens: 7, OutputTokens: 5, CostUSD: 0.25})
	h.until("eventos", func() bool { return h.m.views[1].costUSD > 0 && len(h.m.views[0].entries) > 1 })

	// Seleccionado el B (el último creado).
	h.wantView("respuesta-del-B")
	h.wantNoView("respuesta-del-A")
	h.wantView("↑7 ↓5")
	h.press(keyAltUp)
	h.wantView("respuesta-del-A")
	h.wantNoView("respuesta-del-B")
	h.wantView("↑0 ↓0")
	// La lista muestra los tokens de cada uno.
	h.wantView("12 tok")

	// Un evento de un agente no seleccionado no toca el chat visible.
	h.made[1].emit(agent.TextDelta{Text: " más-B"})
	h.until("evento", func() bool {
		return strings.HasSuffix(h.m.views[1].entries[len(h.m.views[1].entries)-1].text, "más-B")
	})
	h.wantNoView("más-B")
}

func TestBorradorYDesplazamientoPorAgente(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.typeText("borrador del uno")
	h.press(keyCtrlT)
	h.wantNoView("borrador del uno")
	h.typeText("otro")
	h.press(keyAltUp)
	h.wantView("borrador del uno")
	h.press(keyAltDown)
	h.wantView("otro")
	h.press(keyAltDown) // circular
	h.wantView("borrador del uno")
}

func TestPresupuestoSuperadoVisibleEnLaLista(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{MaxTokens: 100})
	h.say("hola")
	h.untilState(0, "running")
	h.wantView("0/100 tok")
	h.made[0].emit(agent.Usage{InputTokens: 80, OutputTokens: 80})
	h.untilState(0, "budget-exceeded")
	h.wantView("160/100 tok")
	h.wantView("presupuesto de tokens superado: 160/100")
	if got := h.made[0].get(&h.made[0].cancels); got != 1 {
		// Cancel va en otra goroutine del supervisor.
		h.until("Cancel", func() bool { return h.made[0].get(&h.made[0].cancels) == 1 })
	}
	h.wantCounts(0, 0, 2)

	// No acepta más turnos y el texto no se pierde.
	h.say("más")
	h.wantView("no se pudo enviar")
	if v := h.m.input.Value(); v != "más" {
		t.Fatalf("la entrada = %q, debía conservar el texto", v)
	}
	if len(h.made[0].sends) != 0 {
		t.Fatal("no debe enviar más turnos")
	}
}

func TestSegundoMensajeUsaSendYReanudaLaSesion(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{MaxTurns: 3})
	h.say("uno")
	h.untilState(0, "running")
	h.wantView("t1/3")
	// Mientras corre, Enter no envía.
	h.typeText("adelantado")
	h.press(keyEnter)
	if len(h.made[0].sends) != 0 || h.m.input.Value() != "adelantado" {
		t.Fatal("no debe enviar con un turno en curso")
	}
	h.made[0].emit(agent.Started{SessionID: "S-123"})
	h.made[0].emit(agent.Done{SessionID: "S-123"})
	h.untilState(0, "idle")
	h.press(keyEnter)
	h.untilState(0, "running")
	if len(h.made[0].starts) != 1 || len(h.made[0].sends) != 1 || h.made[0].sends[0] != "adelantado" {
		t.Fatalf("starts=%d sends=%v", len(h.made[0].starts), h.made[0].sends)
	}
	h.wantView("t2/3")
}

func TestSinSupervisorNoEnviaNada(t *testing.T) {
	m := NewPanel(Config{})
	m, _ = m.Handle(tea.WindowSizeMsg{Width: 60, Height: 20})
	for _, r := range "hola" {
		m, _ = m.Handle(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m, cmd := m.Handle(keyEnter)
	if cmd != nil || m.Init() != nil {
		t.Fatal("sin supervisor no hay Cmd")
	}
	if a, q, l := m.Counts(); a != 0 || q != 0 || l != 2 {
		t.Fatalf("Counts = %d %d %d", a, q, l)
	}
	if m.View().Cursor == nil {
		t.Fatal("debería haber cursor")
	}
}

func TestSalirCierraSupervisorYAgentesSinEnviar(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{})
	h.say("uno")
	h.press(keyCtrlT) // agente 2: sin enviar
	h.untilState(0, "running")
	cmd := h.update(keyCtrlC)
	if cmd == nil {
		t.Fatal("ctrl+c debe devolver un Cmd que cierra")
	}
	msg := cmd()
	if _, ok := msg.(quitMsg); !ok {
		t.Fatalf("mensaje = %T", msg)
	}
	for i, f := range h.made {
		if f.get(&f.closes) != 1 {
			t.Fatalf("closes[%d] = %d", i, f.get(&f.closes))
		}
	}
	select {
	case _, ok := <-drain(h.sup.Updates()):
		if ok {
			t.Fatal("Updates debía cerrarse")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Updates no se cerró")
	}
	if h.update(keyEnter) != nil {
		t.Fatal("tras salir no se atienden teclas")
	}
}

// drain lee ch hasta que se cierre y entonces entrega un valor cerrado.
func drain(ch <-chan orch.Update) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	return done
}

func TestListenUpdatesAgrupaYAvisaDelCierre(t *testing.T) {
	ch := make(chan orch.Update, 8)
	for i := 1; i <= 3; i++ {
		ch <- orch.Update{ID: orch.ID(i)}
	}
	msg := listenUpdates(ch)().(updatesMsg)
	if len(msg.ups) != 3 || msg.closed {
		t.Fatalf("msg = %+v", msg)
	}
	close(ch)
	if msg := listenUpdates(ch)().(updatesMsg); !msg.closed || len(msg.ups) != 0 {
		t.Fatalf("msg tras cerrar = %+v", msg)
	}
}

func TestListaDeAgentesMuestraTodoYRecorta(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{MaxTurns: 5})
	for i := 0; i < 9; i++ {
		h.press(keyCtrlT)
	}
	if len(h.m.views) != 10 || h.m.sel != 9 {
		t.Fatalf("views=%d sel=%d", len(h.m.views), h.m.sel)
	}
	if h.m.listH != maxListRows {
		t.Fatalf("listH = %d", h.m.listH)
	}
	// El seleccionado siempre está visible en la lista.
	for i := 0; i < 10; i++ {
		h.press(keyAltDown)
		h.wantView("▸ " + h.m.views[h.m.sel].name + " ")
	}
}

func TestRedimensionarConVariosAgentesNoDesborda(t *testing.T) {
	h := newPharness(t, 2, orch.Budget{MaxTurns: 5, MaxTokens: 100000})
	h.tres()
	h.press(keyCtrlT)
	h.press(keyCtrlT)
	h.m.views[0].applyEvent(agent.TextDelta{Text: strings.Repeat("palabra ", 60)})
	h.m.views[0].applyEvent(agent.Started{SessionID: "S1234567890", Model: "modelo-con-nombre-largo"})
	for w := 1; w <= 120; w += 7 {
		for height := 1; height <= 40; height++ {
			h.update(tea.WindowSizeMsg{Width: w, Height: height})
			v := h.m.View()
			plain := ansiRe.ReplaceAllString(v.Content, "")
			limit := max(w, minWidth)
			lines := strings.Split(plain, "\n")
			for i, l := range lines {
				if lw := lipgloss.Width(l); lw > limit {
					t.Fatalf("%dx%d: línea %d ancha %d > %d: %q", w, height, i, lw, limit, l)
				}
			}
			if len(lines) > max(height, 1) {
				t.Fatalf("%dx%d: %d líneas", w, height, len(lines))
			}
			if v.Cursor == nil || v.Cursor.Position.Y >= max(height, 1) {
				t.Fatalf("%dx%d: cursor %+v", w, height, v.Cursor)
			}
		}
	}
	// Con espacio, la vista llena la altura y la lista va arriba.
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	lines := strings.Split(h.view(), "\n")
	if len(lines) != 30 {
		t.Fatalf("líneas = %d", len(lines))
	}
	if !strings.Contains(lines[0], "agente 1") || !strings.Contains(lines[4], "agente 5") {
		t.Fatalf("la lista debería ocupar las primeras filas:\n%s", strings.Join(lines[:6], "\n"))
	}
}

func TestAtajosDelPanelSalenDeLaConfiguracion(t *testing.T) {
	km := config.Default()
	km["agent_new"] = []string{"f2"}
	km["new_session"] = []string{"f3"}
	sup := orch.New(2)
	defer sup.Close()
	m := NewPanel(Config{Supervisor: sup, Keymap: km, NewAgent: func() agent.Agent { return newPfake() }})
	m.spinning = true
	m, _ = m.Handle(keyCtrlT) // ya no es agent_new
	if len(m.views) != 1 {
		t.Fatalf("views = %d tras ctrl+t", len(m.views))
	}
	m, _ = m.Handle(tea.KeyPressMsg{Code: tea.KeyF2})
	if len(m.views) != 2 {
		t.Fatalf("views = %d tras f2", len(m.views))
	}
}

func TestPresupuestoEstilosYMicroBar(t *testing.T) {
	// 1. microBar
	if got := microBar(0); got != "[░░░░░]" {
		t.Errorf("microBar(0) = %q; want [░░░░░]", got)
	}
	if got := microBar(0.6); got != "[■■■░░]" {
		t.Errorf("microBar(0.6) = %q; want [■■■░░]", got)
	}
	if got := microBar(0.8); got != "[■■■■░]" {
		t.Errorf("microBar(0.8) = %q; want [■■■■░]", got)
	}
	if got := microBar(1.0); got != "[■■■■■]" {
		t.Errorf("microBar(1.0) = %q; want [■■■■■]", got)
	}
	if got := microBar(1.5); got != "[■■■■■]" {
		t.Errorf("microBar(1.5) = %q; want [■■■■■]", got)
	}

	// 2. budgetStyle
	base := lipgloss.NewStyle()
	normal := budgetStyle(base, 0.70)
	if normal.GetForeground() != base.GetForeground() {
		t.Errorf("<=75%% no debe alterar color: %v", normal)
	}
	amber := budgetStyle(base, 0.76)
	if amber.GetForeground() != lipgloss.Color("214") {
		t.Errorf(">75%% debe ser ámbar (214): %v", amber.GetForeground())
	}
	redBold := budgetStyle(base, 0.91)
	if redBold.GetForeground() != lipgloss.Color("9") || !redBold.GetBold() {
		t.Errorf(">90%% debe ser rojo negrita (9 bold): fg=%v bold=%v", redBold.GetForeground(), redBold.GetBold())
	}

	// 3. formatDuration
	if got := formatDuration(45 * time.Second); got != "45s" {
		t.Errorf("formatDuration(45s) = %q", got)
	}
	if got := formatDuration(60 * time.Second); got != "1m" {
		t.Errorf("formatDuration(60s) = %q", got)
	}
	if got := formatDuration(90 * time.Second); got != "1m30s" {
		t.Errorf("formatDuration(90s) = %q", got)
	}
}

func TestPresupuestoEnListaDeAgentes(t *testing.T) {
	sup := orch.New(2)
	defer sup.Close()
	m := NewPanel(Config{
		Supervisor: sup,
		Budget: orch.Budget{
			MaxTurns:    10,
			MaxTokens:   1000,
			MaxDuration: 100 * time.Second,
		},
	})
	m.addView() // 2 agentes: m.views[0] (sel), m.views[1]
	m.views[0].hasSnap = true
	m.views[0].snap = orch.Snapshot{
		State:   orch.Running,
		Turns:   8, // 80% -> ámbar
		Tokens:  500,
		Elapsed: 92 * time.Second, // 92% -> rojo negrita
		CostUSD: 0.05,
	}
	m.views[1].hasSnap = true
	m.views[1].snap = orch.Snapshot{
		State:   orch.Running,
		Turns:   3,
		Tokens:  950, // 95% -> rojo negrita
		Elapsed: 10 * time.Second,
		CostUSD: 0.02,
	}

	line0 := m.listLine(0) // seleccionado: microBar del recurso más cercano (tiempo: 92%)
	line1 := m.listLine(1) // no seleccionado: sin microBar

	// Agente 0 debe incluir la micro barra [■■■■■] y timeStr 92s/1m40s (o 92s/100s)
	if !strings.Contains(line0, "[■■■■■]") {
		t.Errorf("line0 debe tener micro barra [■■■■■]: %q", line0)
	}
	if !strings.Contains(line0, "t8/10") {
		t.Errorf("line0 debe incluir t8/10: %q", line0)
	}
	if !strings.Contains(line0, "500/1000 tok") {
		t.Errorf("line0 debe incluir 500/1000 tok: %q", line0)
	}

	// Agente 1 no está seleccionado: no debe contener microBar
	if strings.Contains(line1, "[") && strings.Contains(line1, "░") {
		t.Errorf("line1 no seleccionado no debe tener microBar: %q", line1)
	}
	if !strings.Contains(line1, "950/1000 tok") {
		t.Errorf("line1 debe incluir 950/1000 tok: %q", line1)
	}

	// Comprobación de secuencias ANSI de color
	// line0 tiene turnos al 80% (ámbar 214) y tiempo al 92% (rojo 9)
	if !strings.Contains(line0, "214") {
		t.Errorf("line0 debe contener código ANSI de ámbar 214: %q", line0)
	}
	if !strings.Contains(line0, "9") {
		t.Errorf("line0 debe contener código ANSI de rojo 9: %q", line0)
	}
	// line1 tiene tokens al 95% (rojo 9)
	if !strings.Contains(line1, "9") {
		t.Errorf("line1 debe contener código ANSI de rojo 9: %q", line1)
	}
}

func TestPanelAccionesExportadas(t *testing.T) {
	sup := orch.New(2)
	defer sup.Close()
	m := NewPanel(Config{
		Supervisor: sup,
		NewAgent:   func() agent.Agent { return newPfake() },
	})
	if !m.Writing() {
		t.Error("Writing() debe ser true al tener foco la entrada")
	}
	m.Blur()
	if m.Writing() {
		t.Error("Writing() debe ser false cuando está blurred")
	}

	// Métodos de acción
	m2, _ := m.Pause()
	if m2.cur() == nil {
		t.Error("Pause retornó modelo inválido")
	}
	m3, _ := m.Stop()
	if m3.cur() == nil {
		t.Error("Stop retornó modelo inválido")
	}
	m4, _ := m.Diff()
	if m4.cur() == nil {
		t.Error("Diff retornó modelo inválido")
	}
	m5, _ := m.RevertFile()
	if m5.cur() == nil {
		t.Error("RevertFile retornó modelo inválido")
	}
	m6, _ := m.RevertTurn()
	if m6.cur() == nil {
		t.Error("RevertTurn retornó modelo inválido")
	}
	m7, _ := m.RevertAgent()
	if m7.cur() == nil {
		t.Error("RevertAgent retornó modelo inválido")
	}
}

func TestStateBadge(t *testing.T) {
	tests := []struct {
		state   orch.State
		hasSnap bool
		want    string
	}{
		{orch.Running, true, "RUN"},
		{orch.Paused, true, "PAU"},
		{orch.Queued, true, "Q"},
		{orch.Idle, true, "IDLE"},
		{orch.Stopped, true, "STOP"},
		{orch.Failed, true, "ERR"},
		{orch.BudgetExceeded, true, "$!"},
		{orch.State(99), false, "NEW"},
	}
	for _, tc := range tests {
		got, _ := stateBadge(tc.state, tc.hasSnap)
		if got != tc.want {
			t.Errorf("stateBadge(%v, %v) = %q, want %q", tc.state, tc.hasSnap, got, tc.want)
		}
	}
}

func TestResponsiveAgentListBadgesAndNoOverflow(t *testing.T) {
	sup := orch.New(5)
	defer sup.Close()
	p := NewPanel(Config{
		Supervisor: sup,
		NewAgent:   func() agent.Agent { return newPfake() },
		Budget: orch.Budget{
			MaxTurns:    10,
			MaxTokens:   1000,
			MaxDuration: time.Minute,
		},
	})
	p.addView()
	p.addView()
	p.views[0].id = 1
	p.views[0].hasSnap = true
	p.views[0].snap = orch.Snapshot{ID: 1, State: orch.Running, Turns: 3, Tokens: 300, CostUSD: 0.05}
	p.views[1].id = 2
	p.views[1].hasSnap = true
	p.views[1].snap = orch.Snapshot{ID: 2, State: orch.Paused, Turns: 5, Tokens: 500, CostUSD: 0.10}

	// 1. En ancho estrecho (< 50), debe renderizar badges compactos ("RUN", "PAU")
	pNarrow := p
	pNarrow.width = 35
	pNarrow.listH = 5
	linesNarrow := pNarrow.listRows()
	if len(linesNarrow) < 2 {
		t.Fatalf("se esperaban al menos 2 filas, se obtuvieron %d", len(linesNarrow))
	}
	if !strings.Contains(linesNarrow[0], "RUN") {
		t.Errorf("fila 0 en ancho estrecho debe contener el badge 'RUN': %q", linesNarrow[0])
	}
	if !strings.Contains(linesNarrow[1], "PAU") {
		t.Errorf("fila 1 en ancho estrecho debe contener el badge 'PAU': %q", linesNarrow[1])
	}

	// 2. En ancho amplio (>= 50), debe renderizar el texto completo del estado ("running", "paused")
	pWide := p
	pWide.width = 80
	pWide.listH = 5
	linesWide := pWide.listRows()
	if !strings.Contains(linesWide[0], "running") {
		t.Errorf("fila 0 en ancho amplio debe contener 'running': %q", linesWide[0])
	}
	if !strings.Contains(linesWide[1], "paused") {
		t.Errorf("fila 1 en ancho amplio debe contener 'paused': %q", linesWide[1])
	}

	// 3. Ningún ancho (de 5 a 120) debe desbordar su ancho
	for w := 5; w <= 120; w++ {
		pTest := p
		pTest.width = w
		pTest.listH = 5
		for i, line := range pTest.listRows() {
			if gotW := lipgloss.Width(line); gotW > w {
				t.Fatalf("desborde en w=%d fila %d: ancho obtenido %d > %d: %q", w, i, gotW, w, line)
			}
		}
	}
}

func TestRevertModalInspectionAndKeyNavigation(t *testing.T) {
	sup := orch.New(2)
	defer sup.Close()
	p := NewPanel(Config{
		Supervisor: sup,
		NewAgent:   func() agent.Agent { return newPfake() },
	})
	p.addView()
	v := p.views[0]
	v.id = 1
	v.hasSnap = true
	v.snap = orch.Snapshot{ID: 1, State: orch.Paused}

	// Sin ediciones: RevertFile no activa modal
	p, _ = p.RevertFile()
	if p.RevertModalActive() {
		t.Fatal("no debe haber modal activo sin ediciones")
	}

	// Con modal simulado
	p.rm = &revertModal{
		kind:    revertFile,
		turn:    3,
		path:    "main.go",
		files:   []string{"main.go"},
		agentID: 1,
		name:    "agente 1",
	}

	if !p.RevertModalActive() {
		t.Fatal("RevertModalActive() debe ser true")
	}
	if p.RevertModalKind() != revertFile {
		t.Errorf("RevertModalKind() = %q, want %q", p.RevertModalKind(), revertFile)
	}
	if files := p.RevertModalFiles(); len(files) != 1 || files[0] != "main.go" {
		t.Errorf("RevertModalFiles() = %v", files)
	}

	// Esc cancela el modal
	p, _ = p.Handle(tea.KeyPressMsg{Code: tea.KeyEsc})
	if p.RevertModalActive() {
		t.Fatal("Esc debía cancelar el modal de reversión")
	}
}
