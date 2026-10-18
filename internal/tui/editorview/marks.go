package editorview

// LineMark es la marca de una línea en el margen: un problema que un servicio
// externo (el LSP) reporta en ella.
type LineMark int

// Marcas de línea, de menor a mayor gravedad.
const (
	MarkNone LineMark = iota
	MarkWarning
	MarkError
)

const (
	sgrMarkError   = "\x1b[38;2;244;71;71m"
	sgrMarkWarning = "\x1b[38;2;220;180;60m"
)

// SetLineMarks fija las marcas del margen por línea (base 0). Una línea sin
// entrada no se marca; nil las quita todas. El mapa no se copia: el llamador
// debe pasar uno nuevo en cada cambio y no modificarlo después.
func (m *Model) SetLineMarks(marks map[int]LineMark) { m.marks = marks }

// markGlyph devuelve el texto (con su color) que ocupa en el margen la línea
// li: un punto si está marcada y un espacio si no.
func (m Model) markGlyph(li int) string {
	if g := m.debugGlyph(li); g != "" {
		return g
	}
	switch m.marks[li] {
	case MarkError:
		return sgrMarkError + "●"
	case MarkWarning:
		return sgrMarkWarning + "●"
	}
	return " "
}
