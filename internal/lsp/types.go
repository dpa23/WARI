package lsp

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// Position es una posición LSP: línea y columna base 0; Character cuenta
// unidades de código UTF-16.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range es un rango semiabierto [Start, End).
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location es un rango dentro de un documento.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Severidades de un diagnóstico.
const (
	SeverityError       = 1
	SeverityWarning     = 2
	SeverityInformation = 3
	SeverityHint        = 4
)

// Diagnostic es un problema que el servidor reporta en un documento.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// CompletionItem es una sugerencia de autocompletado ya normalizada.
type CompletionItem struct {
	Label  string
	Detail string
	// Kind es el CompletionItemKind de LSP (2 método, 3 función, 5 campo...).
	Kind int
	// InsertText es el texto a insertar: textEdit.newText, insertText o la
	// etiqueta, en ese orden.
	InsertText string
	// Replace es el rango que InsertText sustituye, si el servidor lo indicó.
	Replace *Range
	// Snippet indica que InsertText usa la sintaxis de snippets de LSP
	// ($1, ${2:x}); el cliente no declara soporte, así que es raro.
	Snippet  bool
	SortText string
}

// ServerCapabilities son las capacidades que interesan del resultado de
// initialize; el resto se descarta.
type ServerCapabilities struct {
	Completion bool
	Definition bool
	// Raw es el objeto capabilities completo.
	Raw json.RawMessage
}

// FileURI convierte una ruta del sistema en un URI file://.
func FileURI(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/dir en Windows
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// URIToPath convierte un URI file:// en una ruta del sistema. Devuelve false
// si no es un URI de archivo.
func URIToPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), true
}

// ByteToUTF16 convierte un desplazamiento en bytes dentro de line en la
// columna LSP (unidades UTF-16). Un desplazamiento fuera de rango se acota.
func ByteToUTF16(line string, byteOff int) int {
	if byteOff > len(line) {
		byteOff = len(line)
	}
	n := 0
	for _, r := range line[:max(byteOff, 0)] {
		n += utf16Len(r)
	}
	return n
}

// UTF16ToByte es la inversa de ByteToUTF16. Una columna fuera de rango se
// acota al final de la línea.
func UTF16ToByte(line string, col int) int {
	n := 0
	for i, r := range line {
		if n >= col {
			return i
		}
		n += utf16Len(r)
	}
	return len(line)
}

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2 // par sustituto
	}
	return 1
}
