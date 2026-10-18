package snippets

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Vars aporta los valores de las variables del snippet.
type Vars struct {
	// Filename es la ruta o nombre del archivo actual (TM_FILENAME).
	Filename string
	// Tab es el texto que reemplaza a cada tabulador del cuerpo; vacío
	// significa "\t". Para sangrar con espacios, usa Spaces(n).
	Tab string
	// Extra define variables adicionales (nombre sin "$").
	Extra map[string]string
}

// Spaces devuelve n espacios, para Vars.Tab.
func Spaces(n int) string { return strings.Repeat(" ", max(n, 0)) }

func (v Vars) lookup(name string) string {
	file := path.Base(strings.ReplaceAll(v.Filename, `\`, "/"))
	if v.Filename == "" {
		file = ""
	}
	base := strings.TrimSuffix(file, path.Ext(file))
	switch name {
	case "TM_FILENAME":
		return file
	case "TM_FILENAME_BASE", "CLASS_NAME":
		return base
	}
	return v.Extra[name]
}

// Range es un tramo [Start, End) en runas relativo al inicio del texto.
type Range struct{ Start, End int }

// Tabstop es una parada de cursor. Si Start < End es un placeholder que el
// editor selecciona; si no, un punto de inserción. Mirrors son las demás
// apariciones del mismo índice (se editan en espejo).
type Tabstop struct {
	Index      int
	Start, End int
	Mirrors    []Range
}

// Expansion es el resultado de expandir un snippet. Stops va ordenado: $1,
// $2, ..., y al final $0. Siempre incluye la parada final: si el snippet no
// declara $0, queda al final del texto.
type Expansion struct {
	Text  string
	Stops []Tabstop
}

type stopRec struct {
	start, end int
	text       []rune
	mirrors    []Range
}

type expander struct {
	src    []rune
	pos    int
	out    []rune
	vars   Vars
	indent []rune
	tab    []rune
	muted  int
	stops  map[int]*stopRec
}

// Expand expande sn. indent se antepone a las líneas 2..n del texto (la
// sangría de la línea donde se inserta) y Vars.Tab reemplaza a los
// tabuladores del cuerpo. Sintaxis: $1, ${1}, ${1:default} (anidable), $0,
// $VAR, ${VAR}, ${VAR:default}, y los escapes \$, \} y \\. Un índice repetido
// se registra como espejo del primero y copia su texto; una variable sin valor
// usa su default o queda vacía.
func Expand(sn Snippet, vars Vars, indent string) (Expansion, error) {
	tab := vars.Tab
	if tab == "" {
		tab = "\t"
	}
	e := &expander{
		src:    []rune(strings.Join(sn.Body, "\n")),
		vars:   vars,
		indent: []rune(indent),
		tab:    []rune(tab),
		stops:  map[int]*stopRec{},
	}
	if err := e.parseSeq(0); err != nil {
		return Expansion{}, fmt.Errorf("snippet %q: %w", sn.Name, err)
	}

	idxs := make([]int, 0, len(e.stops))
	for i := range e.stops {
		if i != 0 {
			idxs = append(idxs, i)
		}
	}
	sort.Ints(idxs)
	idxs = append(idxs, 0)

	exp := Expansion{Text: string(e.out)}
	for _, i := range idxs {
		rec := e.stops[i]
		if rec == nil { // $0 implícito
			rec = &stopRec{start: len(e.out), end: len(e.out)}
		}
		exp.Stops = append(exp.Stops, Tabstop{Index: i, Start: rec.start, End: rec.end, Mirrors: rec.mirrors})
	}
	return exp, nil
}

func (e *expander) emit(r rune) {
	if e.muted > 0 {
		return
	}
	switch r {
	case '\n':
		e.out = append(e.out, '\n')
		e.out = append(e.out, e.indent...)
	case '\t':
		e.out = append(e.out, e.tab...)
	default:
		e.out = append(e.out, r)
	}
}

func (e *expander) emitRaw(rs []rune) {
	if e.muted == 0 {
		e.out = append(e.out, rs...)
	}
}

// parseSeq consume texto hasta el final o, con depth > 0, hasta el '}' que
// cierra el placeholder actual (sin consumirlo).
func (e *expander) parseSeq(depth int) error {
	for e.pos < len(e.src) {
		c := e.src[e.pos]
		switch c {
		case '\\':
			if e.pos+1 < len(e.src) && strings.ContainsRune(`$}\`, e.src[e.pos+1]) {
				e.emit(e.src[e.pos+1])
				e.pos += 2
			} else {
				e.emit('\\')
				e.pos++
			}
		case '}':
			if depth > 0 {
				return nil
			}
			e.emit('}')
			e.pos++
		case '$':
			if err := e.dollar(); err != nil {
				return err
			}
		default:
			e.emit(c)
			e.pos++
		}
	}
	return nil
}

// body parsea un default anidado y consume su '}' de cierre.
func (e *expander) body() error {
	if err := e.parseSeq(1); err != nil {
		return err
	}
	if e.pos >= len(e.src) {
		return fmt.Errorf("falta '}' para cerrar un placeholder")
	}
	e.pos++
	return nil
}

// skipBody parsea y descarta un default anidado.
func (e *expander) skipBody() error {
	e.muted++
	defer func() { e.muted-- }()
	return e.body()
}

func (e *expander) dollar() error {
	p := e.pos + 1
	if p >= len(e.src) {
		e.emit('$')
		e.pos++
		return nil
	}
	c := e.src[p]
	switch {
	case isDigit(c):
		idx, end := e.readInt(p)
		e.pos = end
		return e.tabstop(idx, false)
	case isNameStart(c):
		name, end := e.readName(p)
		e.pos = end
		return e.variable(name, false)
	case c == '{':
		p++
		if p < len(e.src) && (isDigit(e.src[p]) || isNameStart(e.src[p])) {
			var (
				name string
				num  int
				end  int
			)
			isNum := isDigit(e.src[p])
			if isNum {
				num, end = e.readInt(p)
			} else {
				name, end = e.readName(p)
			}
			if end >= len(e.src) {
				return fmt.Errorf("falta '}' en la posición %d", p)
			}
			hasDefault := false
			switch e.src[end] {
			case '}':
			case ':':
				hasDefault = true
			default:
				return fmt.Errorf("sintaxis no soportada %q en la posición %d", string(e.src[e.pos:end+1]), e.pos)
			}
			e.pos = end + 1
			if isNum {
				return e.tabstop(num, hasDefault)
			}
			return e.variable(name, hasDefault)
		}
	}
	e.emit('$')
	e.pos++
	return nil
}

func (e *expander) tabstop(idx int, hasDefault bool) error {
	if e.muted > 0 {
		if hasDefault {
			return e.body()
		}
		return nil
	}
	if rec := e.stops[idx]; rec != nil {
		if hasDefault {
			if err := e.skipBody(); err != nil {
				return err
			}
		}
		start := len(e.out)
		e.emitRaw(rec.text)
		rec.mirrors = append(rec.mirrors, Range{start, len(e.out)})
		return nil
	}
	rec := &stopRec{start: len(e.out)}
	e.stops[idx] = rec
	if hasDefault {
		if err := e.body(); err != nil {
			return err
		}
	}
	rec.end = len(e.out)
	rec.text = append([]rune(nil), e.out[rec.start:rec.end]...)
	return nil
}

func (e *expander) variable(name string, hasDefault bool) error {
	if val := e.vars.lookup(name); val != "" {
		if hasDefault {
			if err := e.skipBody(); err != nil {
				return err
			}
		}
		for _, r := range val {
			e.emit(r)
		}
		return nil
	}
	if hasDefault {
		return e.body()
	}
	return nil
}

func (e *expander) readInt(p int) (int, int) {
	n := 0
	for p < len(e.src) && isDigit(e.src[p]) {
		if n < 1<<24 {
			n = n*10 + int(e.src[p]-'0')
		}
		p++
	}
	return n, p
}

func (e *expander) readName(p int) (string, int) {
	start := p
	for p < len(e.src) && (isNameStart(e.src[p]) || isDigit(e.src[p])) {
		p++
	}
	return string(e.src[start:p]), p
}

func isDigit(r rune) bool     { return r >= '0' && r <= '9' }
func isNameStart(r rune) bool { return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }
