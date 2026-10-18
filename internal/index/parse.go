package index

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Parse analiza src (Java, Kotlin o C# según lang) y devuelve sus símbolos
// ordenados por posición. path se copia a File.Path y a cada Symbol.Path. Un
// lang desconocido devuelve un File vacío. Nunca falla: con código a medias
// devuelve lo que alcanza a reconocer.
func Parse(lang, path string, src []byte) *File {
	f := &File{Path: path, Lang: lang}
	switch lang {
	case LangJava, LangKotlin, LangCSharp:
	default:
		return f
	}
	ms := mask(lang, src)
	p := &parser{lang: lang, src: src, f: f}
	p.toks = tokenize(lang, ms)
	p.lineStarts = lineStarts(src)
	top := &scope{}
	for p.i < len(p.toks) {
		p.body(top)
		if p.i < len(p.toks) { // "}" sobrante
			p.i++
		}
	}
	sort.SliceStable(f.Symbols, func(a, b int) bool {
		x, y := f.Symbols[a], f.Symbols[b]
		if x.Line != y.Line {
			return x.Line < y.Line
		}
		return x.Col < y.Col
	})
	for i := range f.Symbols {
		f.Symbols[i].Path = path
	}
	return f
}

// --- Tokens -----------------------------------------------------------------

type tokKind uint8

const (
	tEOF tokKind = iota
	tIdent
	tPunct
	tNum
)

type token struct {
	off, end int // posición en el fuente (bytes)
	kind     tokKind
	nl       bool // hay un salto de línea antes (o es el primero)
	text     string
}

var eofTok = token{}

// punct evita una asignación por cada signo de puntuación.
var punct [256]string

func init() {
	for i := range punct {
		punct[i] = string(rune(i))
		if i >= 0x80 {
			punct[i] = ""
		}
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// tokenize parte el fuente enmascarado en identificadores, números y signos.
// "=>" y "->" son un solo signo.
func tokenize(lang string, ms []byte) []token {
	toks := make([]token, 0, len(ms)/6+8)
	nl := true
	for i := 0; i < len(ms); {
		c := ms[i]
		switch {
		case c == '\n':
			nl = true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
			continue
		}
		var t token
		switch {
		case lang == LangKotlin && c == '`':
			j := i + 1
			for j < len(ms) && ms[j] != '`' && ms[j] != '\n' {
				j++
			}
			t = token{off: i + 1, end: j, kind: tIdent, text: string(ms[i+1 : j])}
			if j < len(ms) && ms[j] == '`' {
				j++
			}
			i = j
		case lang == LangCSharp && c == '@' && i+1 < len(ms) && isIdentStart(ms[i+1]):
			j := i + 1
			for j < len(ms) && isIdentPart(ms[j]) {
				j++
			}
			t = token{off: i + 1, end: j, kind: tIdent, text: string(ms[i+1 : j])}
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(ms) && isIdentPart(ms[j]) {
				j++
			}
			t = token{off: i, end: j, kind: tIdent, text: string(ms[i:j])}
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(ms) && (isIdentPart(ms[j]) || ms[j] == '.') {
				j++
			}
			t = token{off: i, end: j, kind: tNum, text: "0"}
			i = j
		default:
			if i+1 < len(ms) && ((c == '=' && ms[i+1] == '>') || (c == '-' && ms[i+1] == '>')) {
				t = token{off: i, end: i + 2, kind: tPunct, text: string(ms[i : i+2])}
				i += 2
			} else {
				t = token{off: i, end: i + 1, kind: tPunct, text: punct[c]}
				i++
			}
		}
		t.nl = nl
		nl = false
		toks = append(toks, t)
	}
	return toks
}

func lineStarts(src []byte) []int {
	ls := []int{0}
	for i, c := range src {
		if c == '\n' {
			ls = append(ls, i+1)
		}
	}
	return ls
}

// --- Parser -----------------------------------------------------------------

// maxNesting acota la profundidad de tipos anidados que se analizan; lo más
// hondo se salta como un bloque.
const maxNesting = 48

type scope struct {
	kind Kind   // "" en el nivel superior del archivo o de un namespace
	name string // nombre del tipo
	qual string // nombre punteado con los contenedores
}

type parser struct {
	lang       string
	src        []byte
	toks       []token
	i          int
	lineStarts []int
	f          *File
	pkg        string // package o namespace vigente
	depth      int
}

func (p *parser) tok(i int) token {
	if i >= 0 && i < len(p.toks) {
		return p.toks[i]
	}
	return eofTok
}

// is indica que el token i es exactamente s (signo o palabra).
func (p *parser) is(i int, s string) bool {
	t := p.tok(i)
	return t.kind != tEOF && t.kind != tNum && t.text == s
}

func (p *parser) isIdent(i int) bool { return p.tok(i).kind == tIdent }

// closeOf devuelve el índice del cierre que corresponde a la apertura ( [ {
// del token i (len(toks) si no cierra).
func (p *parser) closeOf(i int) int {
	open := p.toks[i].text
	var closeS string
	switch open {
	case "(":
		closeS = ")"
	case "[":
		closeS = "]"
	default:
		closeS = "}"
	}
	level := 0
	for j := i; j < len(p.toks); j++ {
		t := p.toks[j]
		if t.kind != tPunct {
			continue
		}
		switch t.text {
		case open:
			level++
		case closeS:
			level--
			if level == 0 {
				return j
			}
		}
	}
	return len(p.toks)
}

// skipBalanced salta el grupo que abre en i y devuelve el índice posterior.
func (p *parser) skipBalanced(i int) int { return min(p.closeOf(i)+1, len(p.toks)) }

// skipAngle salta los argumentos de tipo <...> que abren en i. Si no cierran
// antes de un ";", una llave o el final, solo salta el "<".
func (p *parser) skipAngle(i int) int {
	level := 0
	for j := i; j < len(p.toks); j++ {
		t := p.toks[j]
		if t.kind != tPunct {
			continue
		}
		switch t.text {
		case "<":
			level++
		case ">":
			level--
			if level == 0 {
				return j + 1
			}
		case "(", "[":
			j = p.closeOf(j)
		case ";", "{", "}":
			return i + 1
		}
	}
	return i + 1
}

// compact deja el texto del fuente entre dos offsets en una línea, sin
// espacios repetidos y a lo sumo en max runas.
func (p *parser) compact(from, to, max int) string {
	if from < 0 || to > len(p.src) || from >= to {
		return ""
	}
	var sb strings.Builder
	space := false
	n := 0
	for _, r := range string(p.src[from:to]) {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			space = true
			continue
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		if space && sb.Len() > 0 {
			sb.WriteByte(' ')
			n++
		}
		space = false
		if n >= max {
			sb.WriteString("…")
			break
		}
		sb.WriteRune(r)
		n++
	}
	return sb.String()
}

func (p *parser) emit(kind Kind, tokIdx int, anns []string, sig string, sc *scope) {
	s := p.symbolAt(kind, tokIdx, p.toks[tokIdx].text, sc)
	s.Signature, s.Annotations = sig, anns
	p.f.Symbols = append(p.f.Symbols, s)
}

// sigOf devuelve el texto de los parámetros que abren en open.
func (p *parser) sigOf(open int) string {
	cl := p.closeOf(open)
	if cl >= len(p.toks) {
		return ""
	}
	return p.compact(p.toks[open].off, p.toks[cl].end, 80)
}

// --- Prefijo: anotaciones y modificadores ------------------------------------

type prefix struct {
	anns []string
	mods []string
}

func (pf prefix) has(m string) bool {
	for _, x := range pf.mods {
		if x == m {
			return true
		}
	}
	return false
}

var javaMods = setOf("public", "protected", "private", "static", "final", "abstract", "synchronized",
	"native", "transient", "volatile", "strictfp", "default", "sealed")
var kotlinMods = setOf("public", "private", "protected", "internal", "open", "final", "abstract", "override",
	"lateinit", "const", "inline", "noinline", "crossinline", "infix", "operator", "tailrec", "external",
	"suspend", "vararg", "sealed", "data", "inner", "value", "enum", "annotation", "expect", "actual",
	"companion", "reified")
var csMods = setOf("public", "private", "protected", "internal", "static", "readonly", "virtual", "override",
	"abstract", "sealed", "new", "unsafe", "extern", "partial", "async", "volatile", "const", "required",
	"file", "ref", "global", "implicit", "explicit")

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, x := range s {
		m[x] = true
	}
	return m
}

func (p *parser) isModifier(i int) bool {
	t := p.tok(i)
	if t.kind != tIdent {
		return false
	}
	var set map[string]bool
	switch p.lang {
	case LangJava:
		set = javaMods
		if t.text == "non" && p.is(i+1, "-") && p.is(i+2, "sealed") {
			return true
		}
	case LangKotlin:
		set = kotlinMods
	default:
		set = csMods
	}
	if !set[t.text] {
		return false
	}
	// Un modificador va seguido de otra palabra, de una anotación o de un
	// tipo genérico ("static {" es un inicializador de Java).
	n := p.tok(i + 1)
	return n.kind == tIdent || (n.kind == tPunct && (n.text == "@" || n.text == "[" || n.text == "<" ||
		(n.text == "(" && p.lang == LangCSharp) || (n.text == "{" && t.text == "static")))
}

var kotlinUseSite = setOf("file", "field", "get", "set", "param", "property", "receiver", "setparam", "delegate")

// prefix consume las anotaciones y los modificadores que preceden a una
// declaración.
func (p *parser) prefix() prefix {
	var pf prefix
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		switch {
		case p.lang != LangCSharp && t.kind == tPunct && t.text == "@" && !p.is(p.i+1, "interface"):
			p.annotation(&pf)
		case p.lang == LangCSharp && t.kind == tPunct && t.text == "[":
			p.csAttributes(&pf)
		case t.kind == tIdent && p.isModifier(p.i):
			if t.text == "non" {
				p.i += 2
				pf.mods = append(pf.mods, "non-sealed")
			} else {
				pf.mods = append(pf.mods, t.text)
			}
			p.i++
		default:
			return pf
		}
	}
	return pf
}

// annotation consume una anotación de Java o Kotlin que empieza en "@".
func (p *parser) annotation(pf *prefix) {
	start := p.i
	p.i++
	if p.is(p.i, "[") { // Kotlin: @[A, B(1)]
		end := p.closeOf(p.i)
		for j := p.i + 1; j < end; j++ {
			if p.isIdent(j) && !p.is(j-1, ".") {
				k := j
				for p.is(k+1, ".") && p.isIdent(k+2) {
					k += 2
				}
				if p.is(k+1, "(") {
					k = p.closeOf(k + 1)
				}
				pf.anns = append(pf.anns, "@"+p.compact(p.toks[j].off, p.toks[min(k, len(p.toks)-1)].end, 80))
				j = k
			}
		}
		p.i = min(end+1, len(p.toks))
		return
	}
	file := false
	if p.isIdent(p.i) && p.is(p.i+1, ":") && kotlinUseSite[p.toks[p.i].text] {
		file = p.toks[p.i].text == "file"
		p.i += 2
	}
	if !p.isIdent(p.i) {
		return
	}
	p.i++
	for p.is(p.i, ".") && p.isIdent(p.i+1) {
		p.i += 2
	}
	if p.is(p.i, "(") && !p.toks[p.i].nl {
		p.i = p.skipBalanced(p.i)
	}
	if file {
		return
	}
	pf.anns = append(pf.anns, p.compact(p.toks[start].off, p.toks[p.i-1].end, 80))
}

// csAttributes consume una lista de atributos de C#, "[A, B(1)]".
func (p *parser) csAttributes(pf *prefix) {
	open := p.i
	end := p.closeOf(open)
	segStart := open + 1
	flush := func(a, b int) {
		if a >= b {
			return
		}
		if p.isIdent(a) && p.is(a+1, ":") {
			if t := p.toks[a].text; t == "assembly" || t == "module" {
				return
			}
			a += 2
		}
		if a >= b {
			return
		}
		pf.anns = append(pf.anns, "["+p.compact(p.toks[a].off, p.toks[b-1].end, 80)+"]")
	}
	for j := open + 1; j < end; {
		t := p.toks[j]
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			j = p.skipBalanced(j)
		case t.kind == tPunct && t.text == ",":
			flush(segStart, j)
			segStart = j + 1
			j++
		default:
			j++
		}
	}
	flush(segStart, min(end, len(p.toks)))
	p.i = min(end+1, len(p.toks))
}

// --- Cuerpos y declaraciones --------------------------------------------------

// body analiza declaraciones hasta la "}" que cierra el ámbito (sin
// consumirla) o hasta el final.
func (p *parser) body(sc *scope) {
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		if t.kind == tPunct {
			if t.text == "}" {
				return
			}
			if t.text == ";" {
				p.i++
				continue
			}
		}
		start := p.i
		pf := p.prefix()
		if p.i >= len(p.toks) {
			return
		}
		if p.lang == LangKotlin {
			p.kotlinDecl(sc, pf)
		} else {
			p.jvmCsDecl(sc, pf)
		}
		if p.i == start {
			p.i++
		}
	}
}

// resync salta lo que no se reconoce: un bloque, un grupo o un token.
func (p *parser) resync() {
	if p.i >= len(p.toks) {
		return
	}
	t := p.toks[p.i]
	if t.kind == tPunct && (t.text == "{" || t.text == "(" || t.text == "[") {
		p.i = p.skipBalanced(p.i)
		return
	}
	p.i++
}

// skipStatement salta hasta pasar el ";" (o el bloque) que cierra la
// sentencia; no consume una "}" que cierra el ámbito.
func (p *parser) skipStatement() {
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		if t.kind == tPunct {
			switch t.text {
			case ";":
				p.i++
				return
			case "}":
				return
			case "{":
				p.i = p.skipBalanced(p.i)
				return
			case "(", "[":
				p.i = p.skipBalanced(p.i)
				continue
			}
		}
		p.i++
	}
}

// skipExpr salta una expresión hasta el ";" que la cierra (consumido) o la
// "}" del ámbito (sin consumir).
func (p *parser) skipExpr() {
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		if t.kind == tPunct {
			switch t.text {
			case ";":
				p.i++
				return
			case "}":
				return
			case "(", "[", "{":
				p.i = p.skipBalanced(p.i)
				continue
			}
		}
		p.i++
	}
}

// dotted lee un nombre con puntos que empieza en i; devuelve el nombre y el
// índice siguiente. Admite un "*" final (import).
func (p *parser) dotted(i int, star bool) (string, int) {
	if !p.isIdent(i) {
		return "", i
	}
	var sb strings.Builder
	sb.WriteString(p.toks[i].text)
	i++
	for p.is(i, ".") {
		switch {
		case p.isIdent(i + 1):
			sb.WriteByte('.')
			sb.WriteString(p.toks[i+1].text)
			i += 2
		case star && p.is(i+1, "*"):
			sb.WriteString(".*")
			i += 2
			return sb.String(), i
		default:
			return sb.String(), i
		}
	}
	return sb.String(), i
}

func (p *parser) setPackage(name string) {
	p.pkg = name
	if p.f.Package == "" {
		p.f.Package = name
	}
}

// packageDecl analiza "package a.b" (Java, Kotlin).
func (p *parser) packageDecl(top *scope) {
	kw := p.i
	name, next := p.dotted(kw+1, false)
	if name == "" {
		p.skipStatement()
		return
	}
	p.setPackage(name)
	p.f.Symbols = append(p.f.Symbols, p.symbolAt(KindPackage, kw+1, name, top))
	p.i = next
	if p.is(p.i, ";") {
		p.i++
	}
}

// symbolAt es como emit pero con un nombre dado, y devuelve el símbolo.
func (p *parser) symbolAt(kind Kind, tokIdx int, name string, sc *scope) Symbol {
	t := p.toks[tokIdx]
	line := sort.Search(len(p.lineStarts), func(k int) bool { return p.lineStarts[k] > t.off }) - 1
	col := utf8.RuneCount(p.src[p.lineStarts[line]:t.off])
	return Symbol{Name: name, Kind: kind, Line: line, Col: col, Container: sc.qual, Package: p.pkg}
}

// importDecl analiza "import a.b.C", "import a.b.*" (Java, Kotlin).
func (p *parser) importDecl() {
	i := p.i + 1
	static := false
	if p.is(i, "static") {
		static = true
		i++
	}
	name, next := p.dotted(i, true)
	if name == "" {
		p.skipStatement()
		return
	}
	if !static {
		p.f.Imports = append(p.f.Imports, name)
	}
	p.i = next
	if p.is(p.i, "as") && p.isIdent(p.i+1) { // alias de Kotlin
		p.i += 2
	}
	if p.is(p.i, ";") {
		p.i++
	}
}

// typeKind reconoce la palabra que abre un tipo en p.i.
func (p *parser) typeKind(pf prefix) (Kind, bool) {
	t := p.tok(p.i)
	switch {
	case t.kind == tPunct && t.text == "@" && p.is(p.i+1, "interface") && p.lang != LangCSharp:
		return KindAnnotation, true
	case t.kind != tIdent:
		return "", false
	}
	switch t.text {
	case "class":
		if pf.has("enum") {
			return KindEnum, true
		}
		return KindClass, true
	case "interface":
		return KindInterface, true
	case "enum":
		if p.lang != LangKotlin {
			return KindEnum, true
		}
	case "object":
		if p.lang == LangKotlin && (p.isIdent(p.i+1) || pf.has("companion") || p.is(p.i+1, "{")) {
			return KindObject, true
		}
	case "struct":
		if p.lang == LangCSharp && p.isIdent(p.i+1) {
			return KindStruct, true
		}
	case "record":
		if p.lang == LangJava && p.isIdent(p.i+1) && (p.is(p.i+2, "(") || p.is(p.i+2, "<")) {
			return KindRecord, true
		}
		if p.lang == LangCSharp && p.isIdent(p.i+1) {
			return KindRecord, true
		}
	}
	return "", false
}

func (p *parser) jvmCsDecl(sc *scope, pf prefix) {
	t := p.toks[p.i]
	inType := sc.kind != ""
	if t.kind == tIdent && !inType {
		switch {
		case t.text == "package" && p.lang == LangJava:
			p.packageDecl(sc)
			return
		case t.text == "import" && p.lang == LangJava:
			p.importDecl()
			return
		case t.text == "using" && p.lang == LangCSharp:
			p.usingDecl()
			return
		case t.text == "namespace" && p.lang == LangCSharp:
			p.namespaceDecl()
			return
		}
	}
	if kind, ok := p.typeKind(pf); ok {
		p.parseType(kind, pf, sc)
		return
	}
	if !inType && !(p.lang == LangCSharp && t.kind == tIdent && t.text == "delegate") {
		p.skipStatement()
		return
	}
	p.member(sc, pf)
}

// usingDecl analiza "using A.B;" de C#; los alias y "using static" se
// ignoran.
func (p *parser) usingDecl() {
	i := p.i + 1
	if p.is(i, "static") || (p.isIdent(i) && p.is(i+1, "=")) {
		p.skipStatement()
		return
	}
	name, next := p.dotted(i, false)
	if name == "" || !p.is(next, ";") {
		p.skipStatement()
		return
	}
	p.f.Imports = append(p.f.Imports, name)
	p.i = next + 1
}

// namespaceDecl analiza "namespace A.B { ... }" y "namespace A.B;".
func (p *parser) namespaceDecl() {
	kw := p.i
	name, next := p.dotted(kw+1, false)
	if name == "" {
		p.skipStatement()
		return
	}
	full := name
	if p.pkg != "" {
		full = p.pkg + "." + name
	}
	sym := p.symbolAt(KindNamespace, kw+1, full, &scope{})
	sym.Package = full
	p.f.Symbols = append(p.f.Symbols, sym)
	p.i = next
	switch {
	case p.is(p.i, ";"):
		p.i++
		p.setPackage(full)
	case p.is(p.i, "{"):
		if p.depth >= maxNesting {
			p.i = p.skipBalanced(p.i)
			return
		}
		saved := p.pkg
		p.setPackage(full)
		p.i++
		p.depth++
		p.body(&scope{})
		p.depth--
		if p.is(p.i, "}") {
			p.i++
		}
		p.pkg = saved
	}
}

// parseType analiza un tipo (p.i está en su palabra clave) y su cuerpo.
func (p *parser) parseType(kind Kind, pf prefix, sc *scope) {
	kw := p.i
	if p.toks[p.i].text == "@" { // @interface
		p.i++
	}
	p.i++
	if p.lang == LangCSharp && kind == KindRecord && (p.is(p.i, "class") || p.is(p.i, "struct")) {
		p.i++
	}
	nameTok := p.i
	name := ""
	switch {
	case p.isIdent(p.i):
		name = p.toks[p.i].text
		p.i++
	case kind == KindObject && pf.has("companion"):
		name, nameTok = "Companion", kw
	default:
		p.skipStatement()
		return
	}
	qual := name
	if sc.qual != "" {
		qual = sc.qual + "." + name
	}
	// El símbolo toma el texto del nombre (o "Companion" si no lo tiene).
	sym := p.symbolAt(kind, nameTok, name, sc)
	sym.Annotations = pf.anns
	p.f.Symbols = append(p.f.Symbols, sym)
	inner := &scope{kind: kind, name: name, qual: qual}

	if p.is(p.i, "<") {
		p.i = p.skipAngle(p.i)
	}
	if p.lang == LangKotlin {
		// "private constructor(...)" antes de la lista de parámetros.
		save := p.i
		_ = p.prefix()
		if p.is(p.i, "constructor") {
			p.i++
		} else {
			p.i = save
		}
	}
	if p.is(p.i, "(") && !(p.lang == LangKotlin && p.toks[p.i].nl) {
		cl := p.closeOf(p.i)
		if kind == KindRecord || (p.lang == LangKotlin && (kind == KindClass || kind == KindEnum)) {
			p.paramMembers(p.i, cl, inner)
		}
		p.i = min(cl+1, len(p.toks))
	}
	// Resto del encabezado hasta el cuerpo.
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		if t.kind == tPunct {
			switch t.text {
			case "{":
				if p.depth >= maxNesting {
					p.i = p.skipBalanced(p.i)
					return
				}
				p.i++
				p.depth++
				if kind == KindEnum {
					p.enumConstants(inner)
				}
				p.body(inner)
				p.depth--
				if p.is(p.i, "}") {
					p.i++
				}
				return
			case ";":
				p.i++
				return
			case "}":
				return
			case "(", "[":
				p.i = p.skipBalanced(p.i)
				continue
			}
		}
		if p.lang == LangKotlin && t.nl && p.declStart(p.i) {
			return
		}
		p.i++
	}
}

// paramMembers emite como campos o propiedades los parámetros de un record
// (Java, C#) o los val/var del constructor primario de una clase de Kotlin.
func (p *parser) paramMembers(open, cl int, sc *scope) {
	segStart := open + 1
	flush := func(a, b int) {
		if a >= b {
			return
		}
		nameIdx := -1
		switch p.lang {
		case LangKotlin:
			for k := a; k < b; k++ {
				if p.is(k, "val") || p.is(k, "var") {
					if p.isIdent(k + 1) {
						nameIdx = k + 1
					}
					break
				}
			}
		default:
			for k := a; k < b; k++ {
				if p.is(k, "=") {
					break
				}
				if p.isIdent(k) {
					nameIdx = k
				}
			}
		}
		if nameIdx < 0 {
			return
		}
		kind := KindProperty
		if p.lang == LangJava {
			kind = KindField
		}
		p.emit(kind, nameIdx, nil, "", sc)
	}
	angle := 0
	for j := open + 1; j < cl; {
		t := p.toks[j]
		switch {
		case t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{"):
			j = p.skipBalanced(j)
			continue
		case t.kind == tPunct && t.text == "<":
			angle++
		case t.kind == tPunct && t.text == ">":
			if angle > 0 {
				angle--
			}
		case t.kind == tPunct && t.text == "," && angle == 0:
			flush(segStart, j)
			segStart = j + 1
		}
		j++
	}
	flush(segStart, cl)
}

// enumConstants emite las constantes de un enum. Termina en ";" (siguen los
// miembros), en "}" (sin consumirla) o ante algo que no es una constante.
func (p *parser) enumConstants(sc *scope) {
	for p.i < len(p.toks) {
		var pf prefix
		for p.is(p.i, "@") || (p.lang == LangCSharp && p.is(p.i, "[")) {
			if p.lang == LangCSharp {
				p.csAttributes(&pf)
			} else {
				p.annotation(&pf)
			}
		}
		t := p.tok(p.i)
		switch {
		case t.kind == tPunct && t.text == ";":
			p.i++
			return
		case t.kind == tPunct && t.text == "}":
			return
		case t.kind == tPunct && t.text == ",":
			p.i++
			continue
		case t.kind != tIdent:
			return
		}
		n := p.tok(p.i + 1)
		if n.kind == tIdent || (n.kind == tPunct && !(n.text == "," || n.text == "(" || n.text == "{" ||
			n.text == ";" || n.text == "}" || n.text == "=")) {
			return // es un miembro, no una constante
		}
		p.emit(KindConstant, p.i, pf.anns, "", sc)
		p.i++
		if p.is(p.i, "(") {
			p.i = p.skipBalanced(p.i)
		}
		if p.is(p.i, "=") { // C#: A = 1
			p.i++
			for p.i < len(p.toks) {
				t := p.toks[p.i]
				if t.kind == tPunct && (t.text == "," || t.text == ";" || t.text == "}") {
					break
				}
				if t.kind == tPunct && (t.text == "(" || t.text == "[" || t.text == "{") {
					p.i = p.skipBalanced(p.i)
					continue
				}
				p.i++
			}
		}
		if p.is(p.i, "{") {
			p.i = p.skipBalanced(p.i)
		}
	}
}

// --- Miembros de Java y C# ------------------------------------------------------

// member analiza un método, constructor, campo o propiedad dentro de un tipo
// (o un delegate de C# en un namespace).
func (p *parser) member(sc *scope, pf prefix) {
	var head []int // identificadores del encabezado
	var declNames []int
	angle, tuples := 0, 0
	j := p.i
	term := ""
loop:
	for j < len(p.toks) {
		t := p.toks[j]
		switch t.kind {
		case tIdent:
			if angle == 0 {
				head = append(head, j)
			}
		case tPunct:
			switch t.text {
			case "(":
				if len(head) == 0 { // tipo tupla de C#
					tuples++
					j = p.skipBalanced(j)
					continue
				}
				term = "("
				break loop
			case "=", ";", "{", "=>", "}":
				term = t.text
				break loop
			case "[":
				j = p.skipBalanced(j)
				continue
			case "<":
				angle++
			case ">":
				if angle > 0 {
					angle--
				}
			case ",":
				if angle == 0 && len(head) > 0 {
					declNames = append(declNames, head[len(head)-1])
				}
			}
		}
		j++
	}
	hasHead := func(word string) bool {
		for _, h := range head {
			if p.toks[h].text == word {
				return true
			}
		}
		return false
	}
	fieldKind := KindField
	switch {
	case pf.has("const") && p.lang == LangCSharp:
		fieldKind = KindConstant
	case hasHead("event"):
		fieldKind = KindEvent
	}

	switch term {
	case "(":
		name := head[len(head)-1]
		text := p.toks[name].text
		switch {
		case text == "operator" || hasHead("operator"):
			// operadores de C#: no se indexan
		case hasHead("delegate"):
			p.emit(KindDelegate, name, pf.anns, p.sigOf(j), sc)
		case name > 0 && p.is(name-1, "~"):
			s := p.symbolAt(KindMethod, name, "~"+text, sc)
			s.Signature, s.Annotations = p.sigOf(j), pf.anns
			p.f.Symbols = append(p.f.Symbols, s)
		case text == sc.name && sc.name != "":
			p.emit(KindConstructor, name, pf.anns, p.sigOf(j), sc)
		case len(head)+tuples >= 2:
			p.emit(KindMethod, name, pf.anns, p.sigOf(j), sc)
		}
		p.i = p.skipBalanced(j)
		for p.i < len(p.toks) {
			t := p.toks[p.i]
			if t.kind == tPunct {
				switch t.text {
				case "{":
					p.i = p.skipBalanced(p.i)
					return
				case ";":
					p.i++
					return
				case "}":
					return
				case "=>":
					p.i++
					p.skipExpr()
					return
				case "(", "[":
					p.i = p.skipBalanced(p.i)
					continue
				}
			}
			p.i++
		}
	case "=":
		if len(head) > 0 {
			for _, d := range declNames {
				p.emit(fieldKind, d, pf.anns, "", sc)
			}
			p.emit(fieldKind, head[len(head)-1], pf.anns, "", sc)
		}
		p.i = j + 1
		p.fieldTail(fieldKind, pf, sc)
	case ";":
		if len(head) >= 2 {
			for _, d := range declNames {
				p.emit(fieldKind, d, pf.anns, "", sc)
			}
			p.emit(fieldKind, head[len(head)-1], pf.anns, "", sc)
		}
		p.i = j + 1
	case "{":
		switch {
		case len(head) == 0:
			// inicializador (static { ... })
		case p.lang == LangJava && len(head) == 1 && p.toks[head[0]].text == sc.name:
			p.emit(KindConstructor, head[0], pf.anns, "", sc) // constructor compacto de un record
		case p.lang == LangCSharp && len(head) >= 2 && p.toks[head[len(head)-1]].text != "this":
			kind := KindProperty
			if hasHead("event") {
				kind = KindEvent
			}
			p.emit(kind, head[len(head)-1], pf.anns, "", sc)
			p.i = p.skipBalanced(j)
			if p.is(p.i, "=") { // inicializador de propiedad
				p.i++
				p.skipExpr()
			}
			return
		}
		p.i = p.skipBalanced(j)
	case "=>":
		if p.lang == LangCSharp && len(head) >= 2 && p.toks[head[len(head)-1]].text != "this" {
			p.emit(KindProperty, head[len(head)-1], pf.anns, "", sc)
		}
		p.i = j + 1
		p.skipExpr()
	default: // "}" o fin de archivo
		p.i = j
	}
}

// fieldTail salta el inicializador de un campo, que empieza en p.i, y emite
// los declarantes que siguen ("int a = 1, b = 2;").
func (p *parser) fieldTail(kind Kind, pf prefix, sc *scope) {
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		if t.kind == tPunct {
			switch t.text {
			case ";":
				p.i++
				return
			case "}":
				return
			case "(", "[", "{":
				p.i = p.skipBalanced(p.i)
				continue
			case ",":
				if p.isIdent(p.i+1) && (p.is(p.i+2, "=") || p.is(p.i+2, ",") || p.is(p.i+2, ";") || p.is(p.i+2, "[")) {
					p.emit(kind, p.i+1, pf.anns, "", sc)
					p.i += 2
					if p.is(p.i, "=") {
						p.i++
					}
					continue
				}
			}
		}
		p.i++
	}
}

// --- Kotlin ----------------------------------------------------------------------

var kotlinDeclWords = setOf("fun", "val", "var", "class", "interface", "object", "typealias", "constructor",
	"init", "companion", "enum", "data", "sealed", "open", "abstract", "override", "private", "public",
	"protected", "internal", "inner", "annotation", "lateinit", "const", "inline", "suspend", "operator",
	"infix", "tailrec", "external", "expect", "actual", "value", "final")

// declStart indica que el token i puede abrir una declaración de Kotlin.
func (p *parser) declStart(i int) bool {
	t := p.tok(i)
	if t.kind == tPunct {
		return t.text == "@"
	}
	if t.kind != tIdent || !kotlinDeclWords[t.text] {
		return false
	}
	n := p.tok(i + 1)
	switch t.text {
	case "init":
		return n.kind == tPunct && n.text == "{"
	case "val", "var":
		return n.kind == tIdent || (n.kind == tPunct && (n.text == "<" || n.text == "("))
	case "object", "companion":
		return n.kind == tIdent || (n.kind == tPunct && n.text == "{")
	}
	return n.kind == tIdent || (n.kind == tPunct && (n.text == "@" || n.text == "<"))
}

func (p *parser) kotlinDecl(sc *scope, pf prefix) {
	t := p.toks[p.i]
	if t.kind != tIdent {
		p.resync()
		return
	}
	inType := sc.kind != ""
	switch t.text {
	case "package":
		if !inType {
			p.packageDecl(sc)
			return
		}
	case "import":
		if !inType {
			p.importDecl()
			return
		}
	case "class", "interface", "object":
		if kind, ok := p.typeKind(pf); ok {
			p.parseType(kind, pf, sc)
			return
		}
	case "fun":
		if p.is(p.i+1, "interface") { // fun interface
			p.i++
			if kind, ok := p.typeKind(pf); ok {
				p.parseType(kind, pf, sc)
			}
			return
		}
		p.kotlinFun(sc, pf)
		return
	case "val", "var":
		p.kotlinProp(sc, pf)
		return
	case "typealias":
		if p.isIdent(p.i + 1) {
			p.emit(KindTypeAlias, p.i+1, pf.anns, "", sc)
			p.i += 2
			if p.is(p.i, "<") {
				p.i = p.skipAngle(p.i)
			}
			p.kotlinTail(false)
			return
		}
	case "constructor":
		if inType && p.is(p.i+1, "(") {
			s := p.symbolAt(KindConstructor, p.i, sc.name, sc)
			s.Signature, s.Annotations = p.sigOf(p.i+1), pf.anns
			p.f.Symbols = append(p.f.Symbols, s)
			p.i = p.skipBalanced(p.i + 1)
			p.kotlinTail(true)
			return
		}
	case "init":
		if p.is(p.i+1, "{") {
			p.i = p.skipBalanced(p.i + 1)
			return
		}
	}
	p.resync()
}

func (p *parser) kotlinFun(sc *scope, pf prefix) {
	p.i++ // fun
	if p.is(p.i, "<") {
		p.i = p.skipAngle(p.i)
	}
	nameIdx := -1
	j := p.i
scan:
	for j < len(p.toks) {
		t := p.toks[j]
		switch {
		case t.kind == tIdent:
			nameIdx = j
			j++
		case t.kind == tPunct && t.text == ".":
			j++
		case t.kind == tPunct && t.text == "<":
			j = p.skipAngle(j)
		case t.kind == tPunct && t.text == "?":
			j++
		default:
			break scan
		}
	}
	if nameIdx < 0 || !p.is(j, "(") {
		p.resync()
		return
	}
	p.emit(KindMethod, nameIdx, pf.anns, p.sigOf(j), sc)
	p.i = p.skipBalanced(j)
	p.kotlinTail(true)
}

func (p *parser) kotlinProp(sc *scope, pf prefix) {
	p.i++ // val, var
	if p.is(p.i, "<") {
		p.i = p.skipAngle(p.i)
	}
	nameIdx := -1
	j := p.i
	for p.isIdent(j) {
		nameIdx = j
		j++
		if p.is(j, "<") {
			j = p.skipAngle(j)
		}
		if p.is(j, ".") {
			j++
			continue
		}
		break
	}
	if nameIdx < 0 {
		p.resync() // desestructuración: val (a, b) = ...
		return
	}
	p.emit(KindProperty, nameIdx, pf.anns, "", sc)
	p.i = j
	p.kotlinTail(false)
}

// kotlinTail salta el resto de una declaración de Kotlin, que no termina en
// ";" sino al abrirse otra declaración en una línea nueva, al cerrarse el
// ámbito o, en las funciones, tras su cuerpo { }.
func (p *parser) kotlinTail(fun bool) {
	expr := false
	for p.i < len(p.toks) {
		t := p.toks[p.i]
		if t.kind == tPunct {
			switch t.text {
			case ";":
				p.i++
				return
			case "}":
				return
			case "{":
				p.i = p.skipBalanced(p.i)
				if fun && !expr {
					return
				}
				continue
			case "(", "[":
				p.i = p.skipBalanced(p.i)
				continue
			case "=":
				expr = true
			}
		}
		if t.nl && p.declStart(p.i) {
			return
		}
		p.i++
	}
}
