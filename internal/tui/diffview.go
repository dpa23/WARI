package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/dpa23/wari/internal/tui/theme"

	"github.com/dpa23/wari/internal/orch"
)

// Vista de solo lectura del diff de un turno para el panel central: filas con
// números de línea de ambos lados, color por operación, desplazamiento
// vertical y horizontal y salto entre hunks. El Panel guarda el estado
// (diffView); el layout lo dibuja con DiffTitle y DiffLines.

const (
	// Ancho mínimo de código que justifica mostrar los dos números de línea;
	// con menos, se muestra solo uno y, con menos aún, ninguno.
	minDiffCodeBoth   = 24
	minDiffCodeSingle = 16
	// defaultDiffW y defaultDiffH rigen hasta que el layout fija el área.
	defaultDiffW = 80
	defaultDiffH = 24
)

// diffRow es una fila del diff ya interpretado.
type diffRow struct {
	// op: 'h' cabecera (---/+++), '@' hunk, ' ' contexto, '+', '-' y 'm'
	// mensaje (texto que no es un diff).
	op byte
	// old y new son los números de línea (1-based; 0 si no aplica).
	old, new int
	text     string
}

// parseDiff interpreta un diff unificado de unifiedDiff. Cualquier otro texto
// (mensajes de error, "sin cambios", binario) se muestra tal cual.
func parseDiff(text string) (rows []diffRow, hunks []int) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "--- ") || !strings.HasPrefix(lines[1], "+++ ") {
		for _, l := range lines {
			rows = append(rows, diffRow{op: 'm', text: l})
		}
		return rows, nil
	}
	rows = append(rows, diffRow{op: 'h', text: lines[0]}, diffRow{op: 'h', text: lines[1]})
	oldN, newN := 0, 0
	for _, l := range lines[2:] {
		if strings.HasPrefix(l, "@@") {
			oldN, newN = parseHunkStarts(l)
			hunks = append(hunks, len(rows))
			rows = append(rows, diffRow{op: '@', text: l})
			continue
		}
		if l == "" { // no debería ocurrir: toda línea lleva su operación
			continue
		}
		r := diffRow{op: l[0], text: l[1:]}
		switch r.op {
		case '-':
			r.old = oldN
			oldN++
		case '+':
			r.new = newN
			newN++
		default:
			r.op = ' '
			r.old, r.new = oldN, newN
			oldN++
			newN++
		}
		rows = append(rows, r)
	}
	return rows, hunks
}

// parseHunkStarts extrae las líneas iniciales de "@@ -a,b +c,d @@".
func parseHunkStarts(h string) (oldStart, newStart int) {
	f := strings.Fields(h)
	if len(f) < 3 {
		return 1, 1
	}
	num := func(s string) int {
		s = strings.TrimLeft(s, "+-")
		if i := strings.IndexByte(s, ','); i >= 0 {
			s = s[:i]
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 1
		}
		return n
	}
	return num(f[1]), num(f[2])
}

// diffView es el diff abierto del agente v. Se muestra en el panel central;
// el historial del agente no se toca.
type diffView struct {
	v    *agentView
	edit orch.Edit
	// text es el diff unificado; vacío mientras se calcula.
	text string

	rows  []diffRow
	hunks []int // índices en rows de las filas "@@"
	numW  int   // dígitos del número de línea más largo
	maxW  int   // ancho del código más ancho

	// yoff y xoff son los desplazamientos; cur, el hunk actual.
	yoff, xoff, cur int
	// w y h son el área donde se dibuja (la fija el layout).
	w, h int
	rm   *revertModal
}

// setText fija el diff y lo interpreta, volviendo al principio.
func (d *diffView) setText(text string) {
	d.text = text
	d.rows, d.hunks = parseDiff(text)
	d.yoff, d.xoff, d.cur = 0, 0, 0
	d.numW, d.maxW = 1, 0
	for _, r := range d.rows {
		d.numW = max(d.numW, len(strconv.Itoa(max(r.old, r.new))))
		if r.op == '+' || r.op == '-' || r.op == ' ' || r.op == 'm' {
			d.maxW = max(d.maxW, lipgloss.Width(cleanDiffText(r.text)))
		}
	}
}

// setArea fija el tamaño del área y reacomoda los desplazamientos.
func (d *diffView) setArea(w, h int) {
	d.w, d.h = w, h
	d.clamp()
}

func (d *diffView) area() (w, h int) {
	w, h = d.w, d.h
	if w <= 0 && h <= 0 {
		return defaultDiffW, defaultDiffH
	}
	return max(w, 0), max(h, 0)
}

// footerRows es 1 si cabe una línea de ayuda o confirmación bajo el diff.
func (d *diffView) footerRows() int {
	if _, h := d.area(); h >= 2 {
		return 1
	}
	return 0
}

func (d *diffView) bodyH() int {
	_, h := d.area()
	return max(h-d.footerRows(), 1)
}

// gutter es cuántas columnas ocupan los números de línea y la marca (+/-) y
// si se muestran los dos números (2), uno (1) o ninguno (0).
func (d *diffView) gutter() (cols, mode int) {
	w, _ := d.area()
	both := 2*d.numW + 3
	single := d.numW + 2
	switch {
	case w-both >= minDiffCodeBoth:
		return both, 2
	case w-single >= minDiffCodeSingle:
		return single, 1
	}
	return 1, 0
}

func (d *diffView) maxY() int { return max(len(d.rows)-d.bodyH(), 0) }

func (d *diffView) maxX() int {
	w, _ := d.area()
	cols, _ := d.gutter()
	return max(d.maxW-max(w-cols, 1), 0)
}

func (d *diffView) clamp() {
	d.yoff = min(max(d.yoff, 0), d.maxY())
	d.xoff = min(max(d.xoff, 0), d.maxX())
	d.cur = min(max(d.cur, 0), max(len(d.hunks)-1, 0))
}

// scroll desplaza dy filas y dx columnas y recalcula el hunk actual.
func (d *diffView) scroll(dy, dx int) {
	d.yoff += dy
	d.xoff += dx
	d.clamp()
	// El hunk actual es el último cuya cabecera quedó arriba de la vista; al
	// llegar al final del diff, el último (puede no haber más desplazamiento).
	d.cur = 0
	for i, r := range d.hunks {
		if r <= d.yoff {
			d.cur = i
		}
	}
	if d.maxY() > 0 && d.yoff == d.maxY() && len(d.hunks) > 0 {
		d.cur = len(d.hunks) - 1
	}
}

// gotoHunk salta al hunk i (acotado) dejándolo arriba, salvo que el final del
// diff no deje más margen.
func (d *diffView) gotoHunk(i int) {
	if len(d.hunks) == 0 {
		return
	}
	d.cur = min(max(i, 0), len(d.hunks)-1)
	d.yoff = d.hunks[d.cur]
	d.xoff = 0
	d.clamp()
}

// cleanDiffText deja el texto de una línea listo para pintar: tabuladores a
// espacios y los caracteres de control (que moverían el cursor o abrirían
// secuencias de escape) como un punto medio.
func cleanDiffText(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			sb.WriteString("    ")
		case unicode.IsControl(r):
			sb.WriteRune('·')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// cutCells devuelve las n celdas de s que siguen a las primeras skip. Un
// carácter ancho cortado por el borde izquierdo se cambia por espacios.
func cutCells(s string, skip, n int) string {
	if n <= 0 {
		return ""
	}
	var sb strings.Builder
	col, taken := 0, 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if col < skip {
			col += rw
			if col > skip {
				gap := min(col-skip, n)
				sb.WriteString(strings.Repeat(" ", gap))
				taken += gap
			}
			continue
		}
		if taken+rw > n {
			break
		}
		sb.WriteRune(r)
		taken += rw
	}
	return sb.String()
}

// padTo rellena s con espacios hasta w celdas (s ya no pasa de w).
func padTo(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// lines dibuja exactamente h filas de w celdas: el diff y, si cabe, la línea
// de ayuda o de confirmación al final.
func (d *diffView) lines(st styles, km KeyMap, armedText, armedShort string, w, h int) []string {
	d.setArea(w, h)
	out := make([]string, 0, h)
	if w <= 0 || h <= 0 {
		return out
	}
	if d.text == "" {
		out = append(out, st.info.Render(padTo(cutCells("calculando el diff…", 0, w), w)))
	} else {
		cols, mode := d.gutter()
		codeW := max(w-cols, 0)
		bodyH := d.bodyH()
		for i := d.yoff; i < len(d.rows) && len(out) < bodyH; i++ {
			out = append(out, d.renderRow(st, d.rows[i], i, w, cols, mode, codeW))
		}
	}
	for len(out) < d.bodyH() {
		out = append(out, strings.Repeat(" ", w))
	}
	if d.rm != nil {
		modalLines := d.renderModalLines(w)
		mH := len(modalLines)
		if len(out) >= mH {
			start := len(out) - mH
			for idx, ml := range modalLines {
				out[start+idx] = ml
			}
		}
	}
	if d.footerRows() > 0 {
		out = append(out, d.footer(st, km, armedText, armedShort, w))
	}
	return out[:h]
}

func (d *diffView) renderModalLines(w int) []string {
	if d.rm == nil {
		return nil
	}
	rm := d.rm
	var title, scopeStr string
	switch rm.kind {
	case revertFile:
		title = "Revertir archivo"
		scopeStr = fmt.Sprintf("Archivo: %s (turno %d)", rm.path, rm.turn)
	case revertTurn:
		title = fmt.Sprintf("Revertir turno %d", rm.turn)
		scopeStr = fmt.Sprintf("Archivos afectados (%d): %s", len(rm.files), strings.Join(rm.files, ", "))
	default:
		title = fmt.Sprintf("Revertir agente %s", rm.name)
		scopeStr = fmt.Sprintf("Archivos afectados (%d): %s", len(rm.files), strings.Join(rm.files, ", "))
	}

	if w < 8 {
		return nil
	}
	boxW := min(max(w-4, 12), min(70, w))
	if boxW <= 0 {
		return nil
	}

	header := fmt.Sprintf("┌─ %s ", title)
	if pad := boxW - lipgloss.Width(header) - 1; pad > 0 {
		header += strings.Repeat("─", pad) + "┐"
	} else {
		header = cutCells(header, 0, boxW-1) + "┐"
	}

	body1 := fmt.Sprintf("│ %s", cutCells(scopeStr, 0, max(boxW-3, 0)))
	if pad := boxW - lipgloss.Width(body1) - 1; pad > 0 {
		body1 += strings.Repeat(" ", pad) + "│"
	} else {
		body1 = cutCells(body1, 0, boxW-1) + "│"
	}

	body2 := "│ [Enter] Confirmar    [Esc] Cancelar"
	if boxW < 38 {
		body2 = "│ [Enter] ok · [Esc] cancelar"
	}
	if pad := boxW - lipgloss.Width(body2) - 1; pad > 0 {
		body2 += strings.Repeat(" ", pad) + "│"
	} else {
		body2 = cutCells(body2, 0, boxW-1) + "│"
	}

	footer := "└" + strings.Repeat("─", max(boxW-2, 0)) + "┘"

	style := lipgloss.NewStyle().Bold(true).Foreground(theme.AdaptiveColor{
		Light: lipgloss.Color("#b45309"),
		Dark:  lipgloss.Color("#fbbf24"),
	}).Background(lipgloss.Color("236"))

	leftPad := max((w-boxW)/2, 0)
	padStr := strings.Repeat(" ", leftPad)

	return []string{
		padTo(padStr+style.Render(header), w),
		padTo(padStr+style.Render(body1), w),
		padTo(padStr+style.Render(body2), w),
		padTo(padStr+style.Render(footer), w),
	}
}

func (d *diffView) renderRow(st styles, r diffRow, idx, w, gutterCols, mode, codeW int) string {
	switch r.op {
	case 'h':
		return st.diffHead.Render(padTo(cutCells(cleanDiffText(r.text), 0, w), w))
	case '@':
		style := st.diffHunk
		if len(d.hunks) > 0 && idx == d.hunks[d.cur] {
			style = style.Bold(true).Reverse(true)
		}
		return style.Render(padTo(cutCells(r.text, 0, w), w))
	case 'm':
		return st.info.Render(padTo(cutCells(cleanDiffText(r.text), d.xoff, w), w))
	}
	var style lipgloss.Style
	switch r.op {
	case '+':
		style = st.diffAdd
	case '-':
		style = st.diffDel
	default:
		style = st.diffCtx
	}
	num := func(n int) string {
		if n == 0 {
			return strings.Repeat(" ", d.numW)
		}
		return fmt.Sprintf("%*d", d.numW, n)
	}
	var gut string
	switch mode {
	case 2:
		gut = num(r.old) + " " + num(r.new) + " "
	case 1:
		n := r.new
		if r.op == '-' {
			n = r.old
		}
		gut = num(n) + " "
	}
	code := string(r.op) + cutCells(cleanDiffText(r.text), d.xoff, max(codeW-1, 0))
	if mode == 0 {
		code = string(r.op) + cutCells(cleanDiffText(r.text), d.xoff, max(w-1, 0))
	}
	line := ""
	if gut != "" {
		line = st.diffCtx.Render(gut)
	}
	body := padTo(code, w-lipgloss.Width(gut))
	return line + style.Render(body)
}

// footer es la confirmación pendiente o, si no hay, la ayuda de la vista.
func (d *diffView) footer(st styles, km KeyMap, armedText, armedShort string, w int) string {
	if d.rm != nil {
		s := "¿Confirmar reversión? [Enter] Confirmar · [Esc] Cancelar"
		return st.warn.Render(padTo(cutCells(s, 0, w), w))
	}
	if armedText != "" {
		s := armedText
		if lipgloss.Width(s) > w && armedShort != "" {
			s = armedShort
		}
		return st.warn.Render(padTo(cutCells(s, 0, w), w))
	}
	hk := func(b key.Binding, desc string) string {
		if !b.Enabled() {
			return ""
		}
		return b.Help().Key + " " + desc
	}
	parts := []string{"n/p hunk", "↑↓←→ pgup/pgdn"}
	for _, p := range []string{
		hk(km.RevertFile, "archivo"),
		hk(km.RevertTurn, "turno"),
		hk(km.RevertAgent, "agente"),
		hk(km.DiffFile, "siguiente"),
	} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	// Con poco ancho se descarta desde el final lo que no cabe, pero la tecla
	// de volver se conserva siempre.
	esc := hk(km.Cancel, "volver")
	join := func(rest []string) string {
		if esc != "" {
			rest = append(append([]string{}, rest...), esc)
		}
		return strings.Join(rest, " · ")
	}
	for len(parts) > 0 && lipgloss.Width(join(parts)) > w {
		parts = parts[:len(parts)-1]
	}
	return st.status.Render(padTo(cutCells(join(parts), 0, w), w))
}
