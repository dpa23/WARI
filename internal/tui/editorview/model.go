// Package editorview es el componente Bubble Tea v2 que muestra y edita un
// [editor.Buffer]: números de línea, resaltado de sintaxis con chroma, scroll
// que sigue al cursor y selección. No usa AltScreen ni conoce el layout: el
// llamador lo coloca y le pasa el tamaño con SetSize.
package editorview

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/editor"
	"github.com/dpa23/wari/internal/imports"
	"github.com/dpa23/wari/internal/snippets"
)

const (
	// tabWidth es el ancho de una tabulación ya presente en el archivo; la
	// tecla Tab inserta tabSpaces espacios.
	tabWidth  = 4
	tabSpaces = "    "

	sgrReset    = "\x1b[0m"
	sgrGutter   = "\x1b[38;2;110;118;129m"
	sgrGutterOn = "\x1b[38;2;201;209;217m"
	sgrSelect   = "\x1b[48;2;38;79;120m"
)

// SavedMsg informa del resultado de guardar (ctrl+s). El layout puede
// mostrarlo en la línea de estado.
type SavedMsg struct {
	Path string
	Err  error
}

// Model es el editor de un buffer. Se usa con receptor de valor; el buffer
// es compartido (puntero), así que copias del modelo editan el mismo buffer.
type Model struct {
	buf  *editor.Buffer
	fi   editor.FileInfo
	path string
	keys KeyMap
	hl   *highlighter

	w, h int
	// top es la primera línea visible; xoff, la primera columna visible (en
	// celdas de pantalla).
	top, xoff int

	focused bool
	dirty   bool
	// saved es la huella del contenido en el último guardado (o al abrir).
	// off cuenta los deshacer menos los rehacer desde la última edición o
	// guardado; cleanOff, si cleanKnown, es el valor de off en el que el
	// buffer coincide con lo guardado. Así dirty se actualiza en O(1) en
	// cada deshacer/rehacer, y solo se calcula la huella mientras no se sabe
	// en qué paso se vuelve al estado guardado.
	saved      fingerprint
	off        int
	cleanOff   int
	cleanKnown bool

	// disk es lo que se vio del archivo al abrirlo o guardarlo (mtime y
	// tamaño); overwrite indica que ya se avisó de que cambió en disco y el
	// próximo guardado lo sobrescribe.
	disk      diskStamp
	overwrite bool

	// sets son los snippets por lenguaje; snip, la sesión de tabstops en
	// curso (nil si no hay); touched marca que la última tecla editó el
	// buffer.
	sets    map[string]snippets.Set
	snip    *snippetState
	touched bool

	// imps son los mapas tipo→import por lenguaje; suggest, el tipo bajo el
	// cursor al que le falta el import ("" si no hay).
	imps    map[string]imports.Map
	suggest string

	// marks son las marcas del margen por línea (ver SetLineMarks).
	marks map[int]LineMark
}

// fingerprint resume el contenido del buffer: líneas y un hash FNV-1a.
type fingerprint struct {
	lines int
	sum   uint64
}

func fingerprintOf(b *editor.Buffer) fingerprint {
	const prime = 1099511628211
	h := uint64(14695981039346656037)
	n := b.LineCount()
	for i := 0; i < n; i++ {
		for _, r := range b.Line(i) {
			h = (h ^ uint64(r)) * prime
		}
		h = (h ^ '\n') * prime
	}
	return fingerprint{lines: n, sum: h}
}

// diskStamp es el mtime y el tamaño de un archivo en un momento dado.
type diskStamp struct {
	mod  time.Time
	size int64
	ok   bool // el archivo existía
}

func statDisk(path string) diskStamp {
	st, err := os.Stat(path)
	if err != nil {
		return diskStamp{}
	}
	return diskStamp{mod: st.ModTime(), size: st.Size(), ok: true}
}

// ErrChangedOnDisk indica que el archivo cambió en disco desde que se abrió o
// guardó; el siguiente guardado lo sobrescribe.
var ErrChangedOnDisk = errors.New("el archivo cambió en disco")

// New crea el editor de buf, que se abrió (o se guardará) con fi y path. El
// resaltado se elige por la extensión de path.
func New(buf *editor.Buffer, fi editor.FileInfo, path string, keys KeyMap) Model {
	m := Model{
		buf:   buf,
		fi:    fi,
		path:  path,
		keys:  keys,
		hl:    newHighlighter(path),
		saved: fingerprintOf(buf),
		// Un buffer recién abierto está limpio en off == 0.
		cleanKnown: true,
	}
	m.disk = statDisk(m.target())
	return m
}

// SetSize fija el tamaño del componente en celdas y recoloca el scroll.
func (m *Model) SetSize(w, h int) {
	m.w, m.h = max(w, 0), max(h, 0)
	m.top, m.xoff = m.scroll()
}

// Focus da el foco al editor; Blur se lo quita. Sin foco, Update no actúa y
// Cursor devuelve nil.
func (m *Model) Focus() { m.focused = true }

// Blur quita el foco.
func (m *Model) Blur() { m.focused = false }

// Focused indica si el editor tiene el foco.
func (m Model) Focused() bool { return m.focused }

// Dirty indica si el buffer tiene cambios sin guardar. Solo ve las ediciones
// hechas a través de Update: si otro código modifica el buffer directamente,
// debe volver a abrir o guardar por su cuenta.
func (m Model) Dirty() bool { return m.dirty }

// Buffer devuelve el buffer editado.
func (m Model) Buffer() *editor.Buffer { return m.buf }

// Path devuelve la ruta del archivo.
func (m Model) Path() string { return m.path }

// Cursor devuelve la posición del cursor relativa a la esquina superior
// izquierda del componente, o nil si no tiene foco o no cabe.
func (m Model) Cursor() *tea.Cursor {
	if !m.focused || m.w <= 0 || m.h <= 0 {
		return nil
	}
	top, xoff := m.scroll()
	p := m.buf.Cursor()
	x := m.gutterWidth() + displayCol(m.buf.Line(p.Line), p.Col) - xoff
	y := p.Line - top
	if x < 0 || y < 0 || y >= m.h || x >= m.w {
		return nil
	}
	c := tea.NewCursor(x, y)
	c.Shape = tea.CursorBar
	return c
}

// Update procesa teclas y pegado cuando el editor tiene el foco.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.focused {
		return m, nil
	}
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if !key.Matches(msg, m.keys.Save) {
			m.overwrite = false
		}
		cmd = m.handleKeyTracked(msg)
	case tea.PasteMsg:
		m.overwrite = false
		if m.snip == nil {
			m.buf.Paste(msg.Content)
			m.edited()
			break
		}
		pre := m.snapshot()
		m.touched = false
		m.buf.BeginGroup()
		m.buf.Paste(msg.Content)
		m.edited()
		m.snippetAfter(pre)
		m.buf.EndGroup()
	default:
		return m, nil
	}
	m.suggest = m.computeSuggestion()
	m.top, m.xoff = m.scroll()
	return m, cmd
}

// handleKeyTracked aplica una tecla; con una sesión de snippet abierta,
// además atiende sus teclas y le informa de la edición que haya hecho.
func (m *Model) handleKeyTracked(msg tea.KeyPressMsg) tea.Cmd {
	if m.snip == nil {
		return m.handleKey(msg)
	}
	if m.snippetKey(msg) {
		return nil
	}
	if m.snip == nil { // la tecla terminó la sesión (deshacer, rehacer)
		return m.handleKey(msg)
	}
	pre := m.snapshot()
	m.touched = false
	m.buf.BeginGroup()
	cmd := m.handleKey(msg)
	m.snippetAfter(pre)
	m.buf.EndGroup()
	return cmd
}

// handleKey aplica una tecla al buffer.
func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	b, k := m.buf, m.keys
	switch {
	case key.Matches(msg, k.Left):
		b.MoveLeft(false)
	case key.Matches(msg, k.SelLeft):
		b.MoveLeft(true)
	case key.Matches(msg, k.Right):
		b.MoveRight(false)
	case key.Matches(msg, k.SelRight):
		b.MoveRight(true)
	case key.Matches(msg, k.Up):
		b.MoveUp(false)
	case key.Matches(msg, k.SelUp):
		b.MoveUp(true)
	case key.Matches(msg, k.Down):
		b.MoveDown(false)
	case key.Matches(msg, k.SelDown):
		b.MoveDown(true)
	case key.Matches(msg, k.WordLeft):
		b.MoveWordLeft(false)
	case key.Matches(msg, k.SelWordLeft):
		b.MoveWordLeft(true)
	case key.Matches(msg, k.WordRight):
		b.MoveWordRight(false)
	case key.Matches(msg, k.SelWordRight):
		b.MoveWordRight(true)
	case key.Matches(msg, k.LineStart):
		b.MoveLineStart(false)
	case key.Matches(msg, k.SelLineStart):
		b.MoveLineStart(true)
	case key.Matches(msg, k.LineEnd):
		b.MoveLineEnd(false)
	case key.Matches(msg, k.SelLineEnd):
		b.MoveLineEnd(true)
	case key.Matches(msg, k.DocStart):
		b.MoveDocStart(false)
	case key.Matches(msg, k.SelDocStart):
		b.MoveDocStart(true)
	case key.Matches(msg, k.DocEnd):
		b.MoveDocEnd(false)
	case key.Matches(msg, k.SelDocEnd):
		b.MoveDocEnd(true)
	case key.Matches(msg, k.PageUp):
		m.page(-1, false)
	case key.Matches(msg, k.SelPageUp):
		m.page(-1, true)
	case key.Matches(msg, k.PageDown):
		m.page(1, false)
	case key.Matches(msg, k.SelPageDown):
		m.page(1, true)
	case key.Matches(msg, k.SelectAll):
		b.SelectAll()

	case key.Matches(msg, k.Save):
		return m.save()
	case key.Matches(msg, k.Undo):
		if b.Undo() {
			m.off++
			m.historyMoved()
		}
	case key.Matches(msg, k.Redo):
		if b.Redo() {
			m.off--
			m.historyMoved()
		}

	case key.Matches(msg, k.Newline):
		b.InsertRune('\n')
		m.edited()
	case key.Matches(msg, k.Tab):
		if !m.expandSnippet() {
			b.InsertText(tabSpaces)
			m.edited()
		}
	case key.Matches(msg, k.Backspace):
		if c := b.Cursor(); !selEmpty(b) || c.Line > 0 || c.Col > 0 {
			b.DeleteBackward()
			m.edited()
		}
	case key.Matches(msg, k.Delete):
		c := b.Cursor()
		if !selEmpty(b) || c.Col < len(b.Line(c.Line)) || c.Line < b.LineCount()-1 {
			b.DeleteForward()
			m.edited()
		}

	default:
		// Texto imprimible con cualquier modificador: en Windows, AltGr llega
		// como Text con ctrl+alt ("@", "{", "["). Los atajos de teclado sin
		// binding (ctrl+x...) no traen Text y se ignoran.
		for _, r := range msg.Text {
			if r < 0x20 || r == 0x7f || r == utf8.RuneError {
				continue
			}
			b.InsertRune(r)
			m.edited()
		}
	}
	return nil
}

// edited marca el buffer como modificado por una edición. Tras una edición
// ya no se sabe en qué paso de deshacer se vuelve al estado guardado.
func (m *Model) edited() {
	m.touched = true
	m.dirty = true
	m.off = 0
	m.cleanKnown = false
}

// historyMoved actualiza dirty tras un deshacer o rehacer.
func (m *Model) historyMoved() {
	if m.cleanKnown {
		m.dirty = m.off != m.cleanOff
		return
	}
	if fingerprintOf(m.buf) == m.saved {
		m.cleanKnown, m.cleanOff, m.dirty = true, m.off, false
		return
	}
	m.dirty = true
}

// markSaved registra el estado actual como el guardado.
func (m *Model) markSaved() {
	m.dirty = false
	m.saved = fingerprintOf(m.buf)
	m.off, m.cleanOff, m.cleanKnown = 0, 0, true
}

// selEmpty indica que no hay selección activa.
func selEmpty(b *editor.Buffer) bool {
	s := b.Selection()
	return s.Anchor == s.Active
}

// page mueve el cursor una pantalla hacia arriba (dir < 0) o abajo.
func (m *Model) page(dir int, sel bool) {
	n := max(m.h-1, 1)
	for range n {
		switch {
		case dir < 0 && sel:
			m.buf.MoveUp(true)
		case dir < 0:
			m.buf.MoveUp(false)
		case sel:
			m.buf.MoveDown(true)
		default:
			m.buf.MoveDown(false)
		}
	}
}

// Save guarda el buffer en disco aunque el editor no tenga el foco y devuelve
// un Cmd que emite el resultado como SavedMsg.
func (m *Model) Save() tea.Cmd { return m.save() }

// Keys devuelve los atajos del editor.
func (m Model) Keys() KeyMap { return m.keys }

// target es la ruta donde se guarda el buffer.
func (m Model) target() string {
	if m.fi.Path != "" {
		return m.fi.Path
	}
	return m.path
}

// save guarda el buffer en disco y devuelve el resultado como SavedMsg. Si el
// archivo cambió en disco (mtime o tamaño) desde que se abrió o guardó, la
// primera vez no lo pisa: avisa con ErrChangedOnDisk y espera otro guardado.
func (m *Model) save() tea.Cmd {
	fi := m.fi
	fi.Path = m.target()
	done := func(err error) tea.Cmd {
		msg := SavedMsg{Path: fi.Path, Err: err}
		return func() tea.Msg { return msg }
	}
	if !m.overwrite && m.changedOnDisk(fi.Path) {
		m.overwrite = true
		k := m.keys.Save.Help().Key
		if k == "" {
			k = "guardar"
		}
		return done(fmt.Errorf("%w; %s otra vez para sobrescribir", ErrChangedOnDisk, k))
	}
	m.overwrite = false
	err := m.buf.Save(fi)
	if err == nil {
		m.markSaved()
		m.disk = statDisk(fi.Path)
	}
	return done(err)
}

// changedOnDisk indica que el archivo de path no es el que se vio al abrirlo
// o guardarlo. Si no se puede consultar, no bloquea el guardado.
func (m Model) changedOnDisk(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !m.disk.ok || !st.ModTime().Equal(m.disk.mod) || st.Size() != m.disk.size
}

// gutterWidth es el ancho de la columna de números (dígitos + 2 de margen);
// 0 si el componente es demasiado estrecho para mostrarla.
func (m Model) gutterWidth() int {
	gw := len(strconv.Itoa(m.buf.LineCount())) + 2
	if m.w <= gw {
		return 0
	}
	return gw
}

// scroll devuelve top y xoff ajustados para que el cursor sea visible. Es una
// función pura: Update guarda el resultado y View/Cursor lo recalculan, así
// un cambio externo del buffer no deja el cursor fuera de la vista.
func (m Model) scroll() (top, xoff int) {
	top, xoff = m.top, m.xoff
	if m.h <= 0 || m.w <= 0 {
		return 0, 0
	}
	p := m.buf.Cursor()
	if p.Line < top {
		top = p.Line
	}
	if p.Line >= top+m.h {
		top = p.Line - m.h + 1
	}
	top = max(0, min(top, m.buf.LineCount()-m.h))

	tw := max(m.w-m.gutterWidth(), 1)
	cx := displayCol(m.buf.Line(p.Line), p.Col)
	if cx < xoff {
		xoff = cx
	}
	if cx >= xoff+tw {
		xoff = cx - tw + 1
	}
	return top, max(xoff, 0)
}

// View dibuja el componente: exactamente h líneas de w celdas.
func (m Model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	top, xoff := m.scroll()
	gw := m.gutterWidth()
	tw := m.w - gw
	n := max(0, min(m.h, m.buf.LineCount()-top))
	marks := m.hl.lines(m.buf, top, n)

	sel := m.buf.Selection()
	selStart, selEnd := sel.Anchor, sel.Active
	if selEnd.Line < selStart.Line || (selEnd.Line == selStart.Line && selEnd.Col < selStart.Col) {
		selStart, selEnd = selEnd, selStart
	}
	hasSel := sel.Anchor != sel.Active
	cursorLine := m.buf.Cursor().Line

	var sb strings.Builder
	sb.Grow(m.h * (m.w + 24))
	for row := 0; row < m.h; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}
		li := top + row
		if li >= m.buf.LineCount() {
			sb.WriteString(strings.Repeat(" ", m.w))
			continue
		}
		if gw > 0 {
			if li == cursorLine && m.focused {
				sb.WriteString(sgrGutterOn)
			} else {
				sb.WriteString(sgrGutter)
			}
			num := strconv.Itoa(li + 1)
			sb.WriteString(strings.Repeat(" ", gw-2-len(num)))
			sb.WriteString(num)
			if len(m.marks) == 0 {
				sb.WriteString("  ")
			} else {
				sb.WriteString(m.markGlyph(li))
				sb.WriteString(sgrReset)
				sb.WriteString(" ")
			}
			sb.WriteString(sgrReset)
		}
		var mk []uint16
		if row < len(marks) {
			mk = marks[row]
		}
		// Rango seleccionado en esta línea: columnas [sc, ec) y, si la
		// selección continúa en la línea siguiente, la celda del salto.
		sc, ec, nlSel := -1, -1, false
		if hasSel && li >= selStart.Line && li <= selEnd.Line {
			line := m.buf.Line(li)
			sc, ec = 0, len(line)
			if li == selStart.Line {
				sc = selStart.Col
			}
			if li == selEnd.Line {
				ec = selEnd.Col
			}
			nlSel = li < selEnd.Line
		}
		m.writeLine(&sb, m.buf.Line(li), mk, sc, ec, nlSel, xoff, tw)
	}
	return sb.String()
}

// writeLine escribe la parte visible de una línea, rellena hasta tw celdas.
func (m Model) writeLine(sb *strings.Builder, line []rune, mk []uint16, sc, ec int, nlSel bool, xoff, tw int) {
	var curStyle uint16
	curSel := false
	styled := false
	set := func(style uint16, sel bool) {
		if styled && style == curStyle && sel == curSel {
			return
		}
		sb.WriteString(sgrReset)
		sb.WriteString(m.hl.sgrOf(style))
		if sel {
			sb.WriteString(sgrSelect)
		}
		curStyle, curSel, styled = style, sel, true
	}

	x, out := 0, 0 // columna de la línea y celdas ya escritas
	for i, r := range line {
		if x >= xoff+tw {
			break
		}
		rw := runeWidth(r, x)
		var text string
		vis := rw
		switch {
		case x+rw <= xoff:
			x += rw
			continue
		case x < xoff, x+rw > xoff+tw:
			// Celda cortada por el borde: se rellena con espacios.
			vis = min(x+rw, xoff+tw) - max(x, xoff)
			text = strings.Repeat(" ", vis)
		case r == '\t':
			text = strings.Repeat(" ", rw)
		case r < 0x20 || r == 0x7f:
			text = "·"
		default:
			text = string(r)
		}
		var st uint16
		if i < len(mk) {
			st = mk[i]
		}
		set(st, i >= sc && i < ec)
		sb.WriteString(text)
		out += vis
		x += rw
	}
	// Celda del salto de línea seleccionado.
	if nlSel && x >= xoff && x < xoff+tw {
		set(0, true)
		sb.WriteByte(' ')
		out++
	}
	if styled {
		sb.WriteString(sgrReset)
	}
	if out < tw {
		sb.WriteString(strings.Repeat(" ", tw-out))
	}
}

// runeWidth es el ancho en celdas de r si empieza en la columna x.
func runeWidth(r rune, x int) int {
	switch {
	case r == '\t':
		return tabWidth - x%tabWidth
	case r < 0x80:
		return 1
	}
	return lipgloss.Width(string(r))
}

// displayCol es la columna de pantalla del índice col de line.
func displayCol(line []rune, col int) int {
	x := 0
	for i, r := range line {
		if i >= col {
			break
		}
		x += runeWidth(r, x)
	}
	return x
}
