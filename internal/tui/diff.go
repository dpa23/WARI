package tui

import (
	"bytes"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	// diffContext son las líneas de contexto alrededor de cada cambio.
	diffContext = 3
	// maxLCSCells es el tamaño máximo de la tabla del LCS (líneas viejas por
	// nuevas, tras recortar lo común al inicio y al final). Por encima, la
	// región cambiada se muestra entera como borrada y agregada.
	maxLCSCells = 4_000_000
)

type diffOp byte

const (
	opSame diffOp = ' '
	opDel  diffOp = '-'
	opAdd  diffOp = '+'
)

type diffLine struct {
	op   diffOp
	text string
}

// splitLines parte en líneas sin el salto final. Un archivo que termina sin
// salto de línea y uno que sí lo tiene dan las mismas líneas: la diferencia
// de ese salto no se muestra.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// lineDiff calcula las operaciones línea a línea entre a y b.
func lineDiff(a, b []string) []diffLine {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]

	out := make([]diffLine, 0, len(a)+len(b))
	for _, l := range a[:pre] {
		out = append(out, diffLine{opSame, l})
	}
	out = append(out, middleDiff(ma, mb)...)
	for _, l := range a[len(a)-suf:] {
		out = append(out, diffLine{opSame, l})
	}
	return out
}

// middleDiff resuelve la región sin prefijo ni sufijo comunes con el LCS.
func middleDiff(a, b []string) []diffLine {
	if len(a) == 0 || len(b) == 0 || len(a)*len(b) > maxLCSCells {
		out := make([]diffLine, 0, len(a)+len(b))
		for _, l := range a {
			out = append(out, diffLine{opDel, l})
		}
		for _, l := range b {
			out = append(out, diffLine{opAdd, l})
		}
		return out
	}
	// lcs[i][j]: largo del LCS de a[i:] y b[j:].
	w := len(b) + 1
	lcs := make([]int32, (len(a)+1)*w)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*w+j] = lcs[(i+1)*w+j+1] + 1
			} else {
				lcs[i*w+j] = max(lcs[(i+1)*w+j], lcs[i*w+j+1])
			}
		}
	}
	var out []diffLine
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{opSame, a[i]})
			i++
			j++
		case lcs[(i+1)*w+j] >= lcs[i*w+j+1]:
			out = append(out, diffLine{opDel, a[i]})
			i++
		default:
			out = append(out, diffLine{opAdd, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, diffLine{opDel, a[i]})
	}
	for ; j < len(b); j++ {
		out = append(out, diffLine{opAdd, b[j]})
	}
	return out
}

// unifiedDiff devuelve el diff unificado de old a cur, con las cabeceras
// "--- oldName" y "+++ newName". Devuelve "" si no hay diferencias. old o cur
// nil significan que el archivo no existía (o ya no existe).
func unifiedDiff(oldName, newName string, old, cur []byte) string {
	a, b := splitLines(old), splitLines(cur)
	ops := lineDiff(a, b)

	changed := false
	for _, l := range ops {
		if l.op != opSame {
			changed = true
			break
		}
	}
	if !changed {
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldName, newName)

	// Posiciones (línea 1-based) en viejo y nuevo antes de cada operación.
	oldPos := make([]int, len(ops)+1)
	newPos := make([]int, len(ops)+1)
	oldPos[0], newPos[0] = 1, 1
	for i, l := range ops {
		oldPos[i+1], newPos[i+1] = oldPos[i], newPos[i]
		if l.op != opAdd {
			oldPos[i+1]++
		}
		if l.op != opDel {
			newPos[i+1]++
		}
	}

	for i := 0; i < len(ops); {
		// Busca el siguiente cambio.
		for i < len(ops) && ops[i].op == opSame {
			i++
		}
		if i >= len(ops) {
			break
		}
		start := max(i-diffContext, 0)
		// Extiende el hunk mientras haya otro cambio a menos de 2*contexto.
		end := i
		for last := i; last < len(ops); last++ {
			if ops[last].op != opSame {
				end = last
				continue
			}
			if last-end > 2*diffContext {
				break
			}
		}
		stop := min(end+1+diffContext, len(ops))
		oldN, newN := 0, 0
		for _, l := range ops[start:stop] {
			if l.op != opAdd {
				oldN++
			}
			if l.op != opDel {
				newN++
			}
		}
		oldStart, newStart := oldPos[start], newPos[start]
		if oldN == 0 {
			oldStart-- // convención de diff: el hunk vacío apunta a la línea previa
		}
		if newN == 0 {
			newStart--
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(oldStart, oldN), hunkRange(newStart, newN))
		for _, l := range ops[start:stop] {
			sb.WriteByte(byte(l.op))
			sb.WriteString(l.text)
			sb.WriteByte('\n')
		}
		i = stop
	}
	return sb.String()
}

func hunkRange(start, n int) string {
	if n == 1 {
		return fmt.Sprint(start)
	}
	return fmt.Sprintf("%d,%d", start, n)
}

// diffText compone el texto del diff de un archivo contra su checkpoint, o un
// mensaje si no hay diferencias o es binario.
func diffText(rel string, turn int, old, cur []byte) string {
	if bytes.IndexByte(old, 0) >= 0 || bytes.IndexByte(cur, 0) >= 0 {
		return "(archivo binario: sin vista de diff)"
	}
	oldName := fmt.Sprintf("a/%s (checkpoint, turno %d)", rel, turn)
	newName := "b/" + rel + " (actual)"
	switch {
	case old == nil && cur == nil:
		return "(el archivo no existía en el checkpoint ni existe ahora)"
	case old == nil:
		oldName = "/dev/null (no existía en el checkpoint)"
	case cur == nil:
		newName = "/dev/null (ya no existe)"
	}
	if d := unifiedDiff(oldName, newName, old, cur); d != "" {
		return d
	}
	return "(sin cambios respecto al checkpoint)"
}

// renderDiff da color al diff unificado y lo ajusta a width columnas.
func renderDiff(text string, st styles, width int) string {
	width = max(width, 1)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.ReplaceAll(l, "\t", "    ")
		var style lipgloss.Style
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			style = st.diffHead
		case strings.HasPrefix(l, "@@"):
			style = st.diffHunk
		case strings.HasPrefix(l, "+"):
			style = st.diffAdd
		case strings.HasPrefix(l, "-"):
			style = st.diffDel
		default:
			style = st.diffCtx
		}
		out = append(out, style.Width(width).Render(l))
	}
	return strings.Join(out, "\n")
}
