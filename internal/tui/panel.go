package tui

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dpa23/wari/internal/tui/theme"

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
		// git indica que la base es git HEAD y no un checkpoint.
		git bool
	}
	// revertMsg es el resultado de revertir (archivo, turno o agente).
	revertMsg struct {
		id   orch.ID
		what string
		err  error
	}
)

// alertInfo es el último choque o bucle sin resolver, para el panel y la
// barra. label es lo que la lista muestra junto al agente ("" es "choque").
type alertInfo struct {
	id    orch.ID
	path  string
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

// revertModal almacena el estado del diálogo interactivo de confirmación de reversión.
type revertModal struct {
	kind    string // revertFile, revertTurn, revertAgent
	turn    int
	path    string
	files   []string
	agentID orch.ID
	name    string
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

	// dv es el diff abierto (nil: ninguno). No toca el historial: el layout
	// lo dibuja en el panel central (diffview.go). armed es la reversión que
	// espera confirmación (armedText, su aviso; armedShort, el aviso corto
	// para áreas angostas); alert, el último choque.
	dv             *diffView
	diffW, diffH   int // área del panel central donde se dibuja el diff
	rm             *revertModal
	armed          string
	armedText      string
	armedShort     string
	alert          alertInfo
	conflictActive bool

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
				m.dv.setText("no se pudo obtener el diff: " + msg.err.Error())
			} else {
				if msg.git {
					m.dv.setText(diffTextBase(msg.edit.Path, "git HEAD, sin checkpoint", "git HEAD", msg.old, msg.cur))
				} else {
					m.dv.setText(diffText(msg.edit.Path, msg.edit.Turn, msg.old, msg.cur))
				}
			}
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
	if m.rm != nil {
		switch {
		case key.Matches(msg, m.keys.Cancel):
			m = m.cancelRevert()
			return m, nil
		case key.Matches(msg, m.keys.Send) || msg.String() == "enter":
			return m.confirmRevert()
		case key.Matches(msg, m.keys.RevertFile):
			if m.rm.kind == revertFile {
				return m.confirmRevert()
			}
			return m.revert(revertFile)
		case key.Matches(msg, m.keys.RevertTurn):
			if m.rm.kind == revertTurn {
				return m.confirmRevert()
			}
			return m.revert(revertTurn)
		case key.Matches(msg, m.keys.RevertAgent):
			if m.rm.kind == revertAgent {
				return m.confirmRevert()
			}
			return m.revert(revertAgent)
		case msg.String() == "tab":
			next := revertFile
			switch m.rm.kind {
			case revertFile:
				next = revertTurn
			case revertTurn:
				next = revertAgent
			case revertAgent:
				next = revertFile
			}
			return m.revert(next)
		}
	}
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
	case m.conflictActive && (msg.String() == "1" || msg.Text == "1"):
		m.conflictActive = false
		return m, m.pauseCmd(m.cur())
	case m.conflictActive && (msg.String() == "2" || msg.Text == "2"):
		m.conflictActive = false
		return m, m.stopCmd(m.cur())
	case m.conflictActive && key.Matches(msg, m.keys.Cancel):
		m.conflictActive = false
		m.refresh()
		return m, nil
	case m.alert.id != 0 && (msg.String() == "f6" || msg.String() == "alt+c"):
		return m.FocusConflict()
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
		return m.revert(revertFile)
	case key.Matches(msg, m.keys.RevertTurn):
		return m.revert(revertTurn)
	case key.Matches(msg, m.keys.RevertAgent):
		return m.revert(revertAgent)
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.AgentStop):
		m.conflictActive = false
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
		m.conflictActive = false
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
	m.conflictActive = false
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

// Pause conmuta la pausa o reanudación del agente seleccionado.
func (m Panel) Pause() (Panel, tea.Cmd) {
	return m, m.pauseCmd(m.cur())
}

// Stop detiene el turno del agente seleccionado.
func (m Panel) Stop() (Panel, tea.Cmd) {
	return m, m.stopCmd(m.cur())
}

// Diff abre o alterna el diff de la última edición del agente seleccionado.
func (m Panel) Diff() (Panel, tea.Cmd) {
	return m.diff()
}

// SetDiffArea fija el tamaño del área central donde se dibuja el diff (la
// que usan el desplazamiento y los saltos entre hunks).
func (m *Panel) SetDiffArea(w, h int) {
	m.diffW, m.diffH = w, h
	if m.dv != nil {
		m.dv.setArea(w, h)
	}
}

// CloseDiff cierra el diff abierto, si lo hay.
func (m Panel) CloseDiff() Panel {
	m.dv = nil
	return m
}

// DiffOpen indica que el agente seleccionado tiene un diff abierto, que el
// layout dibuja en el panel central con DiffTitle y DiffLines.
func (m Panel) DiffOpen() bool { return m.dv != nil && m.dv.v == m.cur() }

// DiffTitle es el título de la vista del diff: archivo, turno, hunk actual y
// desplazamiento horizontal.
func (m Panel) DiffTitle() string {
	if !m.DiffOpen() {
		return ""
	}
	d := m.dv
	t := fmt.Sprintf("diff %s · turno %d", d.edit.Path, d.edit.Turn)
	if n := len(d.hunks); n > 0 {
		t += fmt.Sprintf(" · hunk %d/%d", d.cur+1, n)
	}
	if d.xoff > 0 {
		t += fmt.Sprintf(" · col %d", d.xoff+1)
	}
	return t
}

// DiffLines dibuja el diff abierto en exactamente h filas de w celdas (vacío
// si no hay ninguno abierto).
func (m Panel) DiffLines(w, h int) []string {
	if !m.DiffOpen() {
		return nil
	}
	m.dv.rm = m.rm
	return m.dv.lines(m.styles, m.keys, m.armedText, m.armedShort, w, h)
}

// DiffKey atiende una tecla con la vista del diff en primer plano: desplazar,
// saltar entre hunks, pasar al diff siguiente, volver (Esc) y revertir con su
// confirmación. Las demás teclas no hacen nada: la vista es de solo lectura.
func (m Panel) DiffKey(msg tea.KeyPressMsg) (Panel, tea.Cmd) {
	if !m.DiffOpen() {
		return m, nil
	}
	if m.rm != nil {
		switch {
		case key.Matches(msg, m.keys.Cancel):
			m = m.cancelRevert()
			return m, nil
		case key.Matches(msg, m.keys.Send) || msg.String() == "enter":
			return m.confirmRevert()
		case key.Matches(msg, m.keys.RevertFile):
			if m.rm.kind == revertFile {
				return m.confirmRevert()
			}
			return m.revert(revertFile)
		case key.Matches(msg, m.keys.RevertTurn):
			if m.rm.kind == revertTurn {
				return m.confirmRevert()
			}
			return m.revert(revertTurn)
		case key.Matches(msg, m.keys.RevertAgent):
			if m.rm.kind == revertAgent {
				return m.confirmRevert()
			}
			return m.revert(revertAgent)
		case msg.String() == "tab":
			next := revertFile
			switch m.rm.kind {
			case revertFile:
				next = revertTurn
			case revertTurn:
				next = revertAgent
			case revertAgent:
				next = revertFile
			}
			return m.revert(next)
		}
	}
	switch {
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.DiffFile),
		key.Matches(msg, m.keys.RevertFile), key.Matches(msg, m.keys.RevertTurn),
		key.Matches(msg, m.keys.RevertAgent):
		return m.handleKey(msg)
	}
	d := m.dv
	page := max(d.bodyH()-1, 1)
	switch s := msg.String(); {
	case s == "n" || s == "]":
		d.gotoHunk(d.cur + 1)
	case s == "p" || s == "[":
		d.gotoHunk(d.cur - 1)
	case s == "up" || s == "k":
		d.scroll(-1, 0)
	case s == "down" || s == "j":
		d.scroll(1, 0)
	case key.Matches(msg, m.keys.PageUp):
		d.scroll(-page, 0)
	case key.Matches(msg, m.keys.PageDown), s == "space":
		d.scroll(page, 0)
	case s == "home" || s == "g":
		d.scroll(-len(d.rows), 0)
	case s == "end" || s == "G":
		d.scroll(len(d.rows), 0)
	case s == "left" || s == "h":
		d.scroll(0, -8)
	case s == "right" || s == "l":
		d.scroll(0, 8)
	}
	return m, nil
}

// RevertFile inicia o confirma la reversión del archivo seleccionado.
func (m Panel) RevertFile() (Panel, tea.Cmd) {
	if m.rm != nil && m.rm.kind == revertFile {
		return m.confirmRevert()
	}
	return m.revert(revertFile)
}

// RevertTurn inicia o confirma la reversión del turno del agente.
func (m Panel) RevertTurn() (Panel, tea.Cmd) {
	if m.rm != nil && m.rm.kind == revertTurn {
		return m.confirmRevert()
	}
	return m.revert(revertTurn)
}

// RevertAgent inicia o confirma la reversión total del agente.
func (m Panel) RevertAgent() (Panel, tea.Cmd) {
	if m.rm != nil && m.rm.kind == revertAgent {
		return m.confirmRevert()
	}
	return m.revert(revertAgent)
}

// RevertModalActive indica si hay un diálogo de confirmación de reversión activo.
func (m Panel) RevertModalActive() bool { return m.rm != nil }

// RevertModalFiles devuelve los archivos afectados en la reversión pendiente.
func (m Panel) RevertModalFiles() []string {
	if m.rm == nil {
		return nil
	}
	return append([]string(nil), m.rm.files...)
}

// RevertModalKind devuelve el alcance ("file", "turn", "agent") del diálogo activo.
func (m Panel) RevertModalKind() string {
	if m.rm == nil {
		return ""
	}
	return m.rm.kind
}

// FocusConflict enfoca al agente en conflicto y activa el modo de resolución
// asistida en la línea de estado del panel.
func (m Panel) FocusConflict() (Panel, tea.Cmd) {
	if m.alert.id == 0 {
		return m, nil
	}
	for i, v := range m.views {
		if v.id == m.alert.id {
			m.selectView(i)
			m.conflictActive = true
			m.refresh()
			return m, nil
		}
	}
	return m, nil
}

// ConflictActive indica si el panel está en modo de resolución asistida de choque.
func (m Panel) ConflictActive() bool { return m.conflictActive }

// SelectAgent selecciona el agente con id si existe en las vistas.
func (m *Panel) SelectAgent(id orch.ID) bool {
	if id == 0 {
		return false
	}
	for i, v := range m.views {
		if v.id == id {
			m.selectView(i)
			return true
		}
	}
	return false
}

// Supervisor devuelve el supervisor de agentes (nil si no hay).
func (m Panel) Supervisor() *orch.Supervisor { return m.sup }

// ConflictPath devuelve la ruta relativa del archivo en choque sin resolver ("" si no hay).
func (m Panel) ConflictPath() string { return m.alert.path }

// ConflictAgent devuelve el ID del agente pausado por el choque (0 si no hay).
func (m Panel) ConflictAgent() orch.ID { return m.alert.id }

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
				m.conflictActive = false
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
		m.alert = alertInfo{id: n.Agent, path: n.Path, text: n.Text}
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
			m.conflictActive = false
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

// stateWord es la palabra completa del estado en mayúsculas (RUNNING, IDLE...),
// el mismo vocabulario que el chip angosto de stateBadge.
func (v *agentView) stateWord() string {
	return strings.ToUpper(v.stateLabel())
}

// formatDuration formatea una duración de forma compacta (p. ej. "45s", "1m30s").
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

// microBar genera una barra compacta de 5 caracteres tipo [■■■░░] según el ratio.
func microBar(ratio float64) string {
	const total = 5
	clamped := max(0.0, min(ratio, 1.0))
	filled := int(math.Round(clamped * float64(total)))
	filled = max(0, min(filled, total))
	return "[" + strings.Repeat("■", filled) + strings.Repeat("░", total-filled) + "]"
}

// budgetStyle aplica color ámbar al superar 75% del presupuesto (> 0.75) y
// rojo negrita al superar 90% (> 0.90).
func budgetStyle(base lipgloss.Style, ratio float64) lipgloss.Style {
	if ratio > 0.90 {
		return base.Foreground(lipgloss.Color("9")).Bold(true)
	}
	if ratio > 0.75 {
		return base.Foreground(lipgloss.Color("214"))
	}
	return base
}

// stateBadge devuelve el texto corto (RUN, PAU, Q, IDLE, STOP, ERR, $!) y el estilo del badge.
func stateBadge(st orch.State, hasSnap bool) (string, lipgloss.Style) {
	if !hasSnap {
		return "NEW", lipgloss.NewStyle().Faint(true)
	}
	switch st {
	case orch.Running:
		return "RUN", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	case orch.Paused:
		return "PAU", lipgloss.NewStyle().Bold(true).Foreground(theme.AdaptiveColor{
			Light: lipgloss.Color("#b45309"),
			Dark:  lipgloss.Color("#fbbf24"),
		})
	case orch.Queued:
		return "Q", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	case orch.Idle:
		return "IDLE", lipgloss.NewStyle().Foreground(theme.AdaptiveColor{
			Light: lipgloss.Color("#1d4ed8"),
			Dark:  lipgloss.Color("#93c5fd"),
		})
	case orch.Stopped:
		return "STOP", lipgloss.NewStyle().Faint(true)
	case orch.Failed:
		return "ERR", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	case orch.BudgetExceeded:
		return "$!", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	default:
		return "?", lipgloss.NewStyle().Faint(true)
	}
}

// selectedBadge resalta el chip de la fila seleccionada. IDLE lleva fondo y
// texto adaptativos propios para que contraste en fondos claros y oscuros;
// el resto usa vídeo inverso.
func selectedBadge(st lipgloss.Style, idle bool) lipgloss.Style {
	if idle {
		return st.Bold(true).Reverse(false).
			Foreground(theme.AdaptiveColor{Light: lipgloss.Color("#ffffff"), Dark: lipgloss.Color("#0b1b3a")}).
			Background(theme.AdaptiveColor{Light: lipgloss.Color("#1d4ed8"), Dark: lipgloss.Color("#93c5fd")})
	}
	return st.Reverse(true)
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

	var turnsRatio, tokensRatio, timeRatio float64
	if b.MaxTurns > 0 {
		turnsRatio = float64(v.snap.Turns) / float64(b.MaxTurns)
	}
	if b.MaxTokens > 0 {
		tokensRatio = float64(v.snap.Tokens) / float64(b.MaxTokens)
	}
	if b.MaxDuration > 0 {
		timeRatio = float64(v.snap.Elapsed) / float64(b.MaxDuration)
	}

	turns := fmt.Sprintf("t%d", v.snap.Turns)
	if b.MaxTurns > 0 {
		turns += fmt.Sprintf("/%d", b.MaxTurns)
	}
	tokens := fmt.Sprintf("%d", v.snap.Tokens)
	if b.MaxTokens > 0 {
		tokens += fmt.Sprintf("/%d", b.MaxTokens)
	}
	var timeStr string
	if b.MaxDuration > 0 {
		timeStr = fmt.Sprintf("%s/%s", formatDuration(v.snap.Elapsed), formatDuration(b.MaxDuration))
	}

	var bar string
	var maxRatio float64 = -1
	if b.MaxTurns > 0 && turnsRatio > maxRatio {
		maxRatio = turnsRatio
	}
	if b.MaxTokens > 0 && tokensRatio > maxRatio {
		maxRatio = tokensRatio
	}
	if b.MaxDuration > 0 && timeRatio > maxRatio {
		maxRatio = timeRatio
	}
	if i == m.sel && maxRatio >= 0 {
		bar = microBar(maxRatio)
	}

	st := lipgloss.NewStyle()
	if v.hasSnap {
		switch v.snap.State {
		case orch.Running:
			st = st.Foreground(lipgloss.Color("10"))
		case orch.Paused:
			st = st.Foreground(theme.AdaptiveColor{
				Light: lipgloss.Color("#b45309"),
				Dark:  lipgloss.Color("#fbbf24"),
			})
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

	turnsSt := budgetStyle(st, turnsRatio)
	tokensSt := budgetStyle(st, tokensRatio)
	timeSt := budgetStyle(st, timeRatio)
	barSt := budgetStyle(st, maxRatio)

	var bldr strings.Builder

	if m.width < 50 {
		badgeText, badgeSt := stateBadge(v.snap.State, v.hasSnap)
		if i == m.sel {
			badgeSt = selectedBadge(badgeSt, v.hasSnap && v.snap.State == orch.Idle)
		}

		nameW := 8
		if m.width < 34 {
			nameW = 6
		}
		if m.width < 24 {
			nameW = max(m.width-12, 3)
		}
		name := truncate(v.name, nameW)

		hasAlert := m.alert.id != 0 && v.id == m.alert.id
		alertStr := ""
		if hasAlert {
			if m.width >= 35 {
				alertStr = " ⚠ " + cmp.Or(m.alert.label, "choque")
			} else {
				alertStr = " ⚠"
			}
		}

		prefix := st.Render(fmt.Sprintf("%s%-*s ", marker, nameW, name)) + badgeSt.Render(badgeText)
		bldr.WriteString(prefix)

		avail := m.width - lipgloss.Width(prefix) - lipgloss.Width(alertStr)

		turnsPart := ""
		if avail >= 4 {
			turnsPart = " " + turnsSt.Render(fmt.Sprintf("t%d", v.snap.Turns))
			if b.MaxTurns > 0 && avail >= 8 {
				turnsPart = " " + turnsSt.Render(fmt.Sprintf("t%d/%d", v.snap.Turns, b.MaxTurns))
			}
			avail -= lipgloss.Width(turnsPart)
		}

		// El uso (tokens) importa más que el dinero: con suscripción el costo
		// es 0 o no se reporta, y la cuota se gasta en tokens.
		costPart := ""
		if tok := compactTokens(v.snap.Tokens) + " tok"; avail >= lipgloss.Width(tok)+1 {
			costPart = " " + st.Render(tok)
			avail -= lipgloss.Width(costPart)
		}

		barPart := ""
		if bar != "" && avail >= lipgloss.Width(bar)+1 {
			barPart = " " + barSt.Render(bar)
			avail -= lipgloss.Width(barPart)
		}

		bldr.WriteString(turnsPart)
		bldr.WriteString(costPart)
		bldr.WriteString(barPart)
		if alertStr != "" {
			bldr.WriteString(st.Render(alertStr))
		}

		return lipgloss.NewStyle().MaxWidth(max(m.width, 1)).Render(bldr.String())
	}

	bldr.WriteString(st.Render(fmt.Sprintf("%s%-10s %-15s ", marker, truncate(v.name, 10), v.stateWord())))
	bldr.WriteString(turnsSt.Render(turns))
	bldr.WriteString(st.Render(" · "))
	bldr.WriteString(tokensSt.Render(tokens + " tok"))
	if timeStr != "" {
		bldr.WriteString(st.Render(" · "))
		bldr.WriteString(timeSt.Render(timeStr))
	}
	if bar != "" {
		bldr.WriteString(st.Render(" "))
		bldr.WriteString(barSt.Render(bar))
	}
	if v.snap.CostUSD > 0 {
		bldr.WriteString(st.Render(fmt.Sprintf(" · US$%.4f", v.snap.CostUSD)))
	}
	if m.alert.id != 0 && v.id == m.alert.id {
		bldr.WriteString(st.Render(" ⚠ " + cmp.Or(m.alert.label, "choque")))
	}

	return lipgloss.NewStyle().MaxWidth(max(m.width, 1)).Render(bldr.String())
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

// renderRevertModal dibuja el diálogo modal de confirmación de reversión para el panel.
func (m Panel) renderRevertModal(w, h int) []string {
	if m.rm == nil {
		return nil
	}
	rm := m.rm
	var title, scopeStr string
	switch rm.kind {
	case revertFile:
		title = "Revertir archivo"
		scopeStr = fmt.Sprintf("Afecta: %s (t%d)", rm.path, rm.turn)
	case revertTurn:
		title = fmt.Sprintf("Revertir turno %d", rm.turn)
		scopeStr = fmt.Sprintf("Afecta (%d): %s", len(rm.files), strings.Join(rm.files, ", "))
	default:
		title = fmt.Sprintf("Revertir agente %s", rm.name)
		scopeStr = fmt.Sprintf("Afecta (%d): %s", len(rm.files), strings.Join(rm.files, ", "))
	}

	header := fmt.Sprintf("┌─ %s ", title)
	if pad := w - lipgloss.Width(header) - 1; pad > 0 {
		header += strings.Repeat("─", pad) + "┐"
	} else {
		header = truncate(header, max(w-1, 1)) + "┐"
	}

	body := fmt.Sprintf("│ %s", truncate(scopeStr, max(w-3, 0)))
	if pad := w - lipgloss.Width(body) - 1; pad > 0 {
		body += strings.Repeat(" ", pad) + "│"
	} else {
		body = truncate(body, max(w-1, 1)) + "│"
	}

	action := "│ [Enter] Confirmar  [Esc] Cancelar"
	if w < 34 {
		action = "│ [Enter] ok · [Esc] salir"
	}
	if pad := w - lipgloss.Width(action) - 1; pad > 0 {
		action += strings.Repeat(" ", pad) + "│"
	} else {
		action = truncate(action, max(w-1, 1)) + "│"
	}

	footer := "└" + strings.Repeat("─", max(w-2, 0)) + "┘"

	st := m.styles.warn
	lines := []string{
		st.MaxWidth(max(w, 1)).Render(padTo(header, w)),
		st.MaxWidth(max(w, 1)).Render(padTo(body, w)),
		st.MaxWidth(max(w, 1)).Render(padTo(action, w)),
		st.MaxWidth(max(w, 1)).Render(padTo(footer, w)),
	}

	out := make([]string, 0, max(h, len(lines)))
	out = append(out, lines...)
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	if h > 0 && len(out) > h {
		out = out[:h]
	}
	return out
}

// statusLine compone la línea de estado del agente seleccionado.
func (m Panel) statusLine() string {
	v := m.cur()
	lead := "  "
	if m.quitting || (v.hasSnap && v.snap.State == orch.Running) {
		lead = m.spin.View() + " "
	}
	if m.rm != nil && !m.DiffOpen() && m.inH == 0 {
		parts := []string{"⚠ reversión pendiente: [Enter] confirmar · [Esc] cancelar"}
		st := m.styles.warn
		return st.MaxWidth(max(m.width, 1)).Render(lead + strings.Join(parts, " · "))
	}
	// Cada parte lleva una prioridad de recorte: con poco ancho se sueltan
	// primero la ayuda, luego la sesión y el modelo, para que el uso (tokens)
	// no quede cortado a la mitad.
	type part struct {
		text string
		drop int // 0 = no se suelta; mayor = se suelta antes
	}
	parts := []part{{m.stateText(), 0}}
	if m.armedText != "" {
		parts = []part{{m.armedText, 0}}
	}
	if m.dv != nil && m.dv.v == v {
		parts = append(parts, part{fmt.Sprintf("diff %s (turno %d)", m.dv.edit.Path, m.dv.edit.Turn), 1})
	}
	if m.cfg.Task.Permissions == agent.PermissionAcceptEdits {
		parts = append(parts, part{"edita sin preguntar", 2})
	}
	if v.model != "" {
		parts = append(parts, part{v.model, 4})
	}
	if v.sessionID != "" {
		parts = append(parts, part{"sesión " + shortSession(v.sessionID), 5})
	}
	if h := m.pipelineHint(); h != "" {
		parts = append(parts, part{h, 3})
	}
	parts = append(parts, part{formatUsage(v.tokensIn, v.tokensOut, v.costUSD), 0})
	if m.dv != nil && m.dv.v == v {
		parts = append(parts, part{joinHelp(m.keys.DiffFile, m.keys.RevertFile, m.keys.RevertTurn, m.keys.Cancel), 6})
	} else {
		if v.nEdits > 0 && m.keys.DiffFile.Enabled() {
			parts = append(parts, part{joinHelp(m.keys.DiffFile), 6})
		}
		parts = append(parts, part{m.keys.panelHelpLine(), 7})
	}
	join := func(ps []part) string {
		texts := make([]string, 0, len(ps))
		for _, p := range ps {
			if p.text != "" {
				texts = append(texts, p.text)
			}
		}
		return lead + strings.Join(texts, " · ")
	}
	for lipgloss.Width(join(parts)) > m.width {
		worst := -1
		for i, p := range parts {
			if p.drop > 0 && (worst < 0 || p.drop > parts[worst].drop) {
				worst = i
			}
		}
		if worst < 0 {
			break
		}
		parts = append(parts[:worst], parts[worst+1:]...)
	}
	st := m.styles.status
	if m.conflictActive && m.alert.id != 0 && v.id == m.alert.id {
		msg := "⚠ choque: [1] ceder archivo (reanudar) · [2] detener agente · [esc] cancelar"
		if m.width < 75 {
			msg = "⚠ [1] ceder archivo (reanudar) · [2] detener"
		}
		if m.width < 45 {
			msg = "⚠ [1] ceder · [2] detener"
		}
		parts = []part{{msg, 0}}
		st = m.styles.warn
	} else if !m.conflictActive && m.alert.id != 0 && v.id == m.alert.id {
		parts = append(parts, part{"⚠ choque: [f6 resolver]", 0})
	}
	return st.MaxWidth(max(m.width, 1)).Render(join(parts))
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
	if m.panelModal() {
		rows = append(rows, m.renderRevertModal(m.width, m.inH)...)
	} else {
		rows = append(rows, m.input.View())
	}
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
	if m.blurred || m.panelModal() {
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

// Writing indica si el panel tiene el foco y su entrada de texto está activa.
func (m Panel) Writing() bool { return !m.blurred }

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

// closeDiff cierra el diff abierto.
func (m *Panel) closeDiff() { m.dv = nil }

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
		return m.diffAgainstGit(v)
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
	m.dv = &diffView{v: v, edit: ed, numW: 1, w: m.diffW, h: m.diffH}
	sup, id := m.sup, v.id
	return m, func() tea.Msg {
		old, cur, err := sup.Diff(id, ed.Turn, ed.Path)
		return diffMsg{id: id, edit: ed, old: old, cur: cur, err: err}
	}
}

// diffAgainstGit muestra la última edición sin checkpoint del agente (codex,
// opencode, un comando de shell) contra su versión en git HEAD. No se puede
// revertir, pero al menos se ve qué cambió.
func (m Panel) diffAgainstGit(v *agentView) (Panel, tea.Cmd) {
	var last *orch.Edit
	if m.sup != nil && v.id != 0 {
		eds := m.sup.Edits(v.id)
		if n := len(eds); n > 0 {
			last = &eds[n-1]
		}
	}
	if last == nil {
		v.add(kindInfo, "no hay ediciones que mostrar")
		m.refresh()
		return m, nil
	}
	ed := *last
	m.dv = &diffView{v: v, edit: ed, numW: 1, w: m.diffW, h: m.diffH}
	sup, id := m.sup, v.id
	return m, func() tea.Msg {
		old, cur, err := sup.GitDiff(context.Background(), id, ed.Path)
		return diffMsg{id: id, edit: ed, old: old, cur: cur, err: err, git: true}
	}
}

// revert inicia el diálogo interactivo de confirmación para revertir el archivo,
// el turno o todo el agente. Muestra los archivos afectados y requiere [Enter]
// para confirmar o [Esc] para cancelar (no volátil).
func (m Panel) revert(kind string) (Panel, tea.Cmd) {
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

	var files []string
	switch kind {
	case revertFile:
		files = []string{ed.Path}
	case revertTurn:
		seen := make(map[string]bool)
		for _, e := range eds {
			if e.Turn == ed.Turn && !seen[e.Path] {
				seen[e.Path] = true
				files = append(files, e.Path)
			}
		}
	default: // revertAgent
		seen := make(map[string]bool)
		for _, e := range eds {
			if !seen[e.Path] {
				seen[e.Path] = true
				files = append(files, e.Path)
			}
		}
	}

	m.rm = &revertModal{
		kind:    kind,
		turn:    ed.Turn,
		path:    ed.Path,
		files:   files,
		agentID: v.id,
		name:    v.name,
	}

	var what, short string
	switch kind {
	case revertFile:
		what = fmt.Sprintf("revertir %s al checkpoint del turno %d", ed.Path, ed.Turn)
		short = "revertir " + ed.Path
	case revertTurn:
		what = fmt.Sprintf("revertir los archivos que editó %s en el turno %d", v.name, ed.Turn)
		short = fmt.Sprintf("revertir el turno %d", ed.Turn)
	default:
		what = fmt.Sprintf("revertir todo lo que editó %s", v.name)
		short = "revertir todo el agente"
	}

	m.armed = kind
	m.armedText = fmt.Sprintf("¿%s? [Enter] confirmar · [Esc] cancelar", what)
	m.armedShort = fmt.Sprintf("¿%s? [Enter]/[Esc]", short)
	m.refresh()
	return m, nil
}

// panelModal indica si el cuadro de confirmación de reversión se dibuja en el
// panel de agentes. Con el diff abierto en el editor lo dibuja la vista del
// diff: dos cuadros a la vez, más el aviso en la línea de estado y en el
// historial, eran cuatro avisos para una sola acción.
func (m Panel) panelModal() bool { return m.rm != nil && !m.DiffOpen() }

// confirmRevert ejecuta la reversión confirmada por el usuario con Enter.
func (m Panel) confirmRevert() (Panel, tea.Cmd) {
	if m.rm == nil {
		return m, nil
	}
	rm := m.rm
	m.rm = nil
	m.armed, m.armedText, m.armedShort = "", "", ""
	m.dv = nil

	v := m.cur()
	sup, id := m.sup, rm.agentID

	var what string
	switch rm.kind {
	case revertFile:
		what = fmt.Sprintf("revertir %s al checkpoint del turno %d", rm.path, rm.turn)
	case revertTurn:
		what = fmt.Sprintf("revertir los archivos que editó %s en el turno %d", rm.name, rm.turn)
	default:
		what = fmt.Sprintf("revertir todo lo que editó %s", rm.name)
	}

	if sup == nil {
		v.add(kindInfo, "sin supervisor: no se puede revertir")
		m.refresh()
		return m, nil
	}

	return m, func() tea.Msg {
		var err error
		switch rm.kind {
		case revertFile:
			err = sup.RevertFile(id, rm.turn, rm.path)
		case revertTurn:
			err = sup.RevertTurn(id, rm.turn)
		default:
			err = sup.RevertAgent(id)
		}
		return revertMsg{id: id, what: what, err: err}
	}
}

// cancelRevert cancela el diálogo modal de confirmación de reversión.
func (m Panel) cancelRevert() Panel {
	if m.rm != nil {
		m.rm = nil
		m.armed, m.armedText, m.armedShort = "", "", ""
		m.refresh()
	}
	return m
}
