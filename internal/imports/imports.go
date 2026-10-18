// Package imports carga mapas tipo→import (JSON por lenguaje) y calcula dónde
// y cómo insertar el import de un tipo en el texto de un archivo Java, Kotlin
// o C#. No depende del editor: trabaja con las líneas del archivo y devuelve
// un Edit que el llamador aplica como un único cambio.
//
// Formato del JSON: {"List": ["java.util.List", "java.awt.List"]}. Cada tipo
// lleva sus candidatos; el primero es el más habitual. En Java y Kotlin el
// candidato es el nombre completo de la clase (se escribe "import X;" o
// "import X"); en C# es el namespace ("using X;").
//
// Regla de posición en Java y Kotlin. El import nuevo se coloca dentro del
// bloque de imports existente, antes del primero que le sigue en este orden y
// si no, tras el último:
//   - Java: los imports normales antes que los "import static"; dentro de cada
//     tipo, por grupos (java., javax. y jakarta., org., com., resto) y por
//     orden alfabético estricto dentro del grupo.
//   - Kotlin: orden alfabético estricto (ASCII) por nombre completo.
//
// Sin bloque de imports, va tras la línea package, separado de ella y del
// código por una línea en blanco; sin package, al inicio del archivo tras los
// comentarios de cabecera.
//
// Regla en C#. "using X;" va en el bloque de using del inicio (los de
// columna 0 antes de "namespace"), con System y System.* primero y el resto
// después, cada grupo por orden alfabético ordinal; los alias y "using static"
// se quedan al final del bloque. Sin bloque, va al inicio tras los
// comentarios de cabecera y las directivas "#".
//
// Un import no se inserta si ya está, si un comodín lo cubre (java.util.* o
// "using" del mismo namespace), si el tipo es del mismo package o namespace
// que el archivo (o de uno que lo contiene, en C#), o si el archivo ya
// importa otro tipo con el mismo nombre simple.
package imports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Lenguajes admitidos, con el nombre de su archivo JSON.
const (
	Java   = "java"
	Kotlin = "kotlin"
	CSharp = "csharp"
)

// Map asocia el nombre simple de un tipo con sus candidatos de import.
type Map map[string][]string

type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*l = stringList{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return errors.New("se esperaba un texto o una lista de textos")
	}
	*l = arr
	return nil
}

// Load lee un JSON {"Tipo": ["candidato", ...]} (un texto suelto vale como
// lista de uno). Rechaza nombres o candidatos vacíos y tipos sin candidatos.
func Load(r io.Reader) (Map, error) {
	var raw map[string]stringList
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("imports: JSON inválido: %w", err)
	}
	m := make(Map, len(raw))
	for typ, cands := range raw {
		if typ == "" {
			return nil, errors.New("imports: nombre de tipo vacío")
		}
		if len(cands) == 0 {
			return nil, fmt.Errorf("imports: %q no tiene candidatos", typ)
		}
		for _, c := range cands {
			if strings.TrimSpace(c) == "" {
				return nil, fmt.Errorf("imports: %q tiene un candidato vacío", typ)
			}
		}
		m[typ] = append([]string(nil), cands...)
	}
	return m, nil
}

// LoadDir carga cada *.json de dir; la clave es el nombre del archivo sin
// extensión ("java", "kotlin", "csharp").
func LoadDir(fsys fs.FS, dir string) (map[string]Map, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("imports: %w", err)
	}
	out := make(map[string]Map)
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		f, err := fsys.Open(path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("imports: %w", err)
		}
		m, err := Load(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out[strings.TrimSuffix(e.Name(), ".json")] = m
	}
	return out, nil
}

// Merge devuelve m con los tipos de over añadidos; ante el mismo tipo gana
// over. No modifica ninguno de los dos.
func (m Map) Merge(over Map) Map {
	out := make(Map, len(m)+len(over))
	for k, v := range m {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// Candidates devuelve los candidatos de typ (nil si no está en el mapa).
func (m Map) Candidates(typ string) []string { return m[typ] }

// Types devuelve los nombres de tipo del mapa, ordenados.
func (m Map) Types() []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Edit es una inserción de líneas: Text se inserta en la columna 0 de la
// línea Line (base 0) y termina en salto de línea. Si Line == len(lines), el
// archivo no tiene línea siguiente: Text se añade tras la última línea, con un
// salto delante y sin el final (ver Apply).
type Edit struct {
	Line int
	Text string
}

// Apply devuelve el texto resultante de aplicar e a lines (unidas con "\n").
func Apply(lines []string, e Edit) string {
	if e.Line >= len(lines) {
		return strings.Join(lines, "\n") + "\n" + strings.TrimSuffix(e.Text, "\n")
	}
	before := strings.Join(lines[:e.Line], "\n")
	if e.Line > 0 {
		before += "\n"
	}
	return before + e.Text + strings.Join(lines[e.Line:], "\n")
}

// Covered indica que candidate ya se puede usar en el archivo sin añadir nada.
func Covered(lang string, lines []string, candidate string) bool {
	if lang == CSharp {
		return scanCS(lines).covers(candidate)
	}
	return scanJVM(lines).covers(candidate)
}

// Imported indica que el tipo typ ya se puede usar: alguno de sus candidatos
// está cubierto o el archivo ya importa un tipo con ese nombre simple.
func Imported(lang string, lines []string, typ string, candidates []string) bool {
	if lang == CSharp {
		s := scanCS(lines)
		for _, c := range candidates {
			if s.covers(c) {
				return true
			}
		}
		return false
	}
	s := scanJVM(lines)
	for _, d := range s.decls {
		if !d.wildcard && !d.static && d.simple() == typ {
			return true
		}
	}
	for _, c := range candidates {
		if s.covers(c) {
			return true
		}
	}
	return false
}

// Insert calcula la inserción del import de candidate. ok es false si no hace
// falta insertar nada (ver la regla en la documentación del paquete).
func Insert(lang string, lines []string, candidate string) (e Edit, ok bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return Edit{}, false
	}
	if lang == CSharp {
		return insertCS(lines, candidate)
	}
	return insertJVM(lang, lines, candidate)
}

// --- Java y Kotlin ---------------------------------------------------------

type decl struct {
	line     int
	name     string // sin ".*", sin alias
	alias    string
	static   bool
	wildcard bool
}

func (d decl) simple() string {
	if d.alias != "" {
		return d.alias
	}
	return name(d.name)
}

// name devuelve el último segmento de un nombre con puntos.
func name(full string) string {
	if i := strings.LastIndexByte(full, '.'); i >= 0 {
		return full[i+1:]
	}
	return full
}

func pkgOf(full string) string {
	if i := strings.LastIndexByte(full, '.'); i >= 0 {
		return full[:i]
	}
	return ""
}

type jvmFile struct {
	pkg     string
	pkgLine int // -1 si no hay
	decls   []decl
}

func (f jvmFile) covers(candidate string) bool {
	if f.pkg != "" && pkgOf(candidate) == f.pkg {
		return true
	}
	for _, d := range f.decls {
		if d.static {
			continue
		}
		if d.wildcard && d.name == pkgOf(candidate) {
			return true
		}
		if !d.wildcard && d.name == candidate && (d.alias == "" || d.alias == name(candidate)) {
			return true
		}
	}
	return false
}

// stripLine quita el comentario de línea y el ";" final.
func stripLine(s string) string {
	if i := strings.Index(s, "//"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";"))
}

func scanJVM(lines []string) jvmFile {
	f := jvmFile{pkgLine: -1}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case f.pkgLine < 0 && len(f.decls) == 0 && strings.HasPrefix(t, "package ") && !strings.HasPrefix(t, "package-"):
			f.pkgLine = i
			f.pkg = strings.TrimSpace(strings.TrimPrefix(stripLine(t), "package "))
		case strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "import\t"):
			rest := strings.TrimSpace(strings.TrimPrefix(stripLine(t), "import"))
			d := decl{line: i}
			if strings.HasPrefix(rest, "static ") {
				d.static = true
				rest = strings.TrimSpace(strings.TrimPrefix(rest, "static "))
			}
			if a, alias, ok := strings.Cut(rest, " as "); ok {
				rest, d.alias = strings.TrimSpace(a), strings.TrimSpace(alias)
			}
			if strings.HasSuffix(rest, ".*") {
				d.wildcard = true
				rest = strings.TrimSuffix(rest, ".*")
			}
			d.name = rest
			f.decls = append(f.decls, d)
		}
	}
	return f
}

type jvmKey struct {
	static bool
	group  int
	name   string
}

func (a jvmKey) less(b jvmKey) bool {
	if a.static != b.static {
		return !a.static
	}
	if a.group != b.group {
		return a.group < b.group
	}
	return a.name < b.name
}

func keyOf(lang string, d decl) jvmKey {
	k := jvmKey{static: d.static, name: d.name}
	if lang == Java {
		switch {
		case strings.HasPrefix(d.name, "java."):
			k.group = 0
		case strings.HasPrefix(d.name, "javax."), strings.HasPrefix(d.name, "jakarta."):
			k.group = 1
		case strings.HasPrefix(d.name, "org."):
			k.group = 2
		case strings.HasPrefix(d.name, "com."):
			k.group = 3
		default:
			k.group = 4
		}
	}
	return k
}

func insertJVM(lang string, lines []string, candidate string) (Edit, bool) {
	f := scanJVM(lines)
	if f.covers(candidate) {
		return Edit{}, false
	}
	stmt := "import " + candidate
	if lang == Java {
		stmt += ";"
	}
	if len(f.decls) > 0 {
		nk := keyOf(lang, decl{name: candidate})
		for _, d := range f.decls {
			if nk.less(keyOf(lang, d)) {
				return Edit{d.line, stmt + "\n"}, true
			}
		}
		return Edit{f.decls[len(f.decls)-1].line + 1, stmt + "\n"}, true
	}
	if f.pkgLine >= 0 {
		next := f.pkgLine + 1
		switch {
		case next >= len(lines):
			return Edit{len(lines), "\n" + stmt + "\n"}, true
		case strings.TrimSpace(lines[next]) == "":
			return Edit{next, "\n" + stmt + "\n"}, true
		default:
			return Edit{next, "\n" + stmt + "\n\n"}, true
		}
	}
	k := headerEnd(lines, false)
	switch {
	case isBlank(lines):
		return Edit{0, stmt + "\n"}, true
	case k >= len(lines):
		return Edit{len(lines), "\n" + stmt + "\n"}, true
	}
	return Edit{k, stmt + "\n\n"}, true
}

// isBlank indica que el archivo no tiene más que líneas en blanco.
func isBlank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return false
		}
	}
	return true
}

// headerEnd devuelve la primera línea que no es blanca, comentario de cabecera
// ni (en C#) directiva "#".
func headerEnd(lines []string, cs bool) int {
	i := 0
	for i < len(lines) {
		t := strings.TrimSpace(lines[i])
		switch {
		case t == "" || strings.HasPrefix(t, "//") || (cs && strings.HasPrefix(t, "#")):
			i++
		case strings.HasPrefix(t, "/*"):
			for i < len(lines) && !strings.Contains(lines[i], "*/") {
				i++
			}
			i++
		default:
			return i
		}
	}
	return len(lines)
}

// --- C# ---------------------------------------------------------------------

type csUsing struct {
	line int
	name string
	rank int // 0 System, 1 resto, 2 alias y static
}

type csFile struct {
	usings []csUsing
	ns     string
}

func (f csFile) covers(candidate string) bool {
	if f.ns != "" && (f.ns == candidate || strings.HasPrefix(f.ns, candidate+".")) {
		return true
	}
	for _, u := range f.usings {
		if u.rank < 2 && u.name == candidate {
			return true
		}
	}
	return false
}

func csRank(ns string) int {
	if ns == "System" || strings.HasPrefix(ns, "System.") {
		return 0
	}
	return 1
}

func scanCS(lines []string) csFile {
	var f csFile
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "namespace ") {
			rest := strings.TrimSpace(strings.TrimSuffix(stripLineKeepSemi(t[len("namespace "):]), "{"))
			f.ns = strings.TrimSpace(strings.TrimSuffix(rest, ";"))
			break
		}
		global := strings.HasPrefix(t, "global using ")
		if !global && (!strings.HasPrefix(t, "using ") || strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) {
			continue
		}
		code := stripLineKeepSemi(t)
		if !strings.HasSuffix(code, ";") {
			continue
		}
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(code, "global "), "using "), ";"))
		if strings.HasPrefix(body, "(") || strings.HasPrefix(body, "var ") {
			continue
		}
		u := csUsing{line: i, name: body}
		if strings.HasPrefix(body, "static ") || strings.Contains(body, "=") {
			u.rank = 2
		} else {
			u.rank = csRank(body)
		}
		f.usings = append(f.usings, u)
	}
	return f
}

// stripLineKeepSemi quita el comentario de línea conservando el ";" final.
func stripLineKeepSemi(s string) string {
	if i := strings.Index(s, "//"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func insertCS(lines []string, candidate string) (Edit, bool) {
	f := scanCS(lines)
	if f.covers(candidate) {
		return Edit{}, false
	}
	stmt := "using " + candidate + ";"
	if len(f.usings) > 0 {
		rank := csRank(candidate)
		for _, u := range f.usings {
			if rank < u.rank || (rank == u.rank && candidate < u.name) {
				return Edit{u.line, stmt + "\n"}, true
			}
		}
		return Edit{f.usings[len(f.usings)-1].line + 1, stmt + "\n"}, true
	}
	k := headerEnd(lines, true)
	switch {
	case isBlank(lines):
		return Edit{0, stmt + "\n"}, true
	case k >= len(lines):
		return Edit{len(lines), "\n" + stmt + "\n"}, true
	case strings.TrimSpace(lines[k]) != "":
		return Edit{k, stmt + "\n\n"}, true
	}
	return Edit{k, stmt + "\n"}, true
}
