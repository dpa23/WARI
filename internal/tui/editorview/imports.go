package editorview

import (
	"strings"
	"unicode"

	"github.com/dpa23/wari/internal/editor"
	"github.com/dpa23/wari/internal/imports"
)

// Imports. El editor sabe qué tipo hay bajo el cursor y si le falta el
// import (ImportSuggestion, que el layout muestra en la línea de estado), y
// puede insertarlo (AddImport) como un único paso de deshacer. Elegir entre
// varios candidatos es cosa del layout. El lenguaje se deduce de la extensión
// igual que en los snippets (SnippetLang).

// SetImports fija los mapas tipo→import por lenguaje ("java", "kotlin",
// "csharp"). Sin ellos no hay sugerencias.
func (m *Model) SetImports(maps map[string]imports.Map) {
	m.imps = maps
	m.suggest = m.computeSuggestion()
}

// lineStrings devuelve las líneas del buffer como texto.
func (m Model) lineStrings() []string {
	out := make([]string, m.buf.LineCount())
	for i := range out {
		out[i] = string(m.buf.Line(i))
	}
	return out
}

// TypeAtCursor devuelve el identificador con inicial mayúscula que toca el
// cursor ("" si no hay).
func (m Model) TypeAtCursor() string {
	c := m.buf.Cursor()
	line := m.buf.Line(c.Line)
	l, r := min(c.Col, len(line)), min(c.Col, len(line))
	for l > 0 && isWordRune(line[l-1]) {
		l--
	}
	for r < len(line) && isWordRune(line[r]) {
		r++
	}
	if l == r || !unicode.IsUpper(line[l]) {
		return ""
	}
	return string(line[l:r])
}

// ImportCandidates devuelve los imports conocidos del tipo typ para este
// archivo (el primero es el más habitual).
func (m Model) ImportCandidates(typ string) []string {
	return m.imps[SnippetLang(m.path)].Candidates(typ)
}

// HasImport indica que el tipo typ ya se puede usar en el archivo.
func (m Model) HasImport(typ string) bool {
	return imports.Imported(SnippetLang(m.path), m.lineStrings(), typ, m.ImportCandidates(typ))
}

// ImportSuggestion devuelve el tipo bajo el cursor si tiene import conocido y
// al archivo le falta; "" si no hay nada que sugerir. Se recalcula tras cada
// tecla.
func (m Model) ImportSuggestion() string { return m.suggest }

func (m Model) computeSuggestion() string {
	if len(m.imps) == 0 {
		return ""
	}
	typ := m.TypeAtCursor()
	if typ == "" || len(m.ImportCandidates(typ)) == 0 {
		return ""
	}
	// Las líneas de import, package, using y namespace no se sugieren.
	t := strings.TrimSpace(string(m.buf.Line(m.buf.Cursor().Line)))
	for _, p := range []string{"import ", "package ", "using ", "global using ", "namespace "} {
		if strings.HasPrefix(t, p) {
			return ""
		}
	}
	if m.HasImport(typ) {
		return ""
	}
	return typ
}

// AddImport inserta el import de candidate (nombre completo de la clase en
// Java y Kotlin, namespace en C#) en su sitio, como un único paso de
// deshacer, y deja el cursor donde estaba. Devuelve false si no hizo falta
// insertar nada (ya estaba o lo cubre otro import).
func (m *Model) AddImport(candidate string) bool {
	b := m.buf
	e, ok := imports.Insert(SnippetLang(m.path), m.lineStrings(), candidate)
	if !ok {
		return false
	}
	cur := b.Cursor()
	pos, text, shift := editor.Pos{Line: e.Line}, e.Text, strings.Count(e.Text, "\n")
	if e.Line > cur.Line {
		shift = 0 // se inserta tras el cursor: no se mueve
	}
	if e.Line >= b.LineCount() {
		// Sin línea siguiente: se añade tras la última.
		last := b.LineCount() - 1
		pos = editor.Pos{Line: last, Col: len(b.Line(last))}
		text = "\n" + strings.TrimSuffix(e.Text, "\n")
	}
	m.snip = nil // los offsets de la sesión dejan de valer
	b.ReplaceRange(pos, pos, text)
	m.edited()
	b.SetCursorPos(editor.Pos{Line: cur.Line + shift, Col: cur.Col})
	m.suggest = m.computeSuggestion()
	return true
}
