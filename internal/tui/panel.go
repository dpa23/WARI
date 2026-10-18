package tui

import (
	"cmp"
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
	"github.com/dpa23/wari/internal/orch/pipeline"
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
	// diffMsg lleva el contenido de un checkpoint y el actual de un archivo.
	diffMsg struct {
		id       orch.ID
		edit     orch.Edit
		old, cur []byte
		err      error
	}
	// revertMsg es el resultado de revertir (archivo, turno o agente).
	revertMsg struct {
		id   orch.ID
		what string
		err  error
	}
)

// diffView es el diff abierto en lugar del historial del agente v.
type diffView struct {
	v    *agentView
	edit orch.Edit
	// text es el diff unificado; vacío mientras se calcula.
	text string
}

// alertInfo es el último choque o bucle sin resolver, para el panel y la
// barra. label es lo que la lista muestra junto al agente ("" es "choque").
type alertInfo struct {
	id    orch.ID
	text  string
	label string
}

// revertible es lo que se pidió revertir.
const (
	revertFile  = "file"
	revertTurn  = "turn"
	revertAgent = "agent"
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

	// nEdits cuenta las ediciones propuestas (para mostrar el atajo del diff).
	nEdits int
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

	// dv es el diff abierto (nil: se muestra el historial); armed, la
	// reversión que espera confirmación; alert, el último choque.
	dv        *diffView
	armed     string
	armedText string
	alert     alertInfo

	// Pipeline (pipeline.go): el Runner, su último estado, las filas que ocupa
	// en el panel, el gate abierto y el modo de edición del prompt.
	pipe      *pipeline.Runner
	ps        pipeline.Snapshot
	pipeH     int
	gate      *gateView
	gateEdit  bool
	gateDraft string
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
		pipe:      cfg.Pipeline,
	}
	if m.pipe != nil {
		m.ps = m.pipe.Snapshot()
	}
	m.addView()
	m.layout(80, 24)
	return m
}

// Init implementa tea.Model: empieza a escuchar las novedades del supervisor.
func (m Panel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.sup != nil {
		cmds = append(cmds, listenUpdates(m.sup.Updates()))
	}
	if m.pipe != nil {
		cmds = append(cmds, listenPipeline(m.pipe.Changed()))
	}
	return tea.Batch(cmds...)
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

	case diffMsg:
		if m.dv != nil && m.dv.v.id == msg.id && m.dv.edit == msg.edit {
			if msg.err != nil {
				m.dv.text = "no se pudo obtener el diff: " + msg.err.Error()
			} else {
				m.dv.text = diffText(msg.edit.Path, msg.edit.Turn, msg.old, msg.cur)
			}
			m.refreshTo(false)
		}
		return m, nil

	case revertMsg:
		v := m.byID[msg.id]
		if v == nil {
			return m, nil
		}
		if msg.err != nil {
			v.add(kindError, fmt.Sprintf("no se pudo %s: %v", msg.what, msg.err))
		} else {
			v.add(kindInfo, "— "+msg.what+": listo —")
			if m.dv != nil && m.dv.v == v {
				m.dv = nil
			}
		}
		m.refreshTo(v != m.cur() || m.vp.AtBottom())
		return m, nil

	case pipeMsg:
		if msg.closed {
			return m, nil
		}
		cmds := m.syncPipeline()
		cmds = append(cmds, listenPipeline(m.pipe.Changed()))
		return m, tea.Batch(cmds...)

	case gateDiffMsg:
		m.applyGateDiff(msg)
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
	// La confirmación de una reversión se desarma con cualquier otra tecla.
	armed := m.armed
	m.armed, m.armedText = "", ""
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.quit()
	case m.quitting:
		return m, nil
	case key.Matches(msg, m.keys.Cancel) && m.dv != nil:
		m.closeDiff()
		return m, nil
	case key.Matches(msg, m.keys.Cancel) && m.gateEdit:
		m.endGateEdit()
		return m, nil
	case key.Matches(msg, m.keys.Cancel) && m.gateShown():
		m.hideGate()
		return m, nil
	case m.pipe != nil && key.Matches(msg, m.keys.PipelineRun):
		return m.pipelineRun()
	case m.pipe != nil && key.Matches(msg, m.keys.GateApprove):
		return m.pipelineOp("aprobar", (*pipeline.Runner).Approve)
	case m.pipe != nil && key.Matches(msg, m.keys.GateReject):
		return m.pipelineOp("rechazar", (*pipeline.Runner).Reject)
	case m.pipe != nil && key.Matches(msg, m.keys.GateEdit):
		return m.gateEditStart()
	case key.Matches(msg, m.keys.DiffFile):
		return m.diff()
	case key.Matches(msg, m.keys.RevertFile):
		return m.revert(revertFile, armed)
	case key.Matches(msg, m.keys.RevertTurn):
		return m.revert(revertTurn, armed)
	case key.Matches(msg, m.keys.RevertAgent):
		return m.revert(revertAgent, armed)
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
		transcript: transcript{newSessionKey: m.keys.AgentNew.Help().Key, root: m.cfg.Task.Dir},
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
	m.dv = nil
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
	if m.gateEdit {
		return m.gateEditSave()
	}
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
	m.dv = nil
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
	touched, adopted := false, false
	for _, u := range ups {
		if u.Notice != nil {
			if m.applyNotice(u) {
				touched = true
			}
			continue
		}
		v := m.byID[u.ID]
		if v == nil {
			if v = m.adopt(u); v == nil {
				continue
			}
			adopted = true
		}
		if u.Event != nil {
			v.applyEvent(u.Event)
			if _, ok := u.Event.(agent.FileEditProposed); ok {
				v.nEdits++
			}
		} else {
			m.noteState(v, u.Snapshot)
			if m.alert.id == u.ID && u.Snapshot.State != orch.Paused {
				m.alert = alertInfo{}
			}
		}
		v.snap, v.hasSnap = u.Snapshot, true
		if v == m.cur() {
			touched = true
		}
	}
	if adopted {
		m.layout(m.width, m.height)
	} else if touched {
		m.refresh()
	}
}

// applyNotice aplica un aviso del espacio de trabajo y dice si cambió el
// historial que se está viendo.
func (m *Panel) applyNotice(u orch.Update) bool {
	n := u.Notice
	v := m.byID[u.ID]
	cur := m.cur()
	changed := false
	add := func(to *agentView, k entryKind, text string) {
		if to == nil {
			return
		}
		to.add(k, text)
		changed = changed || to == cur
	}
	switch n.Kind {
	case orch.NoticeConflict:
		m.alert = alertInfo{id: n.Agent, text: n.Text}
		add(v, kindWarn, "⚠ "+n.Text)
		if !n.Late {
			add(v, kindInfo, fmt.Sprintf("%s reanuda y le da el archivo; %s lo detiene y descarta su turno",
				m.keys.AgentPause.Help().Key, m.keys.AgentStop.Help().Key))
			if h := m.byID[n.Holder]; h != nil && v != nil {
				add(h, kindWarn, fmt.Sprintf("⚠ %s quiso editar %s, que tienes reclamado", v.name, n.Path))
			}
		}
	case orch.NoticeLoop:
		if !n.Late {
			m.alert = alertInfo{id: n.Agent, text: n.Text, label: "bucle"}
		}
		add(v, kindWarn, "⚠ "+n.Text)
		if !n.Late {
			add(v, kindInfo, fmt.Sprintf("%s reanuda; %s lo detiene",
				m.keys.AgentPause.Help().Key, m.keys.AgentStop.Help().Key))
		}
	case orch.NoticeTakeover:
		if m.alert.id == n.Agent {
			m.alert = alertInfo{}
		}
		add(v, kindInfo, "— "+n.Text+" —")
	case orch.NoticeCheckpointFailed:
		add(v, kindError, n.Text)
	case orch.NoticeChanged:
		switch n.Attr {
		case orch.AttrSoleActive:
			add(v, kindEdit, n.Text)
		case orch.AttrUnknown:
			// Sin agente al que atribuirlo: se deja en el historial visible,
			// sin repetir la misma línea.
			if k := len(cur.entries); k == 0 || cur.entries[k-1].text != n.Text {
				add(cur, kindEdit, n.Text)
			}
		}
		// AttrProposal ya tiene su línea "✎ ruta" del evento de edición.
	}
	return changed
}

// Alert devuelve el choque de archivos sin resolver ("" si no hay), para la
// barra de estado.
func (m Panel) Alert() string { return m.alert.text }

// NoteUserWrite avisa al supervisor de que WARI mismo escribirá path (guardar
// desde el editor): el watcher no lo atribuirá a ningún agente.
func (m Panel) NoteUserWrite(path string) {
	if m.sup != nil {
		m.sup.NoteUserWrite(path)
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
	if m.dv != nil && m.dv.v == v {
		m.vp.SetContent(m.dv.render(m.styles, m.vp.Width()))
		return
	}
	if m.gateShown() {
		m.vp.SetContent(m.gate.render(m.styles, m.keys, m.vp.Width()))
		return
	}
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
	avail := max(h-minChatRows, 0)
	m.pipeH = m.pipelineRows(avail)
	m.listH = min(len(m.views), maxListRows, max(avail-m.pipeH, 0))
	m.histH, m.statH, m.inH = splitRows(h - m.pipeH - m.listH)
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

	if m.alert.id != 0 && v.id == m.alert.id {
		line += " ⚠ " + cmp.Or(m.alert.label, "choque")
	}

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
	if m.armedText != "" {
		parts = []string{m.armedText}
	}
	if m.dv != nil && m.dv.v == v {
		parts = append(parts, fmt.Sprintf("diff %s (turno %d)", m.dv.edit.Path, m.dv.edit.Turn))
	}
	if m.cfg.Task.Permissions == agent.PermissionAcceptEdits {
		parts = append(parts, "edita sin preguntar")
	}
	if v.model != "" {
		parts = append(parts, v.model)
	}
	if v.sessionID != "" {
		parts = append(parts, "sesión "+shortSession(v.sessionID))
	}
	if h := m.pipelineHint(); h != "" {
		parts = append(parts, h)
	}
	parts = append(parts, formatUsage(v.tokensIn, v.tokensOut, v.costUSD))
	if m.dv != nil && m.dv.v == v {
		parts = append(parts, joinHelp(m.keys.DiffFile, m.keys.RevertFile, m.keys.RevertTurn, m.keys.Cancel))
	} else {
		if v.nEdits > 0 && m.keys.DiffFile.Enabled() {
			parts = append(parts, joinHelp(m.keys.DiffFile))
		}
		parts = append(parts, m.keys.panelHelpLine())
	}
	return m.styles.status.MaxWidth(max(m.width, 1)).Render(lead + strings.Join(parts, " · "))
}

// Body devuelve el contenido del panel sin AltScreen ni título, para
// componerlo dentro de un layout.
func (m Panel) Body() string {
	rows := append(m.pipeRows(), m.listRows()...)
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
	c.Position.Y += m.pipeH + m.listH + m.histH + m.statH
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

// render compone el diff abierto con su título.
func (d *diffView) render(st styles, width int) string {
	title := st.info.Render(fmt.Sprintf("%s · turno %d", d.edit.Path, d.edit.Turn))
	if d.text == "" {
		return title + "\n" + st.info.Render("calculando el diff…")
	}
	return title + "\n" + renderDiff(d.text, st, width)
}

// closeDiff vuelve al historial del agente.
func (m *Panel) closeDiff() {
	m.dv = nil
	m.refreshTo(m.vp.AtBottom())
}

// savedEdits son las ediciones del agente con checkpoint (las que se pueden
// ver y revertir).
func (m Panel) savedEdits(v *agentView) []orch.Edit {
	if m.sup == nil || v.id == 0 {
		return nil
	}
	var out []orch.Edit
	for _, e := range m.sup.Edits(v.id) {
		if e.Saved {
			out = append(out, e)
		}
	}
	return out
}

// diff abre el diff de la última edición del agente seleccionado contra su
// checkpoint; con uno ya abierto, pasa a la edición siguiente.
func (m Panel) diff() (Panel, tea.Cmd) {
	v := m.cur()
	eds := m.savedEdits(v)
	if len(eds) == 0 {
		v.add(kindInfo, "no hay ediciones con checkpoint que mostrar")
		m.refresh()
		return m, nil
	}
	idx := len(eds) - 1
	if m.dv != nil && m.dv.v == v {
		for i, e := range eds {
			if e == m.dv.edit {
				idx = (i + 1) % len(eds)
				break
			}
		}
	}
	ed := eds[idx]
	m.dv = &diffView{v: v, edit: ed}
	m.refreshTo(false)
	m.vp.GotoTop()
	sup, id := m.sup, v.id
	return m, func() tea.Msg {
		old, cur, err := sup.Diff(id, ed.Turn, ed.Path)
		return diffMsg{id: id, edit: ed, old: old, cur: cur, err: err}
	}
}

// revert revierte el archivo que se está viendo (o el de la última edición),
// su turno o todo el agente. Pide confirmación: la primera pulsación solo
// avisa y repetirla ejecuta.
func (m Panel) revert(kind, armed string) (Panel, tea.Cmd) {
	v := m.cur()
	eds := m.savedEdits(v)
	if len(eds) == 0 {
		v.add(kindInfo, "no hay ediciones con checkpoint que revertir")
		m.refresh()
		return m, nil
	}
	ed := eds[len(eds)-1]
	if m.dv != nil && m.dv.v == v {
		ed = m.dv.edit
	}
	var what, token, keyName string
	switch kind {
	case revertFile:
		what = fmt.Sprintf("revertir %s al checkpoint del turno %d", ed.Path, ed.Turn)
		token = fmt.Sprintf("%s:%d:%s", kind, ed.Turn, ed.Path)
		keyName = m.keys.RevertFile.Help().Key
	case revertTurn:
		what = fmt.Sprintf("revertir los archivos que editó %s en el turno %d", v.name, ed.Turn)
		token = fmt.Sprintf("%s:%d", kind, ed.Turn)
		keyName = m.keys.RevertTurn.Help().Key
	default:
		what = fmt.Sprintf("revertir todo lo que editó %s", v.name)
		token = kind
		keyName = m.keys.RevertAgent.Help().Key
	}
	if armed != token {
		m.armed = token
		m.armedText = fmt.Sprintf("¿%s? repite %s para confirmar", what, keyName)
		v.add(kindWarn, m.armedText+" (cualquier otra tecla cancela)")
		m.refresh()
		return m, nil
	}
	sup, id := m.sup, v.id
	return m, func() tea.Msg {
		var err error
		switch kind {
		case revertFile:
			err = sup.RevertFile(id, ed.Turn, ed.Path)
		case revertTurn:
			err = sup.RevertTurn(id, ed.Turn)
		default:
			err = sup.RevertAgent(id)
		}
		return revertMsg{id: id, what: what, err: err}
	}
}
