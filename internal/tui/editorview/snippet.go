package editorview

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/editor"
	"github.com/dpa23/wari/internal/snippets"
)

// Snippets. Con el foco en el editor, Tab tras una palabra que coincide
// exactamente con un prefijo de los snippets del lenguaje del archivo la
// expande y abre una sesión de tabstops: Tab y Shift+Tab (SnippetPrev) van a
// la parada siguiente o anterior, seleccionando su placeholder; Esc (Cancel) o
// llegar a $0 la terminan, igual que mover el cursor fuera del texto
// expandido, deshacer o rehacer. Mientras dura, cada edición se informa a la
// snippets.Session para que reajuste las demás paradas, y lo escrito en una
// parada se copia a sus espejos.

// snippetLangs asocia la extensión del archivo con el lenguaje de sus snippets.
var snippetLangs = map[string]string{
	".java": "java",
	".kt":   "kotlin",
	".kts":  "kotlin",
	".cs":   "csharp",
}

// SnippetLang devuelve el lenguaje de snippets de path ("" si no tiene).
func SnippetLang(path string) string {
	return snippetLangs[strings.ToLower(filepath.Ext(path))]
}

// snippetState es la sesión de tabstops en curso. Los offsets de la sesión
// son en runas desde base; span es la longitud actual del texto expandido.
type snippetState struct {
	sess *snippets.Session
	base editor.Pos
	span int
}

// SetSnippets fija los snippets por lenguaje ("java", "kotlin", "csharp").
// Sin ellos, Tab inserta espacios siempre. Termina la sesión en curso.
func (m *Model) SetSnippets(sets map[string]snippets.Set) {
	m.sets = sets
	m.snip = nil
}

// SnippetActive indica que hay una sesión de tabstops abierta: Tab y
// Shift+Tab (y Esc) le pertenecen al editor y no mueven el foco.
func (m Model) SnippetActive() bool { return m.snip != nil }

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// escapeRepl protege el texto de la sintaxis de expansión de Regexp.
func escapeRepl(s string) string { return strings.ReplaceAll(s, "$", "$$") }

// replaceAt cambia old, que empieza en p, por text como un único paso de
// deshacer. Deja el cursor al final del texto nuevo.
func (m *Model) replaceAt(p editor.Pos, old, text string) bool {
	m.buf.SetCursorPos(p)
	re := regexp.MustCompile(`\A` + regexp.QuoteMeta(old))
	if !m.buf.ReplaceNext(re, escapeRepl(text)) {
		return false
	}
	m.edited()
	return true
}

// exactSnippet busca el snippet cuyo prefijo es exactamente word.
func exactSnippet(set snippets.Set, word string) (snippets.Snippet, bool) {
	for _, sn := range set.Match(word) {
		for _, p := range sn.Prefixes {
			if p == word {
				return sn, true
			}
		}
	}
	return snippets.Snippet{}, false
}

// expandSnippet expande el snippet cuyo prefijo precede al cursor. Devuelve
// false si no hay coincidencia (el llamador inserta espacios).
func (m *Model) expandSnippet() bool {
	set, ok := m.sets[SnippetLang(m.path)]
	b := m.buf
	if !ok || !selEmpty(b) {
		return false
	}
	c := b.Cursor()
	line := b.Line(c.Line)
	if c.Col < len(line) && isWordRune(line[c.Col]) {
		return false
	}
	// Algunos prefijos llevan guion ("autowired-ctor"): se prueba primero la
	// palabra con guiones y luego sin ellos.
	for _, hyphen := range []bool{true, false} {
		start := c.Col
		for start > 0 && (isWordRune(line[start-1]) || (hyphen && line[start-1] == '-')) {
			start--
		}
		word := string(line[start:c.Col])
		if word == "" {
			continue
		}
		if sn, ok := exactSnippet(set, word); ok {
			return m.insertSnippet(sn, c, start, word)
		}
	}
	return false
}

// insertSnippet reemplaza la palabra [start, c.Col) de la línea de c por el
// snippet expandido y abre la sesión.
func (m *Model) insertSnippet(sn snippets.Snippet, c editor.Pos, start int, word string) bool {
	line := m.buf.Line(c.Line)
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	exp, err := snippets.Expand(sn, snippets.Vars{Filename: m.path, Tab: snippets.Spaces(tabWidth)}, string(line[:n]))
	if err != nil {
		return false
	}
	base := editor.Pos{Line: c.Line, Col: start}
	if !m.replaceAt(base, word, exp.Text) {
		m.buf.SetCursorPos(c)
		return false
	}
	m.snip = &snippetState{sess: snippets.NewSession(exp), base: base, span: utf8.RuneCountInString(exp.Text)}
	m.goTo(m.snip.sess.Current())
	if m.snip.sess.Finished() {
		m.snip = nil
	}
	return true
}

// --- Offsets de la sesión ---------------------------------------------------

// snipOff devuelve el offset en runas de p desde el inicio del texto expandido,
// o -1 si p queda antes.
func (m Model) snipOff(p editor.Pos) int {
	base, b := m.snip.base, m.buf
	if p.Line < base.Line || (p.Line == base.Line && p.Col < base.Col) {
		return -1
	}
	if p.Line == base.Line {
		return p.Col - base.Col
	}
	n := len(b.Line(base.Line)) - base.Col + 1
	for l := base.Line + 1; l < p.Line; l++ {
		n += len(b.Line(l)) + 1
	}
	return n + p.Col
}

// posAt es la inversa de snipOff.
func (m Model) posAt(off int) editor.Pos {
	b := m.buf
	line, col := m.snip.base.Line, m.snip.base.Col+off
	for line < b.LineCount()-1 && col > len(b.Line(line)) {
		col -= len(b.Line(line)) + 1
		line++
	}
	return editor.Pos{Line: line, Col: max(col, 0)}
}

// textRange devuelve el texto de los offsets [start, end).
func (m Model) textRange(start, end int) string {
	a, z := m.posAt(start), m.posAt(end)
	if a.Line == z.Line {
		return string(m.buf.Line(a.Line)[a.Col:z.Col])
	}
	var sb strings.Builder
	sb.WriteString(string(m.buf.Line(a.Line)[a.Col:]))
	for l := a.Line + 1; l < z.Line; l++ {
		sb.WriteByte('\n')
		sb.WriteString(string(m.buf.Line(l)))
	}
	sb.WriteByte('\n')
	sb.WriteString(string(m.buf.Line(z.Line)[:z.Col]))
	return sb.String()
}

// goTo coloca el cursor en la parada t y selecciona su placeholder.
func (m *Model) goTo(t snippets.Tabstop) {
	m.buf.SetCursorPos(m.posAt(t.Start))
	for range t.End - t.Start {
		m.buf.MoveRight(true)
	}
}

// --- Teclas y ediciones durante la sesión ----------------------------------

// editSnap es el estado previo a una tecla, para deducir qué edición hizo.
type editSnap struct {
	selStart editor.Pos // inicio de la selección, o el cursor si no hay
	docLen   int
}

func before(p, q editor.Pos) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Col < q.Col)
}

func (m Model) docLen() int {
	n := 0
	for i := 0; i < m.buf.LineCount(); i++ {
		n += len(m.buf.Line(i)) + 1
	}
	return n
}

func (m Model) snapshot() editSnap {
	sel := m.buf.Selection()
	a := sel.Anchor
	if before(sel.Active, a) {
		a = sel.Active
	}
	if selEmpty(m.buf) {
		a = m.buf.Cursor()
	}
	return editSnap{selStart: a, docLen: m.docLen()}
}

// snippetKey atiende las teclas propias de la sesión. Devuelve true si la
// tecla queda consumida.
func (m *Model) snippetKey(msg tea.KeyPressMsg) bool {
	s, k := m.snip, m.keys
	switch {
	case key.Matches(msg, k.Tab):
		t, ok := s.sess.Next()
		if !ok {
			m.snip = nil
			return false
		}
		m.goTo(t)
		if s.sess.Finished() {
			m.snip = nil
		}
		return true
	case key.Matches(msg, k.SnippetPrev):
		if t, ok := s.sess.Prev(); ok {
			m.goTo(t)
		}
		return true
	case key.Matches(msg, k.Cancel):
		m.snip = nil
		return true
	case key.Matches(msg, k.Undo), key.Matches(msg, k.Redo):
		// Los offsets dejan de valer: la sesión termina y el deshacer sigue.
		m.snip = nil
	}
	return false
}

// snippetAfter reajusta la sesión tras una tecla o un pegado. pre es el
// estado anterior; m.touched dice si hubo edición.
func (m *Model) snippetAfter(pre editSnap) {
	s := m.snip
	if s == nil {
		return
	}
	if m.touched {
		c1 := m.buf.Cursor()
		startPos := pre.selStart
		if before(c1, startPos) {
			startPos = c1
		}
		start := m.snipOff(startPos)
		if start < 0 {
			m.snip = nil
			return
		}
		newLen := max(m.snipOff(c1)-start, 0)
		delta := m.docLen() - pre.docLen
		oldLen := newLen - delta
		if oldLen < 0 || start+oldLen > s.span {
			m.snip = nil
			return
		}
		s.span += delta
		cur := s.sess.Current()
		if start >= cur.Start && start+oldLen <= cur.End {
			// Dentro de la parada actual: se informa como reemplazo de toda
			// ella, para que crezca también al escribir en su borde.
			s.sess.Edit(cur.Start, cur.End, cur.End-cur.Start+delta)
			m.syncMirrors()
		} else {
			s.sess.Edit(start, start+oldLen, newLen)
		}
	}
	if m.snip != nil {
		if o := m.snipOff(m.buf.Cursor()); o < 0 || o > m.snip.span {
			m.snip = nil
		}
	}
}

// syncMirrors copia el texto de la parada actual a sus espejos. Cada espejo
// es un paso de deshacer propio. Un espejo vacío no se puede reajustar de
// forma fiable (Session.Edit no distingue un punto de inserción de sí mismo),
// así que tras rellenarlo la sesión termina.
func (m *Model) syncMirrors() {
	s := m.snip
	cur := s.sess.Current()
	if len(cur.Mirrors) == 0 {
		return
	}
	text := m.textRange(cur.Start, cur.End)
	newLen := utf8.RuneCountInString(text)
	curOff := m.snipOff(m.buf.Cursor())
	stop := false
	for i := 0; i < len(s.sess.Current().Mirrors); i++ {
		r := s.sess.Current().Mirrors[i]
		if r.Start < 0 || r.End < r.Start || r.End > s.span {
			stop = true
			break
		}
		old := m.textRange(r.Start, r.End)
		if old == text {
			continue
		}
		if !m.replaceAt(m.posAt(r.Start), old, text) {
			stop = true
			break
		}
		delta := newLen - (r.End - r.Start)
		if r.End <= curOff {
			curOff += delta
		}
		s.span += delta
		s.sess.Edit(r.Start, r.End, newLen)
		if r.End == r.Start {
			stop = true
		}
	}
	m.buf.SetCursorPos(m.posAt(curOff))
	if stop {
		m.snip = nil
	}
}
