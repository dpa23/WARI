package index

// Enmascarado léxico. Antes de buscar declaraciones, mask devuelve una copia
// del fuente (mismo largo, mismos saltos de línea) donde comentarios, cadenas
// y caracteres literales quedaron reemplazados por espacios. Así ninguna
// declaración escrita dentro de un comentario, de una cadena o de un bloque de
// texto se indexa, y las llaves o comillas de su interior no desbalancean el
// análisis.
//
// Cubre, por lenguaje:
//   - Java: //, /* */, "..." con escapes, bloques de texto """...""" y 'c'.
//   - Kotlin: //, /* */ anidados, "..." con plantillas ${...}, cadenas
//     crudas """...""" y 'c'.
//   - C#: //, /* */, "...", verbatim @"..." (con "" como comilla), interpoladas
//     $"..." y $@"..." (con {expr} y {{), cadenas crudas """...""" y 'c'; las
//     líneas que empiezan por # (directivas) se descartan.

// maxLitDepth acota la recursión de literales dentro de plantillas.
const maxLitDepth = 8

type masker struct {
	lang string
	src  []byte
	out  []byte
}

// mask devuelve el fuente sin comentarios ni literales (ver arriba).
func mask(lang string, src []byte) []byte {
	m := &masker{lang: lang, src: src, out: make([]byte, len(src))}
	copy(m.out, src)
	m.run()
	return m.out
}

// blank reemplaza [from, to) por espacios, conservando los saltos de línea.
func (m *masker) blank(from, to int) {
	for i := from; i < to && i < len(m.out); i++ {
		if c := m.out[i]; c != '\n' && c != '\r' {
			m.out[i] = ' '
		}
	}
}

func (m *masker) at(i int) byte {
	if i >= 0 && i < len(m.src) {
		return m.src[i]
	}
	return 0
}

func (m *masker) run() {
	n := len(m.src)
	lineStart := true // solo espacios desde el inicio de la línea
	for i := 0; i < n; {
		c := m.src[i]
		switch {
		case c == '\n':
			lineStart = true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '/' && m.at(i+1) == '/':
			j := m.eol(i)
			m.blank(i, j)
			i = j
		case c == '/' && m.at(i+1) == '*':
			j := m.blockComment(i)
			m.blank(i, j)
			i = j
		case c == '#' && lineStart && (m.lang == "csharp" || (m.lang == "kotlin" && i == 0)):
			j := m.eol(i)
			m.blank(i, j)
			i = j
		default:
			if j, ok := m.literal(i, 0); ok {
				m.blank(i, j)
				i = j
			} else {
				i++
			}
		}
		lineStart = false
	}
}

func (m *masker) eol(i int) int {
	for i < len(m.src) && m.src[i] != '\n' {
		i++
	}
	return i
}

// blockComment devuelve el final del comentario /* */ que empieza en i;
// en Kotlin se anidan.
func (m *masker) blockComment(i int) int {
	depth := 0
	for i < len(m.src) {
		switch {
		case m.src[i] == '/' && m.at(i+1) == '*' && (depth == 0 || m.lang == "kotlin"):
			depth++
			i += 2
		case m.src[i] == '*' && m.at(i+1) == '/':
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(m.src)
}

// literal reconoce un literal de cadena o carácter que empieza en i y
// devuelve su final (exclusivo). ok es false si en i no empieza ninguno.
func (m *masker) literal(i, depth int) (end int, ok bool) {
	if depth > maxLitDepth {
		return i + 1, true
	}
	c := m.at(i)
	switch {
	case c == '\'':
		return m.char(i), true
	case c == '"':
		if m.at(i+1) == '"' && m.at(i+2) == '"' {
			return m.raw(i, m.lang == "kotlin", depth), true
		}
		return m.quoted(i, false, m.lang == "kotlin", depth), true
	case m.lang == "csharp" && (c == '@' || c == '$'):
		// Prefijos: @", $", $@", @$" y $"""...""" (con varios $).
		j, verbatim, interp := i, false, false
		for ; j < len(m.src) && (m.src[j] == '@' || m.src[j] == '$'); j++ {
			if m.src[j] == '@' {
				verbatim = true
			} else {
				interp = true
			}
		}
		if m.at(j) != '"' {
			return 0, false
		}
		if !verbatim && m.at(j+1) == '"' && m.at(j+2) == '"' {
			return m.raw(j, false, depth), true
		}
		return m.quoted(j, verbatim, interp, depth), true
	}
	return 0, false
}

// char devuelve el final de un literal 'c' (o '\n', 'A'); sin cerrar,
// termina en el salto de línea.
func (m *masker) char(i int) int {
	j := i + 1
	for j < len(m.src) {
		switch m.src[j] {
		case '\\':
			j += 2
			continue
		case '\'':
			return j + 1
		case '\n':
			return j
		}
		j++
	}
	return len(m.src)
}

// quoted devuelve el final de una cadena "..." que abre en i. verbatim es la
// de C# (@"") y interp admite expresiones: ${...} en Kotlin, {...} en C#.
func (m *masker) quoted(i int, verbatim, interp bool, depth int) int {
	j := i + 1
	for j < len(m.src) {
		c := m.src[j]
		switch {
		case verbatim && c == '"':
			if m.at(j+1) == '"' {
				j += 2
				continue
			}
			return j + 1
		case !verbatim && c == '\\':
			j += 2
			continue
		case !verbatim && c == '"':
			return j + 1
		case !verbatim && c == '\n':
			return j // sin cerrar
		case interp && m.lang == "kotlin" && c == '$' && m.at(j+1) == '{':
			j = m.braces(j+1, depth)
			continue
		case interp && m.lang == "csharp" && c == '{':
			if m.at(j+1) == '{' {
				j += 2
				continue
			}
			j = m.braces(j, depth)
			continue
		}
		j++
	}
	return len(m.src)
}

// raw devuelve el final de una cadena de varias comillas ("""...""") que abre
// en i: bloque de texto de Java, cadena cruda de Kotlin o de C#. interp admite
// ${...} (Kotlin).
func (m *masker) raw(i int, interp bool, depth int) int {
	q := 0
	for m.at(i+q) == '"' {
		q++
	}
	j := i + q
	for j < len(m.src) {
		c := m.src[j]
		switch {
		case c == '\\' && m.lang == "java":
			j += 2
			continue
		case c == '"':
			run := 0
			for m.at(j+run) == '"' {
				run++
			}
			if run >= q {
				// Kotlin admite comillas de más antes del cierre: las tres
				// últimas cierran.
				return j + run
			}
			j += run
			continue
		case interp && c == '$' && m.at(j+1) == '{':
			j = m.braces(j+1, depth)
			continue
		}
		j++
	}
	return len(m.src)
}

// braces recorre una expresión { ... } de una plantilla que abre en i y
// devuelve el índice posterior a la llave de cierre; las cadenas anidadas se
// saltan enteras.
func (m *masker) braces(i, depth int) int {
	level := 0
	for j := i; j < len(m.src); {
		switch m.src[j] {
		case '{':
			level++
			j++
		case '}':
			level--
			j++
			if level == 0 {
				return j
			}
		default:
			if k, ok := m.literal(j, depth+1); ok && k > j {
				j = k
			} else {
				j++
			}
		}
	}
	return len(m.src)
}
