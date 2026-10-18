package editorview

// Marcas de depuración del margen (fase 14): breakpoints y la línea donde está
// detenido el depurado. Las pone quien integra el depurador; el editor solo
// las dibuja. En el margen, una línea con varias marcas muestra la de mayor
// prioridad: la línea actual, luego el breakpoint y luego el diagnóstico del
// LSP.

// BreakpointMark es el estado de un breakpoint en el margen.
type BreakpointMark int

// Estados de un breakpoint.
const (
	// BreakpointNone: sin breakpoint.
	BreakpointNone BreakpointMark = iota
	// BreakpointPending: puesto, pero el adaptador aún no lo verificó (o no
	// hay sesión de depuración).
	BreakpointPending
	// BreakpointVerified: el adaptador lo aceptó.
	BreakpointVerified
)

const (
	sgrExecGlyph   = "\x1b[38;2;250;220;80m"
	sgrBreakGlyph  = "\x1b[38;2;244;71;71m"
	sgrBreakWait   = "\x1b[38;2;200;120;120m"
	sgrExecLineBg  = "\x1b[48;2;70;62;20m"
	glyphExec      = "▶"
	glyphBreak     = "◉"
	glyphBreakWait = "○"
)

// SetBreakpoints fija los breakpoints del margen por línea (base 0); nil los
// quita. El mapa no se copia: el llamador debe pasar uno nuevo en cada cambio
// y no modificarlo después.
func (m *Model) SetBreakpoints(marks map[int]BreakpointMark) { m.bps = marks }

// SetExecLine resalta la línea (base 0) donde está detenido el depurado, con
// una flecha en el margen y el fondo de la línea; un valor negativo la quita.
func (m *Model) SetExecLine(line int) {
	if line < 0 {
		m.exec = 0
		return
	}
	m.exec = line + 1
}

// ExecLine devuelve la línea resaltada (base 0) o -1 si no hay.
func (m Model) ExecLine() int { return m.exec - 1 }

// hasGutterMarks indica si alguna marca (LSP o depuración) ocupa el margen.
func (m Model) hasGutterMarks() bool {
	return len(m.marks) > 0 || len(m.bps) > 0 || m.exec > 0
}

// debugGlyph devuelve el glifo de depuración de la línea li, o "" si no tiene.
func (m Model) debugGlyph(li int) string {
	if m.exec > 0 && li == m.exec-1 {
		return sgrExecGlyph + glyphExec
	}
	switch m.bps[li] {
	case BreakpointVerified:
		return sgrBreakGlyph + glyphBreak
	case BreakpointPending:
		return sgrBreakWait + glyphBreakWait
	}
	return ""
}
