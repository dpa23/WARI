package editorview

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/dpa23/wari/internal/editor"
)

// styleName es el estilo de chroma (oscuro y sobrio) del que se toman los
// colores de primer plano. El fondo del estilo se ignora: el editor usa el
// de la terminal.
const styleName = "onedark"

// lookback es el número de líneas anteriores a la ventana que se tokenizan
// junto con ella para que un comentario multilínea que empieza poco antes de
// la primera línea visible se pinte bien.
const lookback = 40

// lexerNames asocia extensiones con alias de lexers de chroma. Fuera de esta
// lista el texto se muestra sin resaltar.
var lexerNames = map[string]string{
	".java": "java",
	".kt":   "kotlin",
	".kts":  "kotlin",
	".cs":   "csharp",
}

// highlighter tokeniza solo las líneas visibles. Imprecisión conocida: como
// no se tokeniza el archivo entero, un comentario o cadena multilínea que
// empieza más de lookback líneas antes de la ventana se pinta como código
// hasta que su inicio entre en la ventana (o en el margen de lookback).
type highlighter struct {
	lexer chroma.Lexer // nil: texto plano
	style *chroma.Style

	mu    sync.Mutex
	index map[chroma.TokenType]uint16
	// cache guarda el último resultado de lines: mientras la ventana no
	// cambie de texto ni de posición (mover el cursor dentro de ella, parpadeo,
	// etc.) no se vuelve a tokenizar.
	cache struct {
		top  int
		text string
		out  [][]uint16
	}
	// sgr[i] es la secuencia SGR del estilo i; sgr[0] es el estilo por defecto.
	sgr []string
}

func newHighlighter(path string) *highlighter {
	h := &highlighter{
		style: styles.Get(styleName),
		index: map[chroma.TokenType]uint16{},
		sgr:   []string{""},
	}
	if name, ok := lexerNames[strings.ToLower(filepath.Ext(path))]; ok {
		if lx := lexers.Get(name); lx != nil {
			h.lexer = chroma.Coalesce(lx)
		}
	}
	return h
}

// styleOf devuelve el índice del estilo del tipo de token t.
func (h *highlighter) styleOf(t chroma.TokenType) uint16 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i, ok := h.index[t]; ok {
		return i
	}
	e := h.style.Get(t)
	var parts []string
	if e.Colour.IsSet() {
		parts = append(parts, fmt.Sprintf("38;2;%d;%d;%d", e.Colour.Red(), e.Colour.Green(), e.Colour.Blue()))
	}
	if e.Bold == chroma.Yes {
		parts = append(parts, "1")
	}
	if e.Italic == chroma.Yes {
		parts = append(parts, "3")
	}
	if e.Underline == chroma.Yes {
		parts = append(parts, "4")
	}
	var i uint16
	if len(parts) > 0 {
		i = uint16(len(h.sgr))
		h.sgr = append(h.sgr, "\x1b["+strings.Join(parts, ";")+"m")
	}
	h.index[t] = i
	return i
}

// sgrOf devuelve la secuencia SGR del estilo i.
func (h *highlighter) sgrOf(i uint16) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sgr[i]
}

// lines devuelve, para cada línea de [top, top+n), el índice de estilo de cada
// runa. Devuelve nil si no hay lexer. Las líneas del margen de lookback se
// tokenizan pero no se devuelven.
func (h *highlighter) lines(buf *editor.Buffer, top, n int) [][]uint16 {
	if h.lexer == nil || n <= 0 {
		return nil
	}
	start := max(0, top-lookback)
	end := min(buf.LineCount(), top+n)
	var sb strings.Builder
	for i := start; i < end; i++ {
		sb.WriteString(string(buf.Line(i)))
		sb.WriteByte('\n')
	}
	text := sb.String()
	h.mu.Lock()
	if h.cache.out != nil && h.cache.top == top && h.cache.text == text {
		out := h.cache.out
		h.mu.Unlock()
		return out
	}
	h.mu.Unlock()
	// EnsureLF falso: los índices por runa deben coincidir con los del buffer.
	it, err := h.lexer.Tokenise(&chroma.TokeniseOptions{State: "root"}, text)
	if err != nil {
		return nil
	}

	out := make([][]uint16, end-top)
	for i := range out {
		out[i] = make([]uint16, len(buf.Line(top+i)))
	}
	cur, col := start, 0 // línea y columna (en runas) del siguiente token
	for tok := it(); tok != chroma.EOF && cur < end; tok = it() {
		st := h.styleOf(tok.Type)
		val := tok.Value
		for val != "" {
			nl := strings.IndexByte(val, '\n')
			seg := val
			if nl >= 0 {
				seg = val[:nl]
			}
			nr := utf8.RuneCountInString(seg)
			if cur >= top && cur < end && st != 0 {
				row := out[cur-top]
				for k := 0; k < nr && col+k < len(row); k++ {
					row[col+k] = st
				}
			}
			col += nr
			if nl < 0 {
				break
			}
			cur++
			col = 0
			val = val[nl+1:]
		}
	}
	h.mu.Lock()
	h.cache.top, h.cache.text, h.cache.out = top, text, out
	h.mu.Unlock()
	return out
}
