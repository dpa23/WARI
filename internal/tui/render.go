package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// entryKind distingue las líneas del historial para darles estilo.
type entryKind int

const (
	kindUser entryKind = iota
	kindAssistant
	kindTool
	kindEdit
	kindDenied
	kindError
	kindInfo
)

// entry es un bloque del historial.
type entry struct {
	kind entryKind
	text string
}

// maxSummary es el largo máximo, en runas, del resumen de una herramienta.
const maxSummary = 80

// summaryPrefix es cuántos bytes del Input se miran para resumirlo.
const summaryPrefix = 512

// styles agrupa los estilos de lipgloss del panel.
type styles struct {
	user      lipgloss.Style
	assistant lipgloss.Style
	tool      lipgloss.Style
	edit      lipgloss.Style
	denied    lipgloss.Style
	err       lipgloss.Style
	info      lipgloss.Style
	status    lipgloss.Style
}

func defaultStyles() styles {
	return styles{
		user: lipgloss.NewStyle().
			Foreground(lipgloss.Color("12")).
			BorderStyle(lipgloss.Border{Left: "▎"}).
			BorderLeft(true).
			BorderForeground(lipgloss.Color("12")).
			PaddingLeft(1),
		assistant: lipgloss.NewStyle(),
		tool:      lipgloss.NewStyle().Faint(true),
		edit:      lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Faint(true),
		denied:    lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		err:       lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		info:      lipgloss.NewStyle().Faint(true).Italic(true),
		status:    lipgloss.NewStyle().Faint(true),
	}
}

func (s styles) forKind(k entryKind) lipgloss.Style {
	switch k {
	case kindUser:
		return s.user
	case kindTool:
		return s.tool
	case kindEdit:
		return s.edit
	case kindDenied:
		return s.denied
	case kindError:
		return s.err
	case kindInfo:
		return s.info
	}
	return s.assistant
}

// separator es lo que va antes de la entrada i: nada para la primera, un salto
// de línea para el resto y una línea en blanco extra alrededor de los mensajes
// del usuario.
func separator(entries []entry, i int) string {
	if i == 0 {
		return ""
	}
	if entries[i].kind == kindUser || entries[i-1].kind == kindUser {
		return "\n\n"
	}
	return "\n"
}

// renderEntry compone una entrada ajustada a width columnas.
func renderEntry(e entry, st styles, width int) string {
	return st.forKind(e.kind).Width(max(width, 1)).Render(strings.TrimRight(e.text, "\n"))
}

// renderEntries compone el historial completo ajustado a width columnas, sin
// caché. Los mensajes del usuario van separados del resto por una línea en
// blanco.
func renderEntries(entries []entry, st styles, width int) string {
	var b strings.Builder
	for i, e := range entries {
		b.WriteString(separator(entries, i))
		b.WriteString(renderEntry(e, st, width))
	}
	return b.String()
}

// historyCache guarda el render de las entradas cerradas (las que ya no
// cambian) para que cada TextDelta solo re-renderice la entrada abierta.
type historyCache struct {
	width  int    // ancho con el que se renderizó prefix
	done   int    // entradas incluidas en prefix
	prefix string // render de entries[:done], con sus separadores
}

// render devuelve el render de entries. open indica que la última entrada
// sigue abierta: se renderiza siempre y no entra en la caché.
func (c *historyCache) render(entries []entry, open bool, st styles, width int) string {
	width = max(width, 1)
	if width != c.width {
		*c = historyCache{width: width}
	}
	closed := len(entries)
	if open && closed > 0 {
		closed--
	}
	if c.done > closed { // el historial se acortó: no debería pasar
		*c = historyCache{width: width}
	}
	if c.done < closed {
		var b strings.Builder
		b.WriteString(c.prefix)
		for ; c.done < closed; c.done++ {
			b.WriteString(separator(entries, c.done))
			b.WriteString(renderEntry(entries[c.done], st, width))
		}
		c.prefix = b.String()
	}
	if closed == len(entries) {
		return c.prefix
	}
	return c.prefix + separator(entries, closed) + renderEntry(entries[closed], st, width)
}

// summarize compacta el Input JSON de una herramienta en una sola línea de
// como máximo maxSummary runas.
func summarize(input json.RawMessage) string {
	raw := bytes.TrimSpace(input)
	if len(raw) == 0 {
		return ""
	}
	// Un Write puede traer MB: basta un prefijo para un resumen de maxSummary
	// runas. Cortado, ya no es JSON válido y se usa tal cual.
	if len(raw) > summaryPrefix {
		raw = raw[:summaryPrefix]
		for len(raw) > 0 && !utf8.Valid(raw) {
			raw = raw[:len(raw)-1]
		}
	}
	var buf bytes.Buffer
	var s string
	if err := json.Compact(&buf, raw); err == nil {
		s = buf.String()
	} else {
		s = string(raw)
	}
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, maxSummary)
}

// truncate recorta s a n runas, marcando el corte con "…".
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// toolSummary arma el texto "<Name> <resumen>" sin el prefijo.
func toolSummary(name string, input json.RawMessage) string {
	if sum := summarize(input); sum != "" {
		return name + " " + sum
	}
	return name
}

// shortSession devuelve los primeros 8 caracteres del ID de sesión.
func shortSession(id string) string {
	r := []rune(id)
	if len(r) > 8 {
		r = r[:8]
	}
	return string(r)
}

// formatUsage da el resumen de consumo de la línea de estado.
func formatUsage(in, out int64, cost float64) string {
	return fmt.Sprintf("↑%d ↓%d · US$%.4f", in, out, cost)
}
