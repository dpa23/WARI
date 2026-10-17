// Package filetree es un árbol de archivos para Bubble Tea v2: carpetas
// plegables, navegación con el teclado y un mensaje al abrir un archivo.
package filetree

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// OpenFileMsg lo emite el árbol al pulsar enter sobre un archivo.
type OpenFileMsg struct{ Path string }

// KeyMap son los atajos del árbol.
type KeyMap struct {
	Up, Down         key.Binding
	PageUp, PageDown key.Binding
	First, Last      key.Binding
	// Left pliega la carpeta (o salta a la carpeta padre); Right la despliega.
	Left, Right key.Binding
	// Enter despliega o pliega una carpeta y abre un archivo.
	Enter key.Binding
}

// DefaultKeyMap devuelve los atajos predeterminados.
func DefaultKeyMap() KeyMap {
	b := func(keys ...string) key.Binding { return key.NewBinding(key.WithKeys(keys...)) }
	return KeyMap{
		Up: b("up", "k"), Down: b("down", "j"),
		PageUp: b("pgup"), PageDown: b("pgdown"),
		First: b("home"), Last: b("end"),
		Left: b("left", "h"), Right: b("right", "l"),
		Enter: b("enter"),
	}
}

type node struct {
	name     string
	path     string // absoluta
	rel      string // relativa a la raíz, con "/"
	dir      bool
	depth    int
	parent   *node
	expanded bool
	loaded   bool
	children []*node
}

// Model es el árbol. Receptor de valor; los nodos se comparten entre copias.
type Model struct {
	root *node
	ig   *ignorer
	keys KeyMap

	rows   []*node // filas visibles
	cursor int
	top    int

	w, h    int
	focused bool
}

// New crea el árbol de root con la raíz desplegada.
func New(root string) Model {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	m := Model{
		ig:   newIgnorer(abs),
		keys: DefaultKeyMap(),
		root: &node{name: filepath.Base(abs), path: abs, dir: true, expanded: true},
	}
	m.load(m.root)
	m.flatten()
	return m
}

// load lee los hijos de la carpeta n: carpetas primero, luego archivos, cada
// grupo en orden alfabético sin distinguir mayúsculas.
func (m Model) load(n *node) {
	n.loaded = true
	n.children = nil
	entries, err := os.ReadDir(n.path)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(n.path, e.Name())
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				isDir = true
			}
		}
		rel := e.Name()
		if n.rel != "" {
			rel = n.rel + "/" + e.Name()
		}
		if m.ig.ignored(rel, e.Name(), isDir) {
			continue
		}
		n.children = append(n.children, &node{
			name: e.Name(), path: p, rel: rel, dir: isDir, depth: n.depth + 1, parent: n,
		})
	}
	sort.SliceStable(n.children, func(i, j int) bool {
		a, b := n.children[i], n.children[j]
		if a.dir != b.dir {
			return a.dir
		}
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	})
}

// flatten recalcula las filas visibles y mantiene el cursor dentro.
func (m *Model) flatten() {
	m.rows = m.rows[:0]
	var walk func(n *node)
	walk = func(n *node) {
		m.rows = append(m.rows, n)
		if n.dir && n.expanded {
			for _, c := range n.children {
				walk(c)
			}
		}
	}
	walk(m.root)
	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
	m.follow()
}

// follow ajusta top para que el cursor sea visible.
func (m *Model) follow() {
	if m.h <= 0 {
		return
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+m.h {
		m.top = m.cursor - m.h + 1
	}
	m.top = max(0, min(m.top, len(m.rows)-m.h))
}

// SetSize fija el tamaño en celdas.
func (m *Model) SetSize(w, h int) {
	m.w, m.h = max(w, 0), max(h, 0)
	m.follow()
}

// Focus y Blur dan y quitan el foco; sin foco, Update no actúa.
func (m *Model) Focus() { m.focused = true }

// Blur quita el foco.
func (m *Model) Blur() { m.focused = false }

// Focused indica si el árbol tiene el foco.
func (m Model) Focused() bool { return m.focused }

// Root devuelve la ruta absoluta de la raíz.
func (m Model) Root() string { return m.root.path }

// Selected devuelve la ruta de la fila bajo el cursor.
func (m Model) Selected() string {
	if m.cursor < len(m.rows) {
		return m.rows[m.cursor].path
	}
	return ""
}

// Reveal despliega las carpetas que contienen path (si está bajo la raíz y no
// se ignora) y mueve el cursor a él.
func (m *Model) Reveal(path string) {
	rel, err := filepath.Rel(m.root.path, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return
	}
	n := m.root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if !n.loaded {
			m.load(n)
		}
		n.expanded = true
		var next *node
		for _, c := range n.children {
			if c.name == part {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		n = next
	}
	m.flatten()
	for i, r := range m.rows {
		if r == n {
			m.cursor = i
			break
		}
	}
	m.follow()
}

// Update procesa teclas cuando el árbol tiene el foco.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok || !m.focused || len(m.rows) == 0 {
		return m, nil
	}
	cur := m.rows[m.cursor]
	var cmd tea.Cmd
	switch {
	case key.Matches(k, m.keys.Up):
		m.cursor--
	case key.Matches(k, m.keys.Down):
		m.cursor++
	case key.Matches(k, m.keys.PageUp):
		m.cursor -= max(m.h-1, 1)
	case key.Matches(k, m.keys.PageDown):
		m.cursor += max(m.h-1, 1)
	case key.Matches(k, m.keys.First):
		m.cursor = 0
	case key.Matches(k, m.keys.Last):
		m.cursor = len(m.rows) - 1
	case key.Matches(k, m.keys.Left):
		switch {
		case cur.dir && cur.expanded && cur != m.root:
			cur.expanded = false
		case cur.parent != nil:
			m.cursor = m.indexOf(cur.parent)
		}
	case key.Matches(k, m.keys.Right):
		if cur.dir {
			if !cur.expanded {
				m.expand(cur)
			} else if len(cur.children) > 0 {
				m.cursor++
			}
		}
	case key.Matches(k, m.keys.Enter):
		if cur.dir {
			if cur.expanded && cur != m.root {
				cur.expanded = false
			} else {
				m.expand(cur)
			}
		} else {
			p := cur.path
			cmd = func() tea.Msg { return OpenFileMsg{Path: p} }
		}
	}
	m.cursor = max(0, min(m.cursor, len(m.rows)-1))
	m.flatten()
	return m, cmd
}

func (m Model) expand(n *node) {
	if !n.loaded || !n.expanded {
		m.load(n) // recarga: refleja cambios en disco
	}
	n.expanded = true
}

func (m Model) indexOf(n *node) int {
	for i, r := range m.rows {
		if r == n {
			return i
		}
	}
	return m.cursor
}

// View dibuja exactamente h filas de w celdas.
func (m Model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	sel := lipgloss.NewStyle().Reverse(true)
	if !m.focused {
		sel = lipgloss.NewStyle().Bold(true)
	}
	dir := lipgloss.NewStyle().Foreground(lipgloss.Color("12"))

	lines := make([]string, m.h)
	for i := range lines {
		idx := m.top + i
		if idx >= len(m.rows) {
			lines[i] = strings.Repeat(" ", m.w)
			continue
		}
		n := m.rows[idx]
		marker := "  "
		name := n.name
		if n.dir {
			marker = "▸ "
			if n.expanded {
				marker = "▾ "
			}
			name += "/"
		}
		text := strings.Repeat("  ", max(n.depth-1, 0)) + marker + name
		if n == m.root {
			text = "▾ " + n.name + "/"
		}
		text = fit(text, m.w)
		switch {
		case idx == m.cursor:
			text = sel.Render(text)
		case n.dir:
			text = dir.Render(text)
		}
		lines[i] = text
	}
	return strings.Join(lines, "\n")
}

// fit recorta o rellena s a exactamente w celdas.
func fit(s string, w int) string {
	s = lipgloss.NewStyle().MaxWidth(w).Render(s)
	if d := w - lipgloss.Width(s); d > 0 {
		s += strings.Repeat(" ", d)
	}
	return s
}
