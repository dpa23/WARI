package tui

import (
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
	numFields
)

var (
	permLabels = []string{"Preguntar (la CLI sin pantalla niega las ediciones)", "Editar sin preguntar", "Solo planear y leer"}
	permModes  = []agent.PermissionMode{agent.PermissionAsk, agent.PermissionAcceptEdits, agent.PermissionPlan}
	roleLabels = []string{"Trabajador", "Orquestador (planifica y reparte; en modo planear)"}
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
	}
}

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
func (m Panel) openForm(v *agentView, pending string, created bool) Panel {
	names := make([]string, 0, len(m.cfg.Infos))
	for n := range m.cfg.Infos {
		names = append(names, n)
	}
	sort.Strings(names)
	f := &agentForm{v: v, created: created, pending: pending, focus: fieldModel}
	for i, n := range names {
		f.infos = append(f.infos, m.cfg.Infos[n])
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
	case "left":
		f.shift(-1)
	case "right":
		f.shift(1)
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
	v.add(kindInfo, v.configSummary(m.effectivePermissions(v)))
	pending := f.pending
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
	switch {
	case f.customActive():
		model = "otro: " + string(f.custom)
		if f.focus == fieldModel {
			model += "▏"
		}
		if info.ModelHint != "" {
			model += "  (" + info.ModelHint + ")"
		}
	case f.model >= 0:
		o := info.Models[f.model]
		model = o.Label
		if o.Note != "" {
			model += " · " + o.Note
		}
	}
	effort := "predeterminada de la CLI"
	switch {
	case len(info.Efforts) == 0:
		effort = "— esta CLI no lo admite"
	case f.effort > 0:
		effort = info.Efforts[f.effort-1]
	}
	perm := permLabels[f.perm]
	if f.role == 1 {
		perm = permLabels[2] + " (orquestador)"
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
		line := fmt.Sprintf("%-9s %s", r.label, opt(f.focus == r.field, r.value))
		if f.focus == r.field {
			line = m.styles.status.Reverse(true).Render(truncate(line, w))
		} else {
			line = truncate(line, w)
		}
		lines = append(lines, line)
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
