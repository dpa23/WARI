package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/dpa23/wari/internal/tui/theme"
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
	kindWarn
)

// entry es un bloque del historial.
type entry struct {
	kind entryKind
	text string
}

// maxSummary es el largo máximo, en runas, del resumen de una herramienta.
const maxSummary = 600

// summaryPrefix es cuántos bytes del Input se miran para resumirlo.
const summaryPrefix = 4096

// styles agrupa los estilos de lipgloss del panel.
type styles struct {
	user      lipgloss.Style
	assistant lipgloss.Style
	tool      lipgloss.Style
	edit      lipgloss.Style
	denied    lipgloss.Style
	err       lipgloss.Style
	info      lipgloss.Style
	warn      lipgloss.Style
	status    lipgloss.Style

	// Vista de diff.
	diffHead, diffHunk, diffAdd, diffDel, diffCtx lipgloss.Style
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
		edit: lipgloss.NewStyle().Foreground(theme.AdaptiveColor{
			Light: lipgloss.Color("#92400e"),
			Dark:  lipgloss.Color("#fcd34d"),
		}).Faint(true),
		denied: lipgloss.NewStyle().Foreground(theme.AdaptiveColor{
			Light: lipgloss.Color("#b45309"),
			Dark:  lipgloss.Color("#f59e0b"),
		}),
		err:  lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		info: lipgloss.NewStyle().Faint(true).Italic(true),
		warn: lipgloss.NewStyle().Foreground(theme.AdaptiveColor{
			Light: lipgloss.Color("#b45309"),
			Dark:  lipgloss.Color("#fbbf24"),
		}).Bold(true),
		status: lipgloss.NewStyle().Faint(true),

		diffHead: lipgloss.NewStyle().Bold(true),
		diffHunk: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		diffAdd:  lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		diffDel:  lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		diffCtx:  lipgloss.NewStyle().Faint(true),
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
	case kindWarn:
		return s.warn
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
	s := ""
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		s = readable(v)
	} else {
		// Cortado ya no es JSON válido: se usa tal cual, con los escapes que
		// Go mete por HTML decodificados.
		s = htmlUnescape.Replace(string(raw))
	}
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, maxSummary)
}

var htmlUnescape = strings.NewReplacer(`\u0026`, "&", `\u003c`, "<", `\u003e`, ">")

// summaryKeys son los campos que dicen mejor qué hace una herramienta.
var summaryKeys = []string{"command", "cmd", "file_path", "path", "pattern", "url", "query"}

// readable da el texto de un Input ya decodificado: el valor del campo
// principal (el comando, la ruta) si lo hay, sin las comillas ni los escapes
// de JSON, y si no el objeto compacto sin escapes de HTML.
func readable(v any) string {
	if obj, ok := v.(map[string]any); ok {
		lower := make(map[string]string, len(obj))
		for k, val := range obj {
			if str, ok := val.(string); ok && str != "" {
				lower[strings.ToLower(k)] = str
			}
		}
		for _, k := range summaryKeys {
			if str, ok := lower[k]; ok {
				return str
			}
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
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
// El costo solo aparece si la CLI lo reporta: con suscripción es 0 y lo que
// cuenta es el uso.
func formatUsage(in, out int64, cost float64) string {
	s := fmt.Sprintf("↑%s ↓%s tok", compactTokens(in), compactTokens(out))
	if cost > 0 {
		s += fmt.Sprintf(" · US$%.4f", cost)
	}
	return s
}

// compactTokens abrevia un conteo de tokens: 950, 12.3k, 1.2M.
func compactTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
