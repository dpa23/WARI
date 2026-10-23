// Package assets embebe los datos que WARI trae de serie: los snippets por
// lenguaje (snippets/<lang>.json) y las plantillas de archivo
// (templates/<lang>/*.tmpl) y los mapas tipo→import (imports/<lang>.json).
package assets

import (
	"embed"
	"io/fs"
)

// FS contiene los directorios snippets, templates e imports.
//
//go:embed snippets templates imports
var FS embed.FS

// fontFS embebe la fuente de los iconos (JetBrainsMono Nerd Font, licencia OFL
// en OFL.txt): el binario la trae y WARI la instala sola si falta.
//
//go:embed fonts/JetBrainsMonoNerdFont-Regular.ttf fonts/OFL.txt
var fontFS embed.FS

// FontFileName es el nombre de la fuente embebida.
const FontFileName = "JetBrainsMonoNerdFont-Regular.ttf"

// NerdFont devuelve los bytes de la fuente embebida.
func NerdFont() ([]byte, error) { return fs.ReadFile(fontFS, "fonts/"+FontFileName) }
