// Package tui implementa la interfaz de terminal de WARI con Bubble Tea v2.
// En la fase 2 es un panel de chat con un agente; el editor llega después.
package tui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/orch"
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
	// Agent es el agente de la sesión. El panel lo usa durante toda su vida:
	// cancelar un turno (Cancel) no lo termina; Close solo se llama al salir.
	// Con nil el panel no envía nada.
	Agent agent.Agent
	// Task es la plantilla de los turnos (Dir, Model, Permissions); el panel
	// rellena Prompt y SessionID.
	Task agent.Task
	// Keymap son los atajos de la configuración; nil usa los predeterminados.
	Keymap config.Keymap

	// Supervisor, NewAgent y Budget son para el panel multiagente (Panel).
	// Supervisor gestiona los agentes; el panel se hace dueño de ellos y no
	// lo cierra salvo en Quit. NewAgent crea un agente sin iniciar; Budget se
	// aplica a cada agente nuevo.
	Supervisor *orch.Supervisor
	NewAgent   func() agent.Agent
	Budget     orch.Budget
}

// Mensajes internos.
type (
	// eventsMsg lleva un lote de eventos del agente. closed indica que el
	// canal se cerró tras ellos.
	eventsMsg struct {
		evs    []agent.Event
		closed bool
	}
	// startMsg es el resultado de Start.
	startMsg struct{ err error }
	// cancelDoneMsg indica que Cancel terminó.
	cancelDoneMsg struct{ err error }
	// quitMsg indica que Close terminó tras ctrl+c y hay que salir.
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

	// transcript es el historial y el consumo del agente.
	transcript

	// ag es el agente de la sesión (nil si no hay). listening indica que hay
	// un Cmd de escucha en curso sobre su canal; dead, que el canal se cerró.
	ag        agent.Agent
	listening bool
	dead      bool

	// blurred indica que el panel está embebido sin foco: sin cursor real.
	blurred bool

	// starting indica que el Cmd de Start no ha vuelto; cancelling, que hay
	// una cancelación en curso (los eventos que lleguen son del turno
	// cancelado y se descartan); cancelPending, que Esc llegó antes de que
	// Start volviera y la cancelación espera a que el turno exista.
	starting      bool
	cancelling    bool
	cancelPending bool
	// refreshes cuenta los volcados del historial al viewport.
	refreshes int
	quitting  bool
	spinning  bool
}

var _ tea.Model = Model{}

// New crea el modelo del panel.
func New(cfg Config) Model {
	km := NewKeyMap(cfg.Keymap)

	m := Model{
		cfg:        cfg,
		ag:         cfg.Agent,
		keys:       km,
		styles:     defaultStyles(),
		vp:         newViewport(),
		input:      newInput(km),
		spin:       newSpinner(),
		transcript: transcript{newSessionKey: km.NewSession.Help().Key},
	}
	m.layout(80, 24)
	return m
}

// newInput crea la entrada de texto del panel.
func newInput(km KeyMap) textarea.Model {
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
	return ta
}

// newViewport crea el viewport del historial.
func newViewport() viewport.Model {
	vp := viewport.New()
	vp.KeyMap = viewport.KeyMap{} // el desplazamiento lo manejan los atajos de KeyMap
	vp.MouseWheelEnabled = false
	vp.FillHeight = true
	return vp
}

func newSpinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.MiniDot))
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
		if m.ag == nil {
			return m, nil
		}
		// Durante una cancelación, lo que llega es del turno cancelado.
		if !m.cancelling {
			for _, ev := range msg.evs {
				m.applyEvent(ev)
			}
		}
		if msg.closed {
			if m.busy && !m.quitting {
				m.add(kindError, "el agente terminó inesperadamente")
			}
			m.busy = false
			m.listening = false
			m.dead = true
			m.refresh()
			return m, nil
		}
		m.refresh()
		return m, listen(m.ag.Events())

	case startMsg:
		m.starting = false
		if msg.err == nil {
			if m.cancelPending {
				m.cancelPending = false
				return m, m.cancelCmd()
			}
			return m, nil
		}
		m.add(kindError, "no se pudo iniciar el turno: "+msg.err.Error())
		m.busy = false
		m.cancelling = false
		m.cancelPending = false
		m.refresh()
		return m, nil

	case cancelDoneMsg:
		m.cancelling = false
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
	if m.busy || m.cancelling || text == "" || m.ag == nil {
		return m, nil
	}
	if m.dead {
		m.add(kindError, "el agente terminó; hay que reiniciar WARI")
		m.refresh()
		return m, nil
	}
	m.add(kindUser, text)
	m.input.Reset()

	var cmds []tea.Cmd
	if !m.listening {
		m.listening = true
		cmds = append(cmds, listen(m.ag.Events()))
	}
	m.busy = true
	m.starting = true
	m.asstOpen = false

	task := m.cfg.Task
	task.Prompt = text
	task.SessionID = m.sessionID
	ag := m.ag
	cmds = append(cmds, func() tea.Msg {
		return startMsg{err: ag.Start(context.Background(), task)}
	})
	if !m.spinning {
		m.spinning = true
		cmds = append(cmds, m.spin.Tick)
	}
	m.refreshTo(true)
	return m, tea.Batch(cmds...)
}

// newSession descarta la sesión guardada: el siguiente mensaje empieza una
// sesión nueva con el mismo agente. Si hay un turno en curso, lo cancela.
func (m Model) newSession() (Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.busy {
		m, cmd = m.cancel()
	}
	m.sessionID = ""
	m.add(kindInfo, "— sesión nueva —")
	m.refresh()
	return m, cmd
}

// cancel cancela el turno en curso sin bloquear Update. El agente sigue vivo
// y se reutiliza en el siguiente envío.
func (m Model) cancel() (Model, tea.Cmd) {
	if !m.busy || m.cancelling || m.ag == nil {
		return m, nil
	}
	m.busy = false
	m.cancelling = true
	m.asstOpen = false
	if m.starting {
		// Cancel no vería ningún turno todavía: espera al resultado de Start.
		m.cancelPending = true
		return m, nil
	}
	return m, m.cancelCmd()
}

// cancelCmd es el Cmd que cancela el turno del agente.
func (m Model) cancelCmd() tea.Cmd {
	ag := m.ag
	return func() tea.Msg { return cancelDoneMsg{err: ag.Cancel()} }
}

// quit cierra al agente dentro de un Cmd y después sale. Si el programa muere
// antes, de todos modos lo cierra el defer de main.
func (m Model) quit() (Model, tea.Cmd) {
	if m.quitting {
		return m, nil
	}
	m.quitting = true
	if m.ag == nil {
		return m, tea.Quit
	}
	ag := m.ag
	closeAgent := func() tea.Msg {
		_ = ag.Close()
		return quitMsg{}
	}
	if m.spinning {
		return m, closeAgent
	}
	m.spinning = true
	return m, tea.Batch(closeAgent, m.spin.Tick)
}

// maxBatch es el máximo de eventos que listen entrega en un solo mensaje.
const maxBatch = 256

// listen espera el siguiente evento del canal y drena sin bloquear los que ya
// estén disponibles (hasta maxBatch), para entregarlos en un solo mensaje. Se
// vuelve a emitir tras manejar cada lote.
func listen(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsMsg{closed: true}
		}
		msg := eventsMsg{evs: []agent.Event{ev}}
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

// splitRows reparte h filas entre historial, línea de estado y entrada. La
// suma nunca supera h. Primero se reduce el historial hasta 1 fila, luego la
// entrada hasta 1 línea, y con menos de 3 filas se omiten el historial y, por
// último, la línea de estado.
func splitRows(h int) (histH, statH, inH int) {
	h = max(h, 1)
	statH, inH = statusLines, inputLines
	histH = h - statH - inH
	if histH < 1 {
		histH = 1
		inH = max(h-statH-histH, 1)
		if histH+statH+inH > h {
			histH = 0
		}
		if histH+statH+inH > h {
			statH = 0
		}
	}
	return histH, statH, inH
}

// layout reparte el espacio: historial, línea de estado y entrada.
func (m *Model) layout(w, h int) {
	follow := m.vp.AtBottom()
	m.width = w
	w = max(w, minWidth)
	m.histH, m.statH, m.inH = splitRows(h)

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

// Quit cierra al agente y sale, igual que el atajo de salida.
func (m Model) Quit() (Model, tea.Cmd) { return m.quit() }
