// Package tui implementa la interfaz de terminal de WARI con Bubble Tea v2.
// En la fase 2 es un panel de chat con un agente; el editor llega después.
package tui

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/config"
)

// inputLines es la altura de la entrada; statusLines, la de la línea de estado.
const (
	inputLines  = 3
	statusLines = 1
	// minWidth evita que los componentes reciban anchos inutilizables.
	minWidth = 10
)

// Config configura el panel de chat.
type Config struct {
	// NewAgent crea un agente nuevo. Se llama al primer envío y después de
	// cada cancelación (Kill deja al agente terminado para siempre).
	NewAgent func() agent.Agent
	// Task es la plantilla de los turnos (Dir, Model, Permissions); el panel
	// rellena Prompt y SessionID.
	Task agent.Task
	// Keymap son los atajos de la configuración; nil usa los predeterminados.
	Keymap config.Keymap
}

// Mensajes internos. Todos los que vienen de un agente llevan la generación
// con la que se creó, para descartar los de un agente ya cancelado.
type (
	// eventsMsg lleva un lote de eventos del agente de la generación gen.
	// closed indica que el canal se cerró tras ellos.
	eventsMsg struct {
		gen    int
		evs    []agent.Event
		closed bool
	}
	// startMsg es el resultado de Start para la generación gen.
	startMsg struct {
		gen int
		err error
	}
	// cancelDoneMsg indica que Kill terminó tras una cancelación.
	cancelDoneMsg struct{ err error }
	// quitMsg indica que Kill terminó tras ctrl+c y hay que salir.
	quitMsg struct{}
)

// Model es el modelo del panel de chat. Implementa tea.Model con receptor de
// valor.
type Model struct {
	cfg    Config
	keys   KeyMap
	styles styles

	vp    viewport.Model
	input textarea.Model
	spin  spinner.Model

	// width es el ancho de la pantalla; histH, statH e inH, las filas que
	// ocupan historial, línea de estado y entrada (suman como máximo el alto).
	width             int
	histH, statH, inH int

	entries []entry
	// asstOpen indica que el último entry es el mensaje del asistente en
	// curso y los TextDelta se agregan a él; asst acumula su texto.
	asstOpen bool
	asst     *strings.Builder
	cache    historyCache

	// ag es el agente vivo (nil si no hay); events es su canal; gen, su
	// generación. dying es un agente cuyo Kill está en curso.
	ag     agent.Agent
	events <-chan agent.Event
	gen    int
	dying  agent.Agent

	// blurred indica que el panel está embebido sin foco: sin cursor real.
	blurred bool

	busy       bool
	cancelling bool
	// refreshes cuenta los volcados del historial al viewport.
	refreshes int
	quitting  bool
	spinning  bool

	sessionID string
	model     string
	tokensIn  int64
	tokensOut int64
	costUSD   float64
}

var _ tea.Model = Model{}

// New crea el modelo del panel.
func New(cfg Config) Model {
	km := NewKeyMap(cfg.Keymap)

	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = "│ "
	ta.Placeholder = "Escribe un mensaje…"
	ta.SetVirtualCursor(false)
	ta.KeyMap.InsertNewline = km.Newline
	st := textarea.DefaultDarkStyles()
	st.Focused.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(st)
	ta.Focus()

	vp := viewport.New()
	vp.KeyMap = viewport.KeyMap{} // el desplazamiento lo manejan los atajos de KeyMap
	vp.MouseWheelEnabled = false
	vp.FillHeight = true

	m := Model{
		cfg:    cfg,
		keys:   km,
		styles: defaultStyles(),
		vp:     vp,
		input:  ta,
		spin:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	m.layout(80, 24)
	return m
}

// toKill devuelve los agentes que aún pueden tener procesos vivos.
func (m Model) toKill() []agent.Agent {
	var list []agent.Agent
	if m.ag != nil {
		list = append(list, m.ag)
	}
	if m.dying != nil {
		list = append(list, m.dying)
	}
	return list
}

// Init implementa tea.Model.
func (m Model) Init() tea.Cmd { return nil }

// Update implementa tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return m.update(msg)
}

func (m Model) update(msg tea.Msg) (Model, tea.Cmd) {
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

	case eventsMsg:
		if msg.gen != m.gen || m.ag == nil {
			return m, nil
		}
		for _, ev := range msg.evs {
			m.applyEvent(ev)
		}
		if msg.closed {
			if m.busy {
				m.add(kindError, "el agente terminó inesperadamente")
			}
			m.busy = false
			m.ag, m.events = nil, nil
			m.refresh()
			return m, nil
		}
		m.refresh()
		return m, listen(m.events, m.gen)

	case startMsg:
		if msg.gen != m.gen || m.ag == nil || msg.err == nil {
			return m, nil
		}
		m.add(kindError, "no se pudo iniciar el turno: "+msg.err.Error())
		m.busy = false
		if m.ag.Status() == agent.StatusDone {
			m.ag, m.events = nil, nil
		}
		m.refresh()
		return m, nil

	case cancelDoneMsg:
		m.cancelling = false
		m.dying = nil
		if msg.err != nil {
			m.add(kindError, "error al cancelar: "+msg.err.Error())
		}
		m.add(kindInfo, "— turno cancelado —")
		m.refresh()
		return m, nil

	case quitMsg:
		return m, tea.Quit
	}

	// Pegado, parpadeo del cursor y demás: los atiende la entrada.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleKey procesa las teclas con los bindings de KeyMap.
func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.quit()
	case m.quitting:
		return m, nil
	case key.Matches(msg, m.keys.Cancel):
		return m.cancel()
	case key.Matches(msg, m.keys.NewSession):
		return m.newSession()
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

// send lanza un turno con el texto de la entrada.
func (m Model) send() (Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if m.busy || m.cancelling || text == "" || m.cfg.NewAgent == nil {
		return m, nil
	}
	m.add(kindUser, text)
	m.input.Reset()

	var cmds []tea.Cmd
	if m.ag == nil || m.ag.Status() == agent.StatusDone {
		m.gen++
		m.ag = m.cfg.NewAgent()
		m.events = m.ag.Events()
		cmds = append(cmds, listen(m.events, m.gen))
	}
	m.busy = true
	m.asstOpen = false

	task := m.cfg.Task
	task.Prompt = text
	task.SessionID = m.sessionID
	ag, gen := m.ag, m.gen
	cmds = append(cmds, func() tea.Msg {
		return startMsg{gen: gen, err: ag.Start(context.Background(), task)}
	})
	if !m.spinning {
		m.spinning = true
		cmds = append(cmds, m.spin.Tick)
	}
	m.refreshTo(true)
	return m, tea.Batch(cmds...)
}

// newSession descarta la sesión guardada y el agente actual. No hace nada con
// un turno en curso o una cancelación pendiente. Kill corre en un Cmd; si el
// programa sale antes, de todos modos lo mata el registro de agentes de main.
func (m Model) newSession() (Model, tea.Cmd) {
	if m.busy || m.cancelling {
		return m, nil
	}
	var cmd tea.Cmd
	if ag := m.ag; ag != nil {
		m.ag, m.events = nil, nil
		m.gen++ // los mensajes del agente descartado se ignoran
		cmd = func() tea.Msg {
			_ = ag.Kill()
			return nil
		}
	}
	m.sessionID = ""
	m.add(kindInfo, "— sesión nueva —")
	m.refresh()
	return m, cmd
}

// cancel mata al agente del turno en curso sin bloquear Update. El agente se
// descarta de inmediato: sus eventos pendientes quedan en una generación vieja.
func (m Model) cancel() (Model, tea.Cmd) {
	if !m.busy || m.cancelling || m.ag == nil {
		return m, nil
	}
	ag := m.ag
	m.dying = ag
	m.ag, m.events = nil, nil
	m.gen++ // los mensajes de la generación anterior se ignoran
	m.busy = false
	m.cancelling = true
	m.asstOpen = false
	return m, func() tea.Msg { return cancelDoneMsg{err: ag.Kill()} }
}

// quit mata a los agentes dentro de un Cmd y después sale.
func (m Model) quit() (Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	m.quitting = true
	agents := m.toKill()
	if len(agents) == 0 {
		return m, tea.Quit
	}
	kill := func() tea.Msg {
		for _, a := range agents {
			_ = a.Kill()
		}
		return quitMsg{}
	}
	if m.spinning {
		return m, kill
	}
	m.spinning = true
	return m, tea.Batch(kill, m.spin.Tick)
}

// maxBatch es el máximo de eventos que listen entrega en un solo mensaje.
const maxBatch = 256

// listen espera el siguiente evento del canal y drena sin bloquear los que ya
// estén disponibles (hasta maxBatch), para entregarlos en un solo mensaje. Se
// vuelve a emitir tras manejar cada lote.
func listen(ch <-chan agent.Event, gen int) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsMsg{gen: gen, closed: true}
		}
		msg := eventsMsg{gen: gen, evs: []agent.Event{ev}}
		for len(msg.evs) < maxBatch {
			select {
			case ev, ok := <-ch:
				if !ok {
					msg.closed = true
					return msg
				}
				msg.evs = append(msg.evs, ev)
			default:
				return msg
			}
		}
		return msg
	}
}

// applyEvent aplica un evento del agente al historial y al estado. No vuelca
// al viewport: quien llama hace un solo refresh por lote.
func (m *Model) applyEvent(ev agent.Event) {
	switch ev := ev.(type) {
	case agent.Started:
		if ev.SessionID != "" {
			m.sessionID = ev.SessionID
		}
		if ev.Model != "" {
			m.model = ev.Model
		}
	case agent.TextDelta:
		if !m.asstOpen || len(m.entries) == 0 || m.asst == nil {
			m.entries = append(m.entries, entry{kind: kindAssistant})
			m.asst = &strings.Builder{}
			m.asstOpen = true
		}
		m.asst.WriteString(ev.Text)
		m.entries[len(m.entries)-1].text = m.asst.String()
	case agent.ToolCall:
		m.add(kindTool, "⚙ "+toolSummary(ev.Name, ev.Input))
	case agent.FileEditProposed:
		m.add(kindEdit, "✎ "+ev.Path)
	case agent.NeedsApproval:
		if ev.Denied {
			m.add(kindDenied, "⛔ denegado: "+toolSummary(ev.Tool, ev.Input))
		}
	case agent.Usage:
		m.tokensIn += ev.InputTokens
		m.tokensOut += ev.OutputTokens
		m.costUSD += ev.CostUSD
	case agent.Error:
		msg := "error desconocido"
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		m.add(kindError, "error: "+msg)
		if errors.Is(ev.Err, agent.ErrSessionNotFound) {
			m.add(kindInfo, "no se pudo reanudar la sesión; "+m.keys.NewSession.Help().Key+" empieza una nueva")
		}
	case agent.Done:
		if ev.SessionID != "" {
			m.sessionID = ev.SessionID
		}
		m.busy = false
		m.asstOpen = false
		if ev.IsError {
			text := "el turno terminó con error"
			if ev.Result != "" {
				text += ": " + ev.Result
			}
			m.add(kindError, text)
		}
	}
}

// add agrega una entrada y cierra el mensaje del asistente en curso.
func (m *Model) add(k entryKind, text string) {
	m.entries = append(m.entries, entry{kind: k, text: text})
	m.asstOpen = false
}

// refresh vuelca el historial al viewport. Sigue el final solo si la vista ya
// estaba al final: así el desplazamiento del usuario se respeta.
func (m *Model) refresh() { m.refreshTo(m.vp.AtBottom()) }

// refreshTo vuelca el historial al viewport y, si follow, baja al final.
func (m *Model) refreshTo(follow bool) {
	m.refreshes++
	m.vp.SetContent(m.cache.render(m.entries, m.asstOpen, m.styles, m.vp.Width()))
	if follow {
		m.vp.GotoBottom()
	}
}

// layout reparte el espacio: historial, línea de estado y entrada. La suma
// nunca supera h. Primero se reduce el historial hasta 1 fila, luego la
// entrada hasta 1 línea, y con menos de 3 filas se omiten el historial y, por
// último, la línea de estado.
func (m *Model) layout(w, h int) {
	follow := m.vp.AtBottom()
	m.width = w
	w = max(w, minWidth)
	h = max(h, 1)

	m.statH, m.inH = statusLines, inputLines
	m.histH = h - m.statH - m.inH
	if m.histH < 1 {
		m.histH = 1
		m.inH = max(h-m.statH-m.histH, 1)
		if m.histH+m.statH+m.inH > h {
			m.histH = 0
		}
		if m.histH+m.statH+m.inH > h {
			m.statH = 0
		}
	}

	m.vp.SetWidth(w)
	m.vp.SetHeight(max(m.histH, 1))
	m.input.SetWidth(w)
	m.input.SetHeight(m.inH)
	m.refreshTo(follow)
}

// working indica que hay algo en marcha que merece el spinner.
func (m Model) working() bool { return m.busy || m.cancelling || m.quitting }

// stateText es el texto de estado de la línea de estado.
func (m Model) stateText() string {
	switch {
	case m.quitting:
		return "saliendo…"
	case m.cancelling:
		return "cancelando…"
	case m.busy:
		return "pensando…"
	}
	return "libre"
}

// statusLine compone la línea de estado, recortada al ancho.
func (m Model) statusLine() string {
	lead := "  "
	if m.working() {
		lead = m.spin.View() + " "
	}
	parts := []string{m.stateText()}
	if m.model != "" {
		parts = append(parts, m.model)
	}
	if m.sessionID != "" {
		parts = append(parts, "sesión "+shortSession(m.sessionID))
	}
	parts = append(parts, formatUsage(m.tokensIn, m.tokensOut, m.costUSD), m.keys.helpLine())
	line := lead + strings.Join(parts, " · ")
	return m.styles.status.MaxWidth(max(m.width, 1)).Render(line)
}

// View implementa tea.Model. Es la vista autónoma (con AltScreen); un layout
// que embeba el panel usa Body y Cursor en su lugar.
func (m Model) View() tea.View {
	v := tea.NewView(m.Body())
	v.AltScreen = true
	v.WindowTitle = "WARI"
	v.Cursor = m.Cursor()
	return v
}

// Body devuelve el contenido del panel sin AltScreen ni título, para
// componerlo dentro de un layout.
func (m Model) Body() string {
	var rows []string
	if m.histH > 0 {
		rows = append(rows, m.vp.View())
	}
	if m.statH > 0 {
		rows = append(rows, m.statusLine())
	}
	rows = append(rows, m.input.View())
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// Cursor devuelve la posición del cursor relativa a la esquina superior
// izquierda del panel, o nil si no tiene foco.
func (m Model) Cursor() *tea.Cursor {
	if m.blurred {
		return nil
	}
	c := m.input.Cursor()
	if c == nil {
		return nil
	}
	c.Position.Y += m.histH + m.statH
	return c
}

// SetSize fija el tamaño del panel cuando está embebido.
func (m *Model) SetSize(w, h int) { m.layout(w, h) }

// Focus da el foco a la entrada; Blur se lo quita y oculta el cursor.
func (m *Model) Focus() {
	m.blurred = false
	m.input.Focus()
}

// Blur quita el foco del panel.
func (m *Model) Blur() {
	m.blurred = true
	m.input.Blur()
}

// Focused indica si el panel tiene el foco.
func (m Model) Focused() bool { return !m.blurred }

// Handle es Update con el tipo concreto del modelo, para embeberlo.
func (m Model) Handle(msg tea.Msg) (Model, tea.Cmd) { return m.update(msg) }

// NewSession descarta la sesión actual, igual que su atajo.
func (m Model) NewSession() (Model, tea.Cmd) { return m.newSession() }

// Quit mata a los agentes y sale, igual que el atajo de salida.
func (m Model) Quit() (Model, tea.Cmd) { return m.quit() }
