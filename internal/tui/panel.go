package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
)

const (
	// maxListRows es el máximo de filas de la lista de agentes.
	maxListRows = 6
	// minChatRows son las filas que se reservan para el chat (historial,
	// estado y entrada) antes de dar espacio a la lista.
	minChatRows = 5
	// defaultMaxActive es el límite que se muestra sin supervisor.
	defaultMaxActive = 2
)

// Mensajes internos del panel.
type (
	// updatesMsg lleva un lote de novedades del supervisor. closed indica
	// que el canal se cerró tras ellas.
	updatesMsg struct {
		ups    []orch.Update
		closed bool
	}
	// opMsg es el resultado de una operación del supervisor hecha en un Cmd.
	opMsg struct {
		id   orch.ID // 0: global
		what string
		err  error
	}
)

// agentView es un agente del panel: su historial, su último estado conocido y
// el borrador y el desplazamiento de su chat.
type agentView struct {
	transcript
	name string
	// ag es el agente hasta el primer envío; después lo posee el supervisor.
	ag agent.Agent
	// id es 0 hasta el primer envío (Submit).
	id orch.ID
	// snap es el último estado recibido; hasSnap indica que ya llegó alguno.
	snap    orch.Snapshot
	hasSnap bool

	draft  string
	yoff   int
	follow bool
}

func (v *agentView) state() (orch.State, bool) { return v.snap.State, v.hasSnap }

// active indica que el agente ocupa un cupo.
func (v *agentView) active() bool {
	return v.hasSnap && (v.snap.State == orch.Running || v.snap.State == orch.Paused)
}

// inFlight indica que el agente tiene un turno en curso o esperando cupo.
func (v *agentView) inFlight() bool {
	if v.id != 0 && !v.hasSnap { // enviado; aún no llegó su primera novedad
		return true
	}
	return v.hasSnap && (v.snap.State == orch.Running || v.snap.State == orch.Paused || v.snap.State == orch.Queued)
}

// Panel es el panel multiagente: una lista de agentes supervisados por un
// orch.Supervisor y, debajo, el chat del seleccionado. Implementa el mismo
// contrato de embebido que Model (Handle, Body, Cursor, SetSize, Focus...).
// Tiene receptor de valor, pero las vistas de los agentes se comparten por
// puntero: el modelo solo se toca desde el bucle de Bubble Tea.
type Panel struct {
	cfg    Config
	keys   KeyMap
	styles styles
	sup    *orch.Supervisor

	vp    viewport.Model
	input textarea.Model
	spin  spinner.Model

	// width y height son el tamaño del panel; las demás, las filas de la
	// lista, el historial, la línea de estado y la entrada.
	width, height                 int
	listH, histH, statH, inH      int
	views                         []*agentView
	byID                          map[orch.ID]*agentView
	sel                           int
	created                       int
	listening, spinning, quitting bool
	blurred                       bool
}

var _ tea.Model = Panel{}

// NewPanel crea el panel con un primer agente sin iniciar. Con Config.Supervisor
// nil el panel se muestra pero no envía nada.
func NewPanel(cfg Config) Panel {
	km := NewKeyMap(cfg.Keymap)
	m := Panel{
		cfg:       cfg,
		keys:      km,
		styles:    defaultStyles(),
		sup:       cfg.Supervisor,
		vp:        newViewport(),
		input:     newInput(km),
		spin:      newSpinner(),
		byID:      make(map[orch.ID]*agentView),
		listening: cfg.Supervisor != nil,
	}
	m.addView()
	m.layout(80, 24)
	return m
}

// Init implementa tea.Model: empieza a escuchar las novedades del supervisor.
func (m Panel) Init() tea.Cmd {
	if m.sup == nil {
		return nil
	}
	return listenUpdates(m.sup.Updates())
}

// Update implementa tea.Model.
func (m Panel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m.update(msg) }

// Handle es Update con el tipo concreto, para embeberlo.
func (m Panel) Handle(msg tea.Msg) (Panel, tea.Cmd) { return m.update(msg) }

func (m Panel) update(msg tea.Msg) (Panel, tea.Cmd) {
	m, cmd := m.dispatch(msg)
	if m.working() && !m.spinning {
		m.spinning = true
		cmd = tea.Batch(cmd, m.spin.Tick)
	}
	return m, cmd
}

func (m Panel) dispatch(msg tea.Msg) (Panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.layout(msg.Width, msg.Height)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		if !m.working() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case updatesMsg:
		m.applyUpdates(msg.ups)
		if msg.closed || m.sup == nil {
			m.listening = false
			return m, nil
		}
		return m, listenUpdates(m.sup.Updates())

	case opMsg:
		if msg.err != nil {
			text := fmt.Sprintf("no se pudo %s: %v", msg.what, msg.err)
			if v := m.byID[msg.id]; v != nil {
				v.add(kindError, text)
			} else {
				m.cur().add(kindError, text)
			}
			m.refresh()
		}
		return m, nil

	case quitMsg:
		return m, tea.Quit
	}

	// Pegado, parpadeo del cursor y demás: los atiende la entrada.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// cur es el agente seleccionado. Siempre hay al menos uno.
func (m Panel) cur() *agentView { return m.views[m.sel] }

// handleKey procesa las teclas con los bindings de KeyMap.
func (m Panel) handleKey(msg tea.KeyPressMsg) (Panel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.quit()
	case m.quitting:
		return m, nil
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.AgentStop):
		return m, m.stopCmd(m.cur())
	case key.Matches(msg, m.keys.AgentNew), key.Matches(msg, m.keys.NewSession):
		return m.newAgent()
	case key.Matches(msg, m.keys.AgentNext):
		m.selectView(m.sel + 1)
		return m, nil
	case key.Matches(msg, m.keys.AgentPrev):
		m.selectView(m.sel - 1)
		return m, nil
	case key.Matches(msg, m.keys.AgentPause):
		return m, m.pauseCmd(m.cur())
	case key.Matches(msg, m.keys.StopAll):
		return m.StopAll()
	case key.Matches(msg, m.keys.Send):
		return m.send()
	case key.Matches(msg, m.keys.PageUp):
		m.vp.PageUp()
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.vp.PageDown()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// addView agrega un agente sin iniciar (con uno nuevo de la fábrica, si hay) y
// devuelve su vista.
func (m *Panel) addView() *agentView {
	m.created++
	v := &agentView{
		name:       fmt.Sprintf("agente %d", m.created),
		follow:     true,
		transcript: transcript{newSessionKey: m.keys.AgentNew.Help().Key},
	}
	if m.cfg.NewAgent != nil {
		v.ag = m.cfg.NewAgent()
	}
	m.views = append(m.views, v)
	return v
}

// newAgent crea un agente nuevo y lo selecciona. Empieza a correr con el
// primer mensaje que se le envíe.
func (m Panel) newAgent() (Panel, tea.Cmd) {
	m.addView()
	m.layout(m.width, m.height)
	m.selectView(len(m.views) - 1)
	return m, nil
}

// selectView selecciona el agente i (circular), guardando el borrador y el
// desplazamiento del actual y restaurando los del nuevo.
func (m *Panel) selectView(i int) {
	n := len(m.views)
	i = ((i % n) + n) % n
	if i == m.sel {
		return
	}
	cur := m.cur()
	cur.draft = m.input.Value()
	cur.follow = m.vp.AtBottom()
	cur.yoff = m.vp.YOffset()
	m.sel = i
	next := m.cur()
	m.input.Reset()
	m.input.SetValue(next.draft)
	m.refreshTo(next.follow)
	if !next.follow {
		m.vp.SetYOffset(next.yoff)
	}
}

// send envía el texto de la entrada al agente seleccionado: Submit la primera
// vez y Send después (que reanuda su sesión). Submit y Send solo lanzan el
// proceso, así que se llaman en línea; así el ID del agente se conoce antes de
// que llegue cualquiera de sus novedades.
func (m Panel) send() (Panel, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	v := m.cur()
	if text == "" || m.sup == nil || v.inFlight() {
		return m, nil
	}
	if v.id == 0 {
		if v.ag == nil {
			return m, nil
		}
		task := m.cfg.Task
		task.Prompt = text
		id := m.sup.Submit(v.name, v.ag, task, m.cfg.Budget)
		if id == 0 {
			v.add(kindError, "no se pudo enviar: "+orch.ErrClosed.Error())
			m.refresh()
			return m, nil
		}
		v.id, v.ag = id, nil
		m.byID[id] = v
	} else if err := m.sup.Send(v.id, text); err != nil {
		// El texto se conserva en la entrada para no perderlo.
		v.add(kindError, "no se pudo enviar: "+err.Error())
		m.refresh()
		return m, nil
	}
	v.add(kindUser, text)
	m.input.Reset()
	m.refreshTo(true)
	return m, nil
}

// stopCmd detiene el turno del agente (Esc y agent_stop) sin bloquear Update.
// Un agente libre o sin iniciar no tiene nada que detener.
func (m Panel) stopCmd(v *agentView) tea.Cmd {
	if m.sup == nil || v.id == 0 || !v.inFlight() {
		return nil
	}
	sup, id := m.sup, v.id
	return func() tea.Msg { return opMsg{id: id, what: "detener", err: sup.Stop(id)} }
}

// pauseCmd pausa el agente si corre y lo reanuda si está pausado.
func (m Panel) pauseCmd(v *agentView) tea.Cmd {
	if m.sup == nil || v.id == 0 || !v.hasSnap {
		return nil
	}
	sup, id := m.sup, v.id
	switch v.snap.State {
	case orch.Running:
		return func() tea.Msg { return opMsg{id: id, what: "pausar", err: sup.Pause(id)} }
	case orch.Paused:
		return func() tea.Msg { return opMsg{id: id, what: "reanudar", err: sup.Resume(id)} }
	}
	return nil
}

// StopAll es la parada global: detiene a todos los agentes sin bloquear
// Update.
func (m Panel) StopAll() (Panel, tea.Cmd) {
	if m.sup == nil {
		return m, nil
	}
	sup := m.sup
	return m, func() tea.Msg {
		sup.StopAll()
		return opMsg{what: "detener todos"}
	}
}

// NewSession crea un agente nuevo: en el panel multiagente una sesión nueva es
// un agente nuevo.
func (m Panel) NewSession() (Panel, tea.Cmd) { return m.newAgent() }

// Quit cierra a todos los agentes y sale. Si el programa muere antes, de todos
// modos lo cierra el defer de main.
func (m Panel) Quit() (Panel, tea.Cmd) { return m.quit() }

func (m Panel) quit() (Panel, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	m.quitting = true
	var unsent []agent.Agent
	for _, v := range m.views {
		if v.ag != nil {
			unsent = append(unsent, v.ag)
		}
	}
	if m.sup == nil && len(unsent) == 0 {
		return m, tea.Quit
	}
	sup := m.sup
	return m, func() tea.Msg {
		for _, a := range unsent {
			_ = a.Close()
		}
		if sup != nil {
			sup.Close()
		}
		return quitMsg{}
	}
}

// applyUpdates aplica un lote de novedades a las vistas y vuelca el
// historial del seleccionado si cambió.
func (m *Panel) applyUpdates(ups []orch.Update) {
	touched := false
	for _, u := range ups {
		v := m.byID[u.ID]
		if v == nil {
			continue
		}
		if u.Event != nil {
			v.applyEvent(u.Event)
		} else {
			m.noteState(v, u.Snapshot)
		}
		v.snap, v.hasSnap = u.Snapshot, true
		if v == m.cur() {
			touched = true
		}
	}
	if touched {
		m.refresh()
	}
}

// noteState deja en el historial los cambios de estado relevantes: pausa,
// reanudación, parada, presupuesto y fallos. La cola no: Submit pasa siempre
// por Queued aunque haya cupo, y la lista y la línea de estado ya la muestran.
func (m *Panel) noteState(v *agentView, s orch.Snapshot) {
	prev, had := v.state()
	if had && prev == s.State {
		return
	}
	switch s.State {
	case orch.Running:
		if prev == orch.Paused {
			v.add(kindInfo, "— reanudado —")
		}
	case orch.Paused:
		v.add(kindInfo, "— pausado —")
	case orch.Stopped:
		if !m.quitting {
			v.add(kindInfo, "— detenido —")
		}
	case orch.BudgetExceeded:
		v.add(kindError, s.Reason)
	case orch.Failed:
		if n := len(v.entries); n == 0 || v.entries[n-1].kind != kindError {
			v.add(kindError, "falló: "+s.Reason)
		}
	}
	if s.State != orch.Running {
		v.busy = false
	}
}

// refresh vuelca el historial del seleccionado al viewport. Sigue el final
// solo si la vista ya estaba al final.
func (m *Panel) refresh() { m.refreshTo(m.vp.AtBottom()) }

func (m *Panel) refreshTo(follow bool) {
	v := m.cur()
	m.vp.SetContent(v.cache.render(v.entries, v.asstOpen, m.styles, m.vp.Width()))
	if follow {
		m.vp.GotoBottom()
	}
}

// layout reparte h filas: primero la lista de agentes (si sobra espacio sobre
// lo mínimo para el chat) y el resto entre historial, estado y entrada.
func (m *Panel) layout(w, h int) {
	follow := m.vp.AtBottom()
	m.width, m.height = w, h
	w = max(w, minWidth)
	m.listH = min(len(m.views), maxListRows, max(h-minChatRows, 0))
	m.histH, m.statH, m.inH = splitRows(h - m.listH)
	m.vp.SetWidth(w)
	m.vp.SetHeight(max(m.histH, 1))
	m.input.SetWidth(w)
	m.input.SetHeight(m.inH)
	m.refreshTo(follow)
}

// SetSize fija el tamaño del panel cuando está embebido.
func (m *Panel) SetSize(w, h int) { m.layout(w, h) }

// working indica que hay algo en marcha que merece el spinner.
func (m Panel) working() bool {
	if m.quitting {
		return true
	}
	for _, v := range m.views {
		if v.hasSnap && v.snap.State == orch.Running {
			return true
		}
	}
	return false
}

// Counts devuelve los agentes activos (Running o Paused), los que esperan en
// la cola y el límite de activos.
func (m Panel) Counts() (active, queued, limit int) {
	limit = defaultMaxActive
	if m.sup != nil {
		limit = m.sup.MaxActive()
	}
	for _, v := range m.views {
		switch {
		case v.active():
			active++
		case v.hasSnap && v.snap.State == orch.Queued:
			queued++
		}
	}
	return active, queued, limit
}

// stateLabel es el estado del agente para la lista.
func (v *agentView) stateLabel() string {
	if !v.hasSnap {
		return "new"
	}
	return v.snap.State.String()
}

// listLine compone la fila del agente i de la lista.
func (m Panel) listLine(i int) string {
	v := m.views[i]
	marker := "  "
	switch {
	case i == m.sel:
		marker = "▸ "
	case v.hasSnap && v.snap.State == orch.Running:
		marker = m.spin.View() + " "
	}
	b := m.cfg.Budget
	turns := fmt.Sprintf("t%d", v.snap.Turns)
	if b.MaxTurns > 0 {
		turns += fmt.Sprintf("/%d", b.MaxTurns)
	}
	tokens := fmt.Sprintf("%d", v.snap.Tokens)
	if b.MaxTokens > 0 {
		tokens += fmt.Sprintf("/%d", b.MaxTokens)
	}
	line := fmt.Sprintf("%s%-10s %-15s %s · %s tok · US$%.4f",
		marker, truncate(v.name, 10), v.stateLabel(), turns, tokens, v.snap.CostUSD)

	st := lipgloss.NewStyle()
	if v.hasSnap {
		switch v.snap.State {
		case orch.Running:
			st = st.Foreground(lipgloss.Color("10"))
		case orch.Paused:
			st = st.Foreground(lipgloss.Color("11"))
		case orch.Failed, orch.BudgetExceeded:
			st = st.Foreground(lipgloss.Color("9"))
		case orch.Queued, orch.Stopped:
			st = st.Faint(true)
		}
	} else {
		st = st.Faint(true)
	}
	if i == m.sel {
		st = st.Bold(true).Reverse(true)
	}
	return st.MaxWidth(max(m.width, 1)).Render(line)
}

// listRows son las filas visibles de la lista, con el seleccionado dentro.
func (m Panel) listRows() []string {
	n := len(m.views)
	if m.listH == 0 {
		return nil
	}
	top := 0
	if n > m.listH {
		top = max(0, min(m.sel-m.listH/2, n-m.listH))
	}
	rows := make([]string, 0, m.listH)
	for i := top; i < top+m.listH && i < n; i++ {
		rows = append(rows, m.listLine(i))
	}
	return rows
}

// stateText es el texto de estado del agente seleccionado.
func (m Panel) stateText() string {
	v := m.cur()
	switch {
	case m.quitting:
		return "saliendo…"
	case !v.hasSnap:
		return "libre"
	}
	switch v.snap.State {
	case orch.Running:
		return "pensando…"
	case orch.Queued:
		return "en cola"
	case orch.Paused:
		return "pausado"
	case orch.Stopped:
		return "detenido"
	case orch.Failed:
		return "falló"
	case orch.BudgetExceeded:
		return "presupuesto superado"
	}
	return "libre"
}

// statusLine compone la línea de estado del agente seleccionado.
func (m Panel) statusLine() string {
	v := m.cur()
	lead := "  "
	if m.quitting || (v.hasSnap && v.snap.State == orch.Running) {
		lead = m.spin.View() + " "
	}
	parts := []string{m.stateText()}
	if v.model != "" {
		parts = append(parts, v.model)
	}
	if v.sessionID != "" {
		parts = append(parts, "sesión "+shortSession(v.sessionID))
	}
	parts = append(parts, formatUsage(v.tokensIn, v.tokensOut, v.costUSD), m.keys.panelHelpLine())
	return m.styles.status.MaxWidth(max(m.width, 1)).Render(lead + strings.Join(parts, " · "))
}

// Body devuelve el contenido del panel sin AltScreen ni título, para
// componerlo dentro de un layout.
func (m Panel) Body() string {
	rows := m.listRows()
	if m.histH > 0 {
		rows = append(rows, m.vp.View())
	}
	if m.statH > 0 {
		rows = append(rows, m.statusLine())
	}
	rows = append(rows, m.input.View())
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// View implementa tea.View para el uso autónomo.
func (m Panel) View() tea.View {
	v := tea.NewView(m.Body())
	v.AltScreen = true
	v.WindowTitle = "WARI"
	v.Cursor = m.Cursor()
	return v
}

// Cursor devuelve la posición del cursor relativa a la esquina superior
// izquierda del panel, o nil si no tiene foco.
func (m Panel) Cursor() *tea.Cursor {
	if m.blurred {
		return nil
	}
	c := m.input.Cursor()
	if c == nil {
		return nil
	}
	c.Position.Y += m.listH + m.histH + m.statH
	return c
}

// Focus da el foco a la entrada; Blur se lo quita y oculta el cursor.
func (m *Panel) Focus() {
	m.blurred = false
	m.input.Focus()
}

// Blur quita el foco del panel.
func (m *Panel) Blur() {
	m.blurred = true
	m.input.Blur()
}

// Focused indica si el panel tiene el foco.
func (m Panel) Focused() bool { return !m.blurred }

// maxUpdateBatch es el máximo de novedades que listenUpdates entrega junto.
const maxUpdateBatch = 256

// listenUpdates espera la siguiente novedad del supervisor y drena sin
// bloquear las que ya estén disponibles, para entregarlas en un mensaje. Se
// vuelve a emitir tras manejar cada lote.
func listenUpdates(ch <-chan orch.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return updatesMsg{closed: true}
		}
		msg := updatesMsg{ups: []orch.Update{u}}
		for len(msg.ups) < maxUpdateBatch {
			select {
			case u, ok := <-ch:
				if !ok {
					msg.closed = true
					return msg
				}
				msg.ups = append(msg.ups, u)
			default:
				return msg
			}
		}
		return msg
	}
}
