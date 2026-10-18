// Package index indexa los símbolos de un proyecto Java, Kotlin y C#
// (package o namespace, clases, interfaces, enums, records, objetos, métodos,
// constructores, campos, propiedades y sus anotaciones) para el outline, "ir
// a definición" aproximado y el autocompletado de nombres del proyecto.
//
// No es un compilador: es un analizador léxico en Go puro. Primero enmascara
// comentarios y cadenas (ver mask.go), luego reconoce declaraciones a nivel de
// miembro saltando los cuerpos de métodos. Por eso es rápido (miles de líneas
// en pocos milisegundos), tolera código a medias y no necesita CGO.
//
// El índice del proyecto (Index) es incremental: Refresh solo vuelve a leer
// los archivos cuyo mtime o tamaño cambió, reutiliza el criterio de ignorados
// que le inyecte el llamador y respeta un presupuesto de archivos, de tamaño
// por archivo y de símbolos. Si universal-ctags está instalado y se pide
// (Options.Ctags), puede ser la fuente de los archivos del proyecto; el
// archivo abierto en el editor siempre se analiza con el analizador propio
// (Parse), que además da las anotaciones.
//
// Tree-sitter queda como opción futura detrás de un build tag; no se usa.
package index

import (
	"path/filepath"
	"strings"
)

// Lenguajes admitidos; coinciden con los de snippets e imports.
const (
	LangJava   = "java"
	LangKotlin = "kotlin"
	LangCSharp = "csharp"
)

// LangOf devuelve el lenguaje de path según su extensión ("" si no se
// indexa).
func LangOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java":
		return LangJava
	case ".kt", ".kts":
		return LangKotlin
	case ".cs":
		return LangCSharp
	}
	return ""
}

// Kind es el tipo de un símbolo.
type Kind string

// Tipos de símbolo.
const (
	KindPackage     Kind = "package"
	KindNamespace   Kind = "namespace"
	KindClass       Kind = "class"
	KindInterface   Kind = "interface"
	KindEnum        Kind = "enum"
	KindRecord      Kind = "record"
	KindStruct      Kind = "struct"
	KindObject      Kind = "object"
	KindAnnotation  Kind = "annotation"
	KindTypeAlias   Kind = "typealias"
	KindDelegate    Kind = "delegate"
	KindMethod      Kind = "method"
	KindConstructor Kind = "constructor"
	KindField       Kind = "field"
	KindProperty    Kind = "property"
	KindConstant    Kind = "constant" // constante de enum
	KindEvent       Kind = "event"
)

// IsType indica que el símbolo es un tipo (algo que se puede importar o
// instanciar).
func (k Kind) IsType() bool {
	switch k {
	case KindClass, KindInterface, KindEnum, KindRecord, KindStruct, KindObject,
		KindAnnotation, KindTypeAlias, KindDelegate:
		return true
	}
	return false
}

// Symbol es una declaración. Line y Col son base 0, y Col cuenta runas, igual
// que editor.Pos.
type Symbol struct {
	Name string
	Kind Kind
	// Path es la ruta del archivo, relativa a la raíz del índice y con "/".
	Path string
	Line int
	Col  int
	// Container es el nombre punteado de los tipos que lo contienen dentro
	// del archivo ("Outer.Inner"; vacío en el nivel superior).
	Container string
	// Package es el package (Java, Kotlin) o namespace (C#) que lo contiene.
	Package string
	// Signature son los parámetros de un método o constructor, "(int a, String b)".
	Signature string
	// Annotations son las anotaciones (Java, Kotlin: "@GetMapping(\"/x\")") o
	// atributos (C#: "[HttpGet(\"x\")]") que lo preceden, tal como se
	// escribieron y en una sola línea.
	Annotations []string
}

// Qualified devuelve el nombre completo: package, contenedores y nombre.
func (s Symbol) Qualified() string {
	var parts []string
	if s.Package != "" {
		parts = append(parts, s.Package)
	}
	if s.Container != "" {
		parts = append(parts, s.Container)
	}
	parts = append(parts, s.Name)
	return strings.Join(parts, ".")
}

// File es el resultado de analizar un archivo.
type File struct {
	Path string
	Lang string
	// Package es el package o, en C#, el primer namespace del archivo.
	Package string
	// Imports son los imports normales (no static) de Java y Kotlin
	// ("com.x.Foo", "com.x.*") y los using de namespace de C#.
	Imports []string
	Symbols []Symbol
}
