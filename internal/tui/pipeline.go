package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/orch/pipeline"
)

// Pipeline y gates en el panel (fase 7). El panel muestra los pasos con su
// estado sobre la lista de agentes; cuando el pipeline espera aprobación, el
// historial se sustituye por el gate: el resultado del paso, los archivos que
// editó con su diff contra el checkpoint y el prompt del siguiente. Esc lo
// oculta (pipeline_run lo vuelve a mostrar); aprobar, rechazar y editar el
// prompt valen con el gate oculto. Los agentes que lanza el pipeline aparecen
// en la lista solos (adopt).

const (
	// maxPipeRows es el máximo de filas del pipeline (cabecera y pasos); con
	// más pasos, o sin espacio, se muestra una sola línea compacta.
	maxPipeRows = 7
	// maxGateResultLines y maxGatePromptLines recortan lo largo del resumen.
	maxGateResultLines = 20
	maxGatePromptLines = 10
)

type (
	// pipeMsg avisa de que el pipeline cambió (Runner.Changed); closed, de que
	// el Runner se cerró.
	pipeMsg struct{ closed bool }
	// gateDiffMsg lleva el diff de la edición idx del gate seq.
	gateDiffMsg struct {
		seq, idx int
		edit     orch.Edit
		old, cur []byte
		err      error
	}
)

// gateView es el gate abierto.
type gateView struct {
	gate   pipeline.Gate
	hidden bool
	// diffs es el texto del diff de cada edición; vacío mientras carga.
	diffs []string
}

// listenPipeline espera el siguiente aviso del pipeline. Se vuelve a emitir
// tras manejar cada uno.
func listenPipeline(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		_, ok := <-ch
		return pipeMsg{closed: !ok}
	}
}

// HasPipeline indica si el panel controla un pipeline.
func (m Panel) HasPipeline() bool { return m.pipe != nil }

// syncPipeline lee el estado del pipeline, abre o cierra el gate y deja las
// notas en el historial. Devuelve los comandos que piden los diffs del gate
// nuevo.
func (m *Panel) syncPipeline() []tea.Cmd {
	if m.pipe == nil {
		return nil
	}
	prev := m.ps
	m.ps = m.pipe.Snapshot()
	m.noteTransition(prev)

	var cmds []tea.Cmd
	newGate := false
	switch g := m.ps.Gate; {
	case g == nil:
		m.gate = nil
	case m.gate == nil || m.gate.gate.Seq != g.Seq:
		m.gate = &gateView{gate: *g, diffs: make([]string, len(g.Edits))}
		cmds = m.gateDiffCmds()
		newGate = true
	default:
		m.gate.gate = *g
	}
	if m.gateEdit && !m.canEditPrompt() {
		m.endGateEdit()
	}
	m.layout(m.width, m.height)
	if newGate {
		m.vp.GotoTop()
	}
	return cmds
}

// noteTransition deja en el historial los cambios de estado del pipeline.
func (m *Panel) noteTransition(prev pipeline.Snapshot) {
	if prev.State == m.ps.State {
		return
	}
	v := m.cur()
	if cur := m.ps.Cur; cur >= 0 && cur < len(m.ps.Steps) {
		if sv := m.byID[m.ps.Steps[cur].AgentID]; sv != nil {
			v = sv
		}
	}
	name := m.ps.Name
	if name == "" {
		name = "pipeline"
	}
	switch m.ps.State {
	case pipeline.GateWaiting:
		if g := m.ps.Gate; g != nil {
			v.add(kindWarn, fmt.Sprintf("⏸ %s: gate tras «%s»; %s", name, g.FromName, m.gateKeys()))
		}
	case pipeline.Done:
		v.add(kindInfo, "— "+name+": terminado —")
	case pipeline.Stopped:
		v.add(kindInfo, "— "+name+": detenido ("+m.ps.Reason+") —")
	case pipeline.Failed:
		v.add(kindError, name+": falló: "+m.ps.Reason)
	}
}

func (m Panel) gateKeys() string {
	return joinHelp(m.keys.GateApprove, m.keys.GateReject, m.keys.GateEdit)
}

// gateShown indica que el gate sustituye al historial.
func (m Panel) gateShown() bool { return m.gate != nil && !m.gate.hidden }

func (m *Panel) hideGate() {
	m.gate.hidden = true
	m.refreshTo(true)
}

// canEditPrompt indica que hay un paso por lanzar cuyo prompt se puede editar.
func (m Panel) canEditPrompt() bool {
	switch m.ps.State {
	case pipeline.GateWaiting, pipeline.NotStarted, pipeline.Ready:
		return true
	}
	return false
}

// pipelineOp ejecuta una operación del pipeline sin bloquear Update.
func (m Panel) pipelineOp(what string, f func(*pipeline.Runner) error) (Panel, tea.Cmd) {
	if m.gateEdit {
		m.endGateEdit()
	}
	r := m.pipe
	return m, func() tea.Msg { return opMsg{what: what, err: f(r)} }
}

// pipelineRun lanza el paso que toca; con el gate oculto, lo vuelve a mostrar.
func (m Panel) pipelineRun() (Panel, tea.Cmd) {
	if m.gate != nil && m.gate.hidden {
		m.gate.hidden = false
		m.refreshTo(false)
		m.vp.GotoTop()
		return m, nil
	}
	return m.pipelineOp("lanzar el pipeline", (*pipeline.Runner).Run)
}

// gateEditStart pone en la entrada el prompt del paso por lanzar para
// editarlo; Enter lo guarda y Esc cancela.
func (m Panel) gateEditStart() (Panel, tea.Cmd) {
	v := m.cur()
	var prompt string
	switch m.ps.State {
	case pipeline.GateWaiting:
		prompt = m.ps.Gate.NextPrompt
	case pipeline.NotStarted, pipeline.Ready:
		prompt = m.ps.Steps[m.ps.Cur].Prompt
	default:
		v.add(kindInfo, "no hay ningún paso por lanzar cuyo prompt editar")
		m.refresh()
		return m, nil
	}
	if !m.gateEdit {
		m.gateDraft = m.input.Value()
	}
	m.gateEdit = true
	m.input.Reset()
	m.input.SetValue(prompt)
	m.refresh()
	return m, nil
}

// gateEditSave guarda el prompt editado en el pipeline.
func (m Panel) gateEditSave() (Panel, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" || m.pipe == nil {
		return m, nil
	}
	m.endGateEdit()
	r := m.pipe
	return m, func() tea.Msg { return opMsg{what: "editar el prompt", err: r.EditPrompt(text)} }
}

// endGateEdit sale del modo de edición y devuelve a la entrada el borrador que
// había.
func (m *Panel) endGateEdit() {
	m.gateEdit = false
	m.input.Reset()
	m.input.SetValue(m.gateDraft)
	m.gateDraft = ""
}

// adopt crea la vista de un agente lanzado por el pipeline, a partir de su
// primera novedad. Los demás agentes desconocidos se ignoran.
func (m *Panel) adopt(u orch.Update) *agentView {
	if m.pipe == nil || u.Snapshot.Name == "" {
		return nil
	}
	var step *pipeline.StepSnapshot
	ps := m.pipe.Snapshot()
	for i := range ps.Steps {
		if st := &ps.Steps[i]; st.Sent != "" && st.AgentName == u.Snapshot.Name {
			step = st
		}
	}
	if step == nil {
		return nil
	}
	v := &agentView{
		name:       u.Snapshot.Name,
		id:         u.ID,
		follow:     true,
		transcript: transcript{newSessionKey: m.keys.AgentNew.Help().Key, root: m.cfg.Task.Dir},
	}
	v.add(kindUser, step.Sent)
	m.views = append(m.views, v)
	m.byID[u.ID] = v
	return v
}

// gateDiffCmds pide el diff de cada edición con checkpoint del gate.
func (m Panel) gateDiffCmds() []tea.Cmd {
	if m.sup == nil || m.gate == nil {
		return nil
	}
	g := m.gate.gate
	var cmds []tea.Cmd
	for i, e := range g.Edits {
		if !e.Saved {
			continue
		}
		sup, seq, idx, id, e := m.sup, g.Seq, i, g.AgentID, e
		cmds = append(cmds, func() tea.Msg {
			old, cur, err := sup.Diff(id, e.Turn, e.Path)
			return gateDiffMsg{seq: seq, idx: idx, edit: e, old: old, cur: cur, err: err}
		})
	}
	return cmds
}

func (m *Panel) applyGateDiff(msg gateDiffMsg) {
	if m.gate == nil || m.gate.gate.Seq != msg.seq || msg.idx >= len(m.gate.diffs) {
		return
	}
	if msg.err != nil {
		m.gate.diffs[msg.idx] = "no se pudo obtener el diff: " + msg.err.Error()
	} else {
		m.gate.diffs[msg.idx] = diffText(msg.edit.Path, msg.edit.Turn, msg.old, msg.cur)
	}
	m.refreshTo(false)
}

// pipelineHint es lo que la línea de estado dice del pipeline: qué hacer
// ahora.
func (m Panel) pipelineHint() string {
	if m.pipe == nil {
		return ""
	}
	if m.gateEdit {
		return fmt.Sprintf("editando el prompt: %s guarda, %s cancela",
			m.keys.Send.Help().Key, m.keys.Cancel.Help().Key)
	}
	switch m.ps.State {
	case pipeline.GateWaiting:
		return "gate: " + m.gateKeys()
	case pipeline.NotStarted, pipeline.Ready:
		return joinHelp(m.keys.PipelineRun, m.keys.GateEdit)
	}
	return ""
}

// pipelineRows decide cuántas filas ocupa el pipeline con avail disponibles:
// cabecera y un paso por fila si caben; si no, una línea compacta.
func (m Panel) pipelineRows(avail int) int {
	if m.pipe == nil || avail <= 0 {
		return 0
	}
	if n := 1 + len(m.ps.Steps); n <= maxPipeRows && n <= avail {
		return n
	}
	return 1
}

var pipeStateLabel = map[pipeline.State]string{
	pipeline.NotStarted:  "sin lanzar",
	pipeline.Running:     "en curso",
	pipeline.Ready:       "listo para lanzar el siguiente paso",
	pipeline.GateWaiting: "esperando aprobación",
	pipeline.Done:        "terminado",
	pipeline.Stopped:     "detenido",
	pipeline.Failed:      "falló",
}

// stepIcon es el ícono de un paso; gated marca al que terminó y espera el gate.
func stepIcon(s pipeline.StepState, gated bool) string {
	switch {
	case gated:
		return "⏸"
	case s == pipeline.StepRunning:
		return "▶"
	case s == pipeline.StepDone:
		return "✓"
	case s == pipeline.StepFailed:
		return "✗"
	case s == pipeline.StepReady:
		return "○"
	}
	return "·"
}

func (m Panel) stepStyle(s pipeline.StepState, gated bool) lipgloss.Style {
	st := lipgloss.NewStyle()
	switch {
	case gated:
		return st.Foreground(lipgloss.Color("11")).Bold(true)
	case s == pipeline.StepRunning:
		return st.Foreground(lipgloss.Color("10"))
	case s == pipeline.StepFailed:
		return st.Foreground(lipgloss.Color("9"))
	case s == pipeline.StepPending:
		return st.Faint(true)
	}
	return st
}

// stepDetail es el estado del paso para la fila: el del agente mientras corre
// (así se ve si está pausado) y la razón si falló.
func (m Panel) stepDetail(st pipeline.StepSnapshot) string {
	switch st.State {
	case pipeline.StepRunning:
		if v := m.byID[st.AgentID]; v != nil && v.hasSnap {
			return v.snap.State.String()
		}
		return "lanzando"
	case pipeline.StepFailed:
		return truncate(st.Reason, 40)
	}
	return st.State.String()
}

// pipeRows son las filas del pipeline.
func (m Panel) pipeRows() []string {
	if m.pipeH == 0 {
		return nil
	}
	ps := m.ps
	name := ps.Name
	if name == "" {
		name = "pipeline"
	}
	head := fmt.Sprintf("%s · %s", name, pipeStateLabel[ps.State])
	gated := func(i int) bool { return ps.State == pipeline.GateWaiting && ps.Gate != nil && ps.Gate.From == i }
	width := max(m.width, 1)

	if m.pipeH == 1 {
		chips := make([]string, len(ps.Steps))
		for i, st := range ps.Steps {
			chips[i] = stepIcon(st.State, gated(i)) + st.Name
		}
		line := head + " · " + strings.Join(chips, " ")
		if h := m.pipelineHint(); h != "" {
			line += " · " + h
		}
		return []string{lipgloss.NewStyle().Bold(true).MaxWidth(width).Render(line)}
	}

	headStyle := lipgloss.NewStyle().Bold(true)
	if ps.State == pipeline.GateWaiting {
		head += " · " + m.gateKeys()
		headStyle = headStyle.Foreground(lipgloss.Color("11"))
	}
	rows := []string{headStyle.MaxWidth(width).Render(head)}
	for i, st := range ps.Steps {
		line := fmt.Sprintf("  %s %d %-10s %-6s %s",
			stepIcon(st.State, gated(i)), i+1, truncate(st.Name, 10), st.Mode, m.stepDetail(st))
		rows = append(rows, m.stepStyle(st.State, gated(i)).MaxWidth(width).Render(line))
	}
	return rows
}

// render compone el gate: resumen, archivos con su diff y el siguiente paso.
func (g *gateView) render(st styles, keys KeyMap, width int) string {
	width = max(width, 1)
	gt := g.gate
	var b strings.Builder
	line := func(s lipgloss.Style, text string) {
		b.WriteString(s.Width(width).Render(text))
		b.WriteString("\n")
	}
	plain := lipgloss.NewStyle()

	line(st.warn, fmt.Sprintf("⏸ gate: «%s» → «%s»", gt.FromName, gt.ToName))
	line(st.info, fmt.Sprintf("resultado de «%s» (turno %d):", gt.FromName, gt.Turn))
	if gt.Result.IsError {
		line(st.err, "el turno terminó con error")
	}
	if txt := strings.TrimSpace(gt.Result.Text); txt == "" {
		line(st.info, "(sin texto)")
	} else {
		line(plain, clipLines(txt, maxGateResultLines))
	}

	b.WriteString("\n")
	if len(gt.Edits) == 0 {
		line(st.info, "archivos editados: ninguno")
	} else {
		line(st.info, fmt.Sprintf("archivos editados (%d):", len(gt.Edits)))
	}
	for i, e := range gt.Edits {
		line(st.edit, fmt.Sprintf("✎ %s (turno %d)", e.Path, e.Turn))
		switch {
		case !e.Saved:
			line(st.info, "(sin checkpoint: no hay diff)")
		case i < len(g.diffs) && g.diffs[i] != "":
			b.WriteString(renderDiff(g.diffs[i], st, width))
			b.WriteString("\n")
		default:
			line(st.info, "calculando el diff…")
		}
	}

	b.WriteString("\n")
	line(st.info, fmt.Sprintf("siguiente: «%s» (%s), prompt:", gt.ToName, gt.NextMode))
	line(plain, clipLines(gt.NextPrompt, maxGatePromptLines))

	b.WriteString("\n")
	help := joinHelp(keys.GateApprove, keys.GateReject, keys.GateEdit)
	if keys.Cancel.Enabled() {
		help += " · " + keys.Cancel.Help().Key + " ocultar"
	}
	line(st.warn, help)
	return strings.TrimRight(b.String(), "\n")
}

// clipLines deja como mucho n líneas de s.
func clipLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… (%d líneas más)", len(lines)-n)
}
