// Package assets embebe los datos que WARI trae de serie: los snippets por
// lenguaje (snippets/<lang>.json) y las plantillas de archivo
// (templates/<lang>/*.tmpl).
package assets

import "embed"

// FS contiene los directorios snippets y templates.
//
//go:embed snippets templates
var FS embed.FS
