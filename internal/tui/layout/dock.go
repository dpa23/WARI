package layout

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dpa23/wari/internal/tui/theme"
)

// El dock es la franja bajo el editor con lo de ejecutar y depurar: la salida
// de la ejecución en el sandbox (con su tecla de parada siempre visible en el
// título), la pila y las variables del depurado, y los avisos de la
// depuración (adaptador ausente, falta .wari/launch.json). Aparece al lanzar
// algo y se cierra con esc (con el foco en él) cuando no queda nada en
// marcha. Cuando tiene el foco:
//
//   - o, p y v eligen la sección: salida, pila o variables;
//   - arriba y abajo mueven la selección (en la salida, el desplazamiento;
//     end vuelve al final), re pág y av pág van de diez en diez;
//   - en la pila, enter elige el marco (salta a su línea y recarga las variables);
//   - en las variables, derecha o enter expanden, izquierda pliega (o sube al
//     padre) y enter alterna.

// dockSub es la sección del dock con la selección.
type dockSub int

const (
	dockOut dockSub = iota
	dockStack
	dockVars
)

// dockState es el estado del dock.
type dockState struct {
	open bool
	sub  dockSub
	// outOff son las líneas por encima del final que muestra la salida (0 la
	// deja siguiendo al final).
	outOff int
}

// outLine es una línea del panel de salida.
type outLine struct {
	stream string // "stdout", "stderr", "runtime" o "debug"
	text   string
}

// maxOutLines es el tope de líneas de salida retenidas.
const maxOutLines = 2000

var (
	ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?|\x1b[@-Z\\-_]`)

	outErrStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	outRTStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("179"))
	outDebugStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110"))
	selRowStyle   = lipgloss.NewStyle().Reverse(true)
	boldStyle     = lipgloss.NewStyle().Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
	askStyle      = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("0")).Background(theme.AdaptiveColor{
		Light: lipgloss.Color("#f59e0b"),
		Dark:  lipgloss.Color("#fbbf24"),
	})
)

// sanitizeLine deja una línea de la salida de un proceso como texto plano
// seguro para dibujar: sin secuencias de escape (el contenedor o el depurado
// no deben poder mover el cursor ni cambiar el terminal), sin otros
// caracteres de control y con las tabulaciones como espacios.
func sanitizeLine(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			sb.WriteString("    ")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == utf8.RuneError:
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// appendOut añade texto (de una o varias líneas) al panel de salida.
func (m *Model) appendOut(stream, text string) {
	for _, l := range strings.Split(strings.TrimRight(text, "\r\n"), "\n") {
		m.out = append(m.out, outLine{stream: stream, text: sanitizeLine(l)})
		if m.dock.outOff > 0 {
			m.dock.outOff++ // la vista no se mueve mientras se lee hacia atrás
		}
	}
	if over := len(m.out) - maxOutLines; over > 0 {
		m.out = append([]outLine(nil), m.out[over:]...)
		m.dock.outOff = max(0, min(m.dock.outOff, len(m.out)-1))
	}
}

// openDock muestra el dock.
func (m *Model) openDock() {
	if !m.dock.open {
		m.dock.open = true
		m.relayout()
	}
}

// closeDock oculta el dock.
func (m *Model) closeDock() {
	if m.dock.open {
		m.dock.open = false
		m.dbg.notice = nil
		m.relayout()
	}
}

// busyDock indica que algo del dock sigue en marcha (no se cierra con esc).
func (m Model) busyDock() bool {
	return m.run.active || m.run.checking || m.run.confirm != nil || m.dbg.sess != nil || m.dbg.starting
}

// dockHeight es la altura del dock (con su título) para un cuerpo de bodyH
// filas: un tercio, entre 6 y 16, y 0 si el editor quedaría con menos de 4.
func dockHeight(bodyH int) int {
	h := min(max(bodyH/3, 6), 16)
	if bodyH-h < 4 {
		h = bodyH - 4
	}
	if h < 3 {
		return 0
	}
	return h
}

// --- Secciones -----------------------------------------------------------------------

type secKind int

const (
	secOut secKind = iota
	secStack
	secVars
	secNote
)

type dockSec struct {
	kind   secKind
	weight int
	w      int
}

// sections decide qué secciones se muestran, de izquierda a derecha.
func (m Model) sections() []dockSec {
	var secs []dockSec
	if len(m.out) > 0 || m.run.active || m.run.checking || m.run.confirm != nil {
		secs = append(secs, dockSec{kind: secOut, weight: 3})
	}
	if m.dbg.sess != nil || m.dbg.starting {
		secs = append(secs, dockSec{kind: secStack, weight: 2}, dockSec{kind: secVars, weight: 3})
	} else if len(m.dbg.notice) > 0 {
		secs = append(secs, dockSec{kind: secNote, weight: 3})
	}
	if len(secs) == 0 {
		secs = append(secs, dockSec{kind: secOut, weight: 1})
	}
	return secs
}

const secMin = 16

// layoutSecs reparte w celdas (con un separador entre secciones). Si no caben
// todas con secMin cada una, descarta primero la pila y luego la salida.
func layoutSecs(secs []dockSec, w int) []dockSec {
	drop := func(k secKind) bool {
		for i, s := range secs {
			if s.kind == k {
				secs = append(append([]dockSec(nil), secs[:i]...), secs[i+1:]...)
				return true
			}
		}
		return false
	}
	for len(secs) > 1 && (w-(len(secs)-1))/len(secs) < secMin {
		if !drop(secStack) && !drop(secOut) {
			secs = secs[len(secs)-1:]
		}
	}
	total := 0
	for _, s := range secs {
		total += s.weight
	}
	avail := w - (len(secs) - 1)
	used := 0
	out := make([]dockSec, len(secs))
	for i, s := range secs {
		s.w = avail * s.weight / total
		if i == len(secs)-1 {
			s.w = avail - used
		}
		used += s.w
		out[i] = s
	}
	return out
}

// dockView dibuja el dock: h filas de w celdas, la primera con los títulos.
func (m Model) dockView(w, h int) []string {
	secs := layoutSecs(m.sections(), w)
	rows := h - 1
	sep := sepStyle.Render("│")
	bodies := make([][]string, len(secs))
	var head strings.Builder
	for i, s := range secs {
		if i > 0 {
			head.WriteString(sep)
		}
		title, focused := m.secTitle(s.kind)
		head.WriteString(header(title, focused, s.w))
		bodies[i] = block(strings.Join(m.secBody(s.kind, s.w, rows), "\n"), s.w, rows)
	}
	out := make([]string, 0, h)
	out = append(out, head.String())
	for r := 0; r < rows; r++ {
		var sb strings.Builder
		for i := range secs {
			if i > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(bodies[i][r])
		}
		out = append(out, sb.String())
	}
	return out
}

func (m Model) secTitle(k secKind) (string, bool) {
	on := func(s dockSub) bool { return m.focus == focusDock && m.dock.sub == s }
	switch k {
	case secOut:
		var t string
		switch {
		case m.run.active:
			t = "■ " + m.firstKey("run_stop") + " detener · Salida " + m.run.profile
		case m.run.confirm != nil || m.run.checking:
			t = "Salida " + m.run.profile
		default:
			t = "Salida"
		}
		return t, on(dockOut)
	case secStack:
		return "Pila · " + m.dbgStatus(), on(dockStack)
	case secVars:
		if m.focus == focusDock {
			return "Variables · ↑↓ ←→ enter · o salida · p pila", on(dockVars)
		}
		if k := m.firstKey("focus_dock"); k != "" {
			return "Variables · " + k + " enfocar", false
		}
		return "Variables", false
	}
	return "Depuración", false
}

func (m Model) secBody(k secKind, w, rows int) []string {
	switch k {
	case secOut:
		return m.bodyOut(w, rows)
	case secStack:
		return m.bodyStack(w, rows)
	case secVars:
		return m.bodyVars(w, rows)
	}
	lines := make([]string, 0, len(m.dbg.notice))
	for _, l := range m.dbg.notice {
		lines = append(lines, fit(" "+l, w))
	}
	return lines
}

func (m Model) bodyOut(w, rows int) []string {
	ask := m.run.confirm != nil
	if ask {
		rows--
	}
	end := len(m.out) - min(m.dock.outOff, max(len(m.out)-1, 0))
	start := max(0, end-rows)
	lines := make([]string, 0, rows+1)
	for _, l := range m.out[start:end] {
		text := fit(" "+l.text, w)
		switch l.stream {
		case "stderr":
			text = outErrStyle.Render(text)
		case "runtime":
			text = outRTStyle.Render(text)
		case "debug":
			text = outDebugStyle.Render(text)
		}
		lines = append(lines, text)
	}
	if ask {
		for len(lines) < rows {
			lines = append(lines, "")
		}
		q := " ¿Permitir red? y: sí (bridge) · n: sin red · esc: cancelar "
		lines = append(lines, askStyle.Render(fit(q, w)))
	}
	return lines
}

func (m Model) bodyStack(w, rows int) []string {
	if len(m.dbg.frames) == 0 {
		msg := " " + m.dbgStatus() + "…"
		if m.dbg.sess != nil {
			if _, stopped := m.dbg.sess.StoppedThread(); !stopped {
				msg = " en ejecución"
			}
		}
		return []string{faintStyle.Render(fit(msg, w))}
	}
	top := 0
	if m.dbg.stackSel >= rows {
		top = m.dbg.stackSel - rows + 1
	}
	var lines []string
	for i := top; i < len(m.dbg.frames) && len(lines) < rows; i++ {
		f := m.dbg.frames[i]
		mark := "  "
		if i == m.dbg.frame {
			mark = "▶ "
		}
		text := mark + f.Name
		if f.Source != nil && f.Source.Path != "" {
			text += "  " + filepath.Base(f.Source.Path)
			if f.Line > 0 {
				text += fmt.Sprintf(":%d", f.Line)
			}
		}
		text = fit(" "+sanitizeLine(text), w)
		if i == m.dbg.stackSel && m.focus == focusDock && m.dock.sub == dockStack {
			text = selRowStyle.Render(text)
		}
		lines = append(lines, text)
	}
	return lines
}

func (m Model) bodyVars(w, rows int) []string {
	if len(m.dbg.rows) == 0 {
		return []string{faintStyle.Render(fit(" sin variables", w))}
	}
	top := 0
	if m.dbg.varSel >= rows {
		top = m.dbg.varSel - rows + 1
	}
	var lines []string
	for i := top; i < len(m.dbg.rows) && len(lines) < rows; i++ {
		r := m.dbg.rows[i]
		marker := "  "
		switch {
		case r.expandable && r.expanded:
			marker = "▾ "
		case r.expandable:
			marker = "▸ "
		}
		text := strings.Repeat("  ", r.depth) + marker + r.name
		if !r.scope {
			text += " = " + r.value
			if r.typ != "" {
				text += "  (" + r.typ + ")"
			}
		}
		text = fit(" "+sanitizeLine(text), w)
		switch {
		case i == m.dbg.varSel && m.focus == focusDock && m.dock.sub == dockVars:
			text = selRowStyle.Render(text)
		case r.scope:
			text = boldStyle.Render(text)
		}
		lines = append(lines, text)
	}
	return lines
}

// --- Teclas ---------------------------------------------------------------------------

// dockKey atiende una tecla con el foco en el dock.
func (m Model) dockKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case m.is(k, "cancel"):
		if m.busyDock() {
			return m.focusPanel(focusEditor), nil
		}
		m.closeDock()
		return m, nil
	case k.String() == "o":
		m.dock.sub = dockOut
		return m, nil
	case k.String() == "p":
		m.dock.sub = dockStack
		return m, nil
	case k.String() == "v":
		m.dock.sub = dockVars
		return m, nil
	}
	switch m.dock.sub {
	case dockOut:
		m.dockOutKey(k)
	case dockStack:
		return m.stackKey(k)
	case dockVars:
		return m.varsKey(k)
	}
	return m, nil
}

// dockPage es cuánto mueven re pág y av pág.
const dockPage = 10

// dockOutKey desplaza la salida.
func (m *Model) dockOutKey(k tea.KeyPressMsg) {
	maxOff := max(len(m.out)-1, 0)
	switch {
	case key.Matches(k, palKeys.up):
		m.dock.outOff = min(m.dock.outOff+1, maxOff)
	case key.Matches(k, palKeys.down):
		m.dock.outOff = max(m.dock.outOff-1, 0)
	case m.is(k, "page_up"):
		m.dock.outOff = min(m.dock.outOff+dockPage, maxOff)
	case m.is(k, "page_down"):
		m.dock.outOff = max(m.dock.outOff-dockPage, 0)
	case k.String() == "end":
		m.dock.outOff = 0
	}
}
