// Package assets embebe los datos que WARI trae de serie: los snippets por
// lenguaje (snippets/<lang>.json) y las plantillas de archivo
// (templates/<lang>/*.tmpl) y los mapas tipo→import (imports/<lang>.json).
package assets

import "embed"

// FS contiene los directorios snippets, templates e imports.
//
//go:embed snippets templates imports
var FS embed.FS
