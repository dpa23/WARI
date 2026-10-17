package layout

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/tui/filetree"
)

// maxFiles limita los archivos que se listan en la paleta.
const maxFiles = 20000

// maxShown es el máximo de resultados visibles en la paleta.
const maxShown = 8

// Identificadores de las acciones de la paleta; los archivos usan
// "open:<ruta relativa>".
const (
	cmdSave         = "save"
	cmdToggleTree   = "toggle_tree"
	cmdToggleAgents = "toggle_agents"
	cmdNewSession   = "new_session"
	cmdQuit         = "quit"
	cmdFocusTree    = "focus_tree"
	cmdFocusEditor  = "focus_editor"
	cmdFocusChat    = "focus_chat"
	cmdOpenPrefix   = "open:"
)

// item es una entrada de la paleta.
type item struct {
	id     string
	title  string
	hint   string // atajo o etiqueta, a la derecha
	folded []rune // title plegado con fold, calculado una vez
}

func newItem(id, title, hint string) item {
	return item{id: id, title: title, hint: hint, folded: fold(title)}
}

// palette es el estado de la paleta de comandos.
type palette struct {
	open    bool
	query   []rune
	items   []item
	results []int // índices en items, ya filtrados y ordenados
	sel     int
	top     int
}

// palKeys son las teclas propias de la edición de la paleta; las demás
// (cancelar, aceptar, paginar) salen de la configuración.
var palKeys = struct{ up, down, back key.Binding }{
	up:   key.NewBinding(key.WithKeys("up")),
	down: key.NewBinding(key.WithKeys("down")),
	back: key.NewBinding(key.WithKeys("backspace")),
}

// paletteItems arma las entradas de la paleta: las acciones y los archivos
// de la última lista conocida.
func (m Model) paletteItems() []item {
	hint := func(action string) string { return m.firstKey(action) }
	items := []item{
		newItem(cmdSave, "Guardar archivo", hint("save")),
		newItem(cmdToggleTree, "Alternar árbol de archivos", hint("toggle_tree")),
		newItem(cmdToggleAgents, "Alternar panel de agentes", hint("toggle_agents")),
		newItem(cmdNewSession, "Nueva sesión de agente", hint("new_session")),
		newItem(cmdFocusTree, "Ir al árbol de archivos", ""),
		newItem(cmdFocusEditor, "Ir al editor", ""),
		newItem(cmdFocusChat, "Ir al panel de agentes", ""),
		newItem(cmdQuit, "Salir", hint("quit")),
	}
	for _, f := range m.files {
		items = append(items, newItem(cmdOpenPrefix+f, f, "archivo"))
	}
	return items
}

// openPalette abre la paleta al instante con las acciones y los archivos que
// ya se conocen, y devuelve un Cmd que recorre el proyecto en segundo plano;
// su resultado (filesMsg) actualiza la lista sin bloquear Update.
func (m *Model) openPalette() tea.Cmd {
	m.pal = palette{open: true}
	m.pal.setItems(m.paletteItems())
	root := m.root
	return func() tea.Msg {
		return filesMsg{files: filetree.ListFiles(root, maxFiles)}
	}
}

// setItems reemplaza las entradas y vuelve a filtrar con la consulta actual.
func (p *palette) setItems(items []item) {
	p.items = items
	p.refilter()
}

// refilter recalcula los resultados para la consulta actual.
func (p *palette) refilter() {
	q := fold(string(p.query))
	type scored struct{ idx, score int }
	var res []scored
	for i, it := range p.items {
		if s, ok := fuzzyScoreFolded(q, it.folded); ok {
			res = append(res, scored{i, s})
		}
	}
	if len(q) > 0 {
		sort.SliceStable(res, func(a, b int) bool { return res[a].score > res[b].score })
	}
	p.results = p.results[:0]
	for _, r := range res {
		p.results = append(p.results, r.idx)
	}
	p.sel, p.top = 0, 0
}

// move mueve la selección delta posiciones, sin salirse de la lista.
func (p *palette) move(delta int) {
	if len(p.results) == 0 {
		return
	}
	p.sel = max(0, min(p.sel+delta, len(p.results)-1))
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+maxShown {
		p.top = p.sel - maxShown + 1
	}
}

// paletteKey procesa una tecla con la paleta abierta.
func (m Model) paletteKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	p := &m.pal
	switch {
	case m.is(k, "cancel"), m.is(k, "palette"):
		p.open = false
		m.quitArmed = false
	case m.is(k, "send"):
		if len(p.results) == 0 {
			return m, nil
		}
		id := p.items[p.results[p.sel]].id
		p.open = false
		return m.runCommand(id)
	case key.Matches(k, palKeys.up):
		p.move(-1)
	case key.Matches(k, palKeys.down):
		p.move(1)
	case m.is(k, "page_up"):
		p.move(-maxShown)
	case m.is(k, "page_down"):
		p.move(maxShown)
	case key.Matches(k, palKeys.back):
		if n := len(p.query); n > 0 {
			p.query = p.query[:n-1]
			p.refilter()
		}
	default:
		// Texto imprimible con cualquier modificador: en Windows, AltGr
		// llega como Text con ctrl+alt ("@", "{", "[").
		if k.Text != "" {
			p.addText(k.Text)
		}
	}
	return m, nil
}

// addText agrega texto imprimible (una sola línea) a la consulta.
func (p *palette) addText(s string) {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	changed := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		p.query = append(p.query, r)
		changed = true
	}
	if changed {
		p.refilter()
	}
}

// runCommand ejecuta una acción de la paleta.
func (m Model) runCommand(id string) (Model, tea.Cmd) {
	armed := m.quitArmed
	m.quitArmed = false
	switch {
	case id == cmdSave:
		return m.save()
	case id == cmdToggleTree:
		m.treeOn = !m.treeOn
		m.relayout()
	case id == cmdToggleAgents:
		m.chatOn = !m.chatOn
		m.relayout()
	case id == cmdNewSession:
		var cmd tea.Cmd
		m.chat, cmd = m.chat.NewSession()
		return m, cmd
	case id == cmdQuit:
		return m.requestQuit(armed)
	case id == cmdFocusTree:
		return m.focusPanel(focusTree), nil
	case id == cmdFocusEditor:
		return m.focusPanel(focusEditor), nil
	case id == cmdFocusChat:
		return m.focusPanel(focusChat), nil
	case strings.HasPrefix(id, cmdOpenPrefix):
		m.openFile(filepath.Join(m.root, filepath.FromSlash(strings.TrimPrefix(id, cmdOpenPrefix))))
	}
	return m, nil
}

// view dibuja la paleta como una franja de n líneas de w celdas que cubre la
// parte superior de la pantalla. Devuelve también la altura máxima disponible
// ya aplicada.
func (p palette) view(w, maxH int) []string {
	bar := lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("252"))
	selSt := lipgloss.NewStyle().Reverse(true)

	lines := []string{bar.Bold(true).Render(fit("> "+string(p.query), w))}
	shown := min(maxShown, maxH-1)
	if len(p.results) == 0 && shown > 0 {
		lines = append(lines, bar.Faint(true).Render(fit("  sin coincidencias", w)))
	}
	for i := 0; i < shown && p.top+i < len(p.results); i++ {
		idx := p.top + i
		it := p.items[p.results[idx]]
		text := "  " + it.title
		if it.hint != "" {
			if gap := w - lipgloss.Width(text) - lipgloss.Width(it.hint) - 2; gap >= 2 {
				text += strings.Repeat(" ", gap) + it.hint
			}
		}
		text = fit(text, w)
		if idx == p.sel {
			lines = append(lines, selSt.Render(text))
		} else {
			lines = append(lines, bar.Render(text))
		}
	}
	if len(lines) > maxH {
		lines = lines[:maxH]
	}
	return lines
}
