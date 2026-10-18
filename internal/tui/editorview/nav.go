package editorview

import "github.com/dpa23/wari/internal/editor"

// Navegación y completado. El editor no conoce el índice de símbolos: solo
// ofrece lo que el layout necesita para el outline, "ir a definición" y el
// autocompletado: el identificador bajo el cursor, su prefijo, reemplazar ese
// prefijo y saltar a una posición.

// Text devuelve el contenido actual del buffer (con lo que aún no se guardó).
func (m Model) Text() string { return m.buf.String() }

// WordAtCursor devuelve el identificador que toca el cursor y su posición
// (línea y columna de inicio, base 0, en runas); "" si no hay.
func (m Model) WordAtCursor() (word string, line, col int) {
	c := m.buf.Cursor()
	ln := m.buf.Line(c.Line)
	l, r := min(c.Col, len(ln)), min(c.Col, len(ln))
	for l > 0 && isWordRune(ln[l-1]) {
		l--
	}
	for r < len(ln) && isWordRune(ln[r]) {
		r++
	}
	return string(ln[l:r]), c.Line, l
}

// WordPrefix devuelve las letras del identificador que quedan a la izquierda
// del cursor (lo ya escrito de una palabra a completar).
func (m Model) WordPrefix() string {
	c := m.buf.Cursor()
	ln := m.buf.Line(c.Line)
	r := min(c.Col, len(ln))
	l := r
	for l > 0 && isWordRune(ln[l-1]) {
		l--
	}
	return string(ln[l:r])
}

// ReplacePrefix reemplaza el prefijo de palabra que precede al cursor (ver
// WordPrefix) por name, como un único paso de deshacer, y deja el cursor al
// final del nombre. Devuelve false si no había prefijo.
func (m *Model) ReplacePrefix(name string) bool {
	c := m.buf.Cursor()
	n := len([]rune(m.WordPrefix()))
	if n == 0 {
		return false
	}
	m.snip = nil // los offsets de la sesión dejan de valer
	m.buf.ReplaceRange(editor.Pos{Line: c.Line, Col: c.Col - n}, c, name)
	m.edited()
	m.suggest = m.computeSuggestion()
	m.top, m.xoff = m.scroll()
	return true
}

// GoTo lleva el cursor a la línea y columna dadas (base 0, en runas; se
// ajustan al buffer) y centra la línea en la vista.
func (m *Model) GoTo(line, col int) {
	m.snip = nil
	n := m.buf.LineCount()
	line = max(0, min(line, n-1))
	m.buf.SetCursorPos(editor.Pos{Line: line, Col: max(col, 0)})
	if m.h > 0 {
		m.top = max(0, min(line-m.h/2, n-m.h))
	}
	m.top, m.xoff = m.scroll()
	m.suggest = m.computeSuggestion()
}
