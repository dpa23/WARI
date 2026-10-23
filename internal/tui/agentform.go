package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
)

// Formulario de agente nuevo. Las CLIs conservan el último modelo que se usó,
// así que lanzar un agente sin elegir podía gastar un modelo caro por
// accidente: el formulario obliga a elegir el modelo y WARI lo manda siempre
// explícito. También elige la potencia (esfuerzo), los permisos y el rol.

const (
	roleWorker       = ""
	roleOrchestrator = "orq"
)

// orchestratorPreamble encabeza el primer mensaje de un agente orquestador. La
// skill vive en .agents/ del proyecto, donde la leen todas las CLIs.
const orchestratorPreamble = "Eres el agente orquestador de WARI. Antes de actuar lee " +
	"`.agents/skills/wari-orquestador/SKILL.md` y sigue ese procedimiento.\n\nTarea del usuario:\n"

const (
	fieldCLI = iota
	fieldModel
	fieldEffort
	fieldPerm
	fieldRole
	fieldMode
	numFields
)

var (
	permLabels = []string{"Preguntar (la CLI sin pantalla niega las ediciones)", "Editar sin preguntar", "Solo planear y leer"}
	permModes  = []agent.PermissionMode{agent.PermissionAsk, agent.PermissionAcceptEdits, agent.PermissionPlan}
	roleLabels = []string{"Trabajador", "Orquestador (planifica y reparte; en modo planear)"}
	modeLabels = []string{"Terminal: la CLI real, con todo lo suyo", "Chat de WARI sin pantalla (solo eventos)"}
)

type agentForm struct {
	v       *agentView
	created bool   // la vista se creó para este formulario: Esc la quita
	pending string // mensaje que se envía al confirmar (el que lo disparó)
	infos   []agent.Info

	cli    int
	model  int // -1: sin elegir; len(Models): el escrito a mano
	custom []rune
	effort int // 0: el de la CLI; i>0: Efforts[i-1]
	perm   int
	role   int
	mode   int // 0 terminal, 1 chat sin pantalla
	focus  int
	err    string
}

func (f *agentForm) info() agent.Info { return f.infos[f.cli] }

// customSlot es el índice del modelo escrito a mano, o -1 si la CLI no admite.
func (f *agentForm) customSlot() int {
	if f.info().ModelHint == "" && len(f.info().Models) > 0 {
		return -1
	}
	return len(f.info().Models)
}

func (f *agentForm) modelCount() int {
	n := len(f.info().Models)
	if f.customSlot() >= 0 {
		n++
	}
	return n
}

func (f *agentForm) customActive() bool { return f.customSlot() >= 0 && f.model == f.customSlot() }

// resetModel vuelve a "sin elegir" al cambiar de CLI.
func (f *agentForm) resetModel() {
	f.model, f.custom, f.effort = -1, nil, 0
	if len(f.info().Models) == 0 {
		f.model = 0 // solo se puede escribir
	}
}

func (f *agentForm) shift(d int) {
	cycle := func(i, n int) int { return ((i+d)%n + n) % n }
	switch f.focus {
	case fieldCLI:
		f.cli = cycle(f.cli, len(f.infos))
		f.resetModel()
	case fieldModel:
		n := f.modelCount()
		switch {
		case f.model < 0 && d > 0:
			f.model = 0
		case f.model < 0:
			f.model = n - 1
		default:
			f.model = cycle(f.model, n)
		}
	case fieldEffort:
		if n := len(f.info().Efforts); n > 0 {
			f.effort = cycle(f.effort, n+1)
		}
	case fieldPerm:
		if f.role == 0 { // un orquestador solo planea
			f.perm = cycle(f.perm, len(permModes))
		}
	case fieldRole:
		f.role = cycle(f.role, len(roleLabels))
	case fieldMode:
		if f.info().Launch != nil {
			f.mode = cycle(f.mode, len(modeLabels))
		}
	}
}

// terminalMode indica que el agente se lanza como CLI real en un terminal: es
// el modo por omisión y solo existe si el adaptador sabe armar la línea.
func (f *agentForm) terminalMode() bool { return f.mode == 0 && f.info().Launch != nil }

// chosenModel devuelve el modelo elegido o "" si falta.
func (f *agentForm) chosenModel() string {
	switch {
	case f.model < 0:
		return ""
	case f.customActive():
		return strings.TrimSpace(string(f.custom))
	}
	return f.info().Models[f.model].ID
}

// openForm abre el formulario para v. created indica que la vista es nueva (Esc
// la descarta); pending es el mensaje que se envía al confirmar.
func (m Panel) openForm(v *agentView, pending string, created bool) (Panel, tea.Cmd) {
	names := make([]string, 0, len(m.cfg.Infos))
	for n := range m.cfg.Infos {
		names = append(names, n)
	}
	sort.Strings(names)
	f := &agentForm{v: v, created: created, pending: pending, focus: fieldModel}
	for i, n := range names {
		info := m.cfg.Infos[n]
		if opts := m.discovered[n]; len(opts) > 0 {
			info.Models = opts
		}
		f.infos = append(f.infos, info)
		if n == v.cli {
			f.cli = i
		}
	}
	f.resetModel()
	for i, mode := range permModes {
		if mode == m.effectivePermissions(v) {
			f.perm = i
		}
	}
	m.form = f
	return m, m.discoverCmd(f.info())
}

// discoverCmd consulta los modelos de la CLI si sabe listarlos y aún no se
// consultaron. Los resultados llegan como modelsMsg.
func (m Panel) discoverCmd(info agent.Info) tea.Cmd {
	if info.Discover == nil || info.Missing || m.discovering[info.Name] || len(m.discovered[info.Name]) > 0 {
		return nil
	}
	m.discovering[info.Name] = true
	discover, name := info.Discover, info.Name
	return func() tea.Msg {
		opts, err := discover(context.Background())
		return modelsMsg{name: name, opts: opts, err: err}
	}
}

// onModels guarda los modelos que dijo una CLI y, con el formulario abierto,
// los pone en la lista conservando el modelo ya elegido si sigue existiendo.
func (m Panel) onModels(msg modelsMsg) Panel {
	delete(m.discovering, msg.name)
	if msg.err != nil || len(msg.opts) == 0 {
		reason := "sin modelos"
		if msg.err != nil {
			reason = msg.err.Error()
		}
		m.discoverErr[msg.name] = reason
		return m
	}
	delete(m.discoverErr, msg.name)
	m.discovered[msg.name] = msg.opts
	if f := m.form; f != nil {
		for i := range f.infos {
			if f.infos[i].Name != msg.name {
				continue
			}
			prev := ""
			if i == f.cli && f.model >= 0 && f.model < len(f.infos[i].Models) {
				prev = f.infos[i].Models[f.model].ID
			}
			f.infos[i].Models = msg.opts
			if i == f.cli && prev != "" {
				f.model = -1
				for j, o := range msg.opts {
					if o.ID == prev {
						f.model = j
					}
				}
			}
		}
	}
	return m
}

// formKey atiende una tecla con el formulario abierto.
func (m Panel) formKey(msg tea.KeyPressMsg) (Panel, tea.Cmd) {
	f := m.form
	f.err = ""
	switch s := msg.String(); s {
	case "esc":
		return m.formCancel(), nil
	case "enter":
		return m.formConfirm()
	case "up", "shift+tab":
		f.focus = (f.focus + numFields - 1) % numFields
	case "down", "tab":
		f.focus = (f.focus + 1) % numFields
	case "left", "right":
		before := f.cli
		if s == "left" {
			f.shift(-1)
		} else {
			f.shift(1)
		}
		if f.focus == fieldCLI && f.cli != before {
			if opts := m.discovered[f.info().Name]; len(opts) > 0 {
				f.infos[f.cli].Models = opts
				f.resetModel()
			}
			return m, m.discoverCmd(f.info())
		}
	case "backspace":
		if f.focus == fieldModel && f.customActive() && len(f.custom) > 0 {
			f.custom = f.custom[:len(f.custom)-1]
		}
	default:
		if f.focus == fieldModel && f.customActive() && msg.Text != "" {
			f.custom = append(f.custom, []rune(msg.Text)...)
		}
	}
	return m, nil
}

// formCancel cierra el formulario sin crear nada: la vista nueva se quita y el
// mensaje pendiente vuelve a la entrada.
func (m Panel) formCancel() Panel {
	f := m.form
	m.form = nil
	if f.created && len(m.views) > 1 {
		m.views = m.views[:len(m.views)-1]
		m.created--
		m.sel = len(m.views) - 1
		m.layout(m.width, m.height)
		return m
	}
	m.input.SetValue(f.pending)
	m.refresh()
	return m
}

// formConfirm aplica la configuración al agente y, si había un mensaje
// pendiente, lo envía.
func (m Panel) formConfirm() (Panel, tea.Cmd) {
	f := m.form
	model := f.chosenModel()
	if model == "" {
		f.err = "elige un modelo: WARI nunca usa el que la CLI recuerde del último uso"
		f.focus = fieldModel
		return m, nil
	}
	info := f.info()
	if info.Missing {
		f.err = info.Name + " no está en el PATH: instálala o elige otra CLI"
		f.focus = fieldCLI
		return m, nil
	}
	v := f.v
	if factory := m.cfg.Agents[info.Name]; factory != nil && (v.cli != info.Name || v.ag == nil) {
		v.ag = factory()
	}
	v.cli, v.taskModel, v.configured = info.Name, model, true
	v.effort = ""
	if f.effort > 0 {
		v.effort = info.Efforts[f.effort-1]
	}
	mode := permModes[f.perm]
	v.perm = &mode
	v.role = roleWorker
	if f.role == 1 {
		v.role = roleOrchestrator
	}
	pending := f.pending
	if f.terminalMode() {
		req, err := m.launchRequest(v, pending)
		if err != nil {
			f.err = "no se pudo lanzar " + info.Name + ": " + err.Error()
			return m, nil
		}
		// El terminal no vive en el panel: el layout lo coloca en su tile. La
		// vista que creó el formulario se quita; la primera se queda sin
		// configurar para el siguiente mensaje.
		m.form = nil
		if f.created && len(m.views) > 1 {
			m.views = m.views[:len(m.views)-1]
			m.created--
			m.sel = len(m.views) - 1
		} else {
			v.configured = false
		}
		m.layout(m.width, m.height)
		return m, func() tea.Msg { return req }
	}
	v.add(kindInfo, v.configSummary(m.effectivePermissions(v)))
	m.form = nil
	m.layout(m.width, m.height)
	if pending != "" {
		m.input.SetValue(pending)
		return m.send()
	}
	m.refresh()
	return m, nil
}

// cliLabel es el nombre de la CLI con su disponibilidad en el PATH.
func cliLabel(info agent.Info) string {
	if info.Missing {
		return "✗ " + info.Name + " (no instalado)"
	}
	return "✓ " + info.Name
}

// configSummary es la línea que deja el formulario en el historial del agente.
func (v *agentView) configSummary(p agent.PermissionMode) string {
	parts := []string{v.cli, v.taskModel}
	if v.effort != "" {
		parts = append(parts, "potencia "+v.effort)
	}
	switch {
	case v.role == roleOrchestrator:
		parts = append(parts, "orquestador (modo planear)")
	case p == agent.PermissionAcceptEdits:
		parts = append(parts, "edita sin preguntar")
	case p == agent.PermissionPlan:
		parts = append(parts, "solo planea")
	default:
		parts = append(parts, "pregunta")
	}
	return "— " + strings.Join(parts, " · ") + " —"
}

// formRows dibuja el formulario en exactamente h filas de ancho w.
func (m Panel) formRows(w, h int) []string {
	f := m.form
	info := f.info()
	opt := func(focus bool, text string) string {
		if focus {
			return "◂ " + text + " ▸"
		}
		return "  " + text
	}
	model := "— elige un modelo —"
	if m.discovering[info.Name] {
		model = "— buscando los modelos de " + info.Name + "… —"
	}
	switch {
	case f.customActive():
		model = "otro: " + string(f.custom)
		if f.focus == fieldModel {
			model += "▏"
		}
		if info.ModelHint != "" {
			model += "  (" + info.ModelHint + ")"
		}
	case f.model >= 0 && f.model < len(info.Models):
		o := info.Models[f.model]
		model = o.Label
		if o.Note != "" {
			model += " · " + o.Note
		}
	}
	if e := m.discoverErr[info.Name]; e != "" && !m.discovering[info.Name] {
		model += "  [lista de respaldo]"
	}
	effort := "predeterminada de la CLI"
	switch {
	case len(info.Efforts) == 0:
		effort = "— esta CLI no lo admite"
	case f.effort > 0:
		effort = info.Efforts[f.effort-1]
	}
	perm := permLabels[f.perm]
	if f.perm == 0 && f.terminalMode() {
		perm = "Preguntar (la CLI te pregunta en su pantalla)"
	}
	if f.role == 1 {
		perm = permLabels[2] + " (orquestador)"
	}
	modeText := modeLabels[f.mode]
	if info.Launch == nil {
		modeText = "Chat de WARI (esta CLI no tiene modo terminal)"
	}
	type row struct {
		label, value string
		field        int
	}
	fields := []row{
		{"CLI", cliLabel(info), fieldCLI},
		{"Modelo", model, fieldModel},
		{"Potencia", effort, fieldEffort},
		{"Permisos", perm, fieldPerm},
		{"Rol", roleLabels[f.role], fieldRole},
		{"Modo", modeText, fieldMode},
	}
	note := ""
	switch {
	case f.role == 1 || permModes[f.perm] == agent.PermissionPlan:
		note = info.PlanNote
	case permModes[f.perm] == agent.PermissionAcceptEdits:
		note = info.EditNote
	}
	lines := []string{"Agente nuevo: elige CLI, modelo y potencia"}
	for _, r := range fields {
		mark := "  "
		if f.focus == r.field {
			mark = "▸ "
		}
		line := fmt.Sprintf("%s%-9s %s", mark, r.label, opt(f.focus == r.field, r.value))
		if f.focus == r.field {
			line = m.styles.status.Reverse(true).Render(truncate(line, w))
		} else {
			line = truncate(line, w)
		}
		lines = append(lines, line)
	}
	if f.terminalMode() && info.LaunchNote != "" && f.err == "" && note == "" {
		note = info.LaunchNote
	}
	if note != "" && f.err == "" {
		lines = append(lines, m.styles.warn.Render(truncate("⚠ "+note, w)))
	}
	if f.err != "" {
		lines = append(lines, m.styles.warn.Render(truncate("⚠ "+f.err, w)))
	} else {
		lines = append(lines, truncate("↑↓ campo · ←→ cambiar · Enter crear · Esc cancelar", w))
	}
	if h < len(lines) { // sin sitio: fuera el título
		lines = lines[1:]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	out := make([]string, 0, h)
	for _, l := range lines[:min(h, len(lines))] {
		out = append(out, padTo(l, w))
	}
	return out
}

// FormActive indica que el formulario de agente nuevo está abierto: el layout
// lo dibuja superpuesto (el panel puede estar oculto) y le entrega las teclas.
func (m Panel) FormActive() bool { return m.form != nil }

// FormLines dibuja el formulario en exactamente h filas de ancho w.
func (m Panel) FormLines(w, h int) []string { return m.formRows(w, h) }
