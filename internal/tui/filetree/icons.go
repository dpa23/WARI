package filetree

import (
	"path/filepath"
	"strings"
)

// Iconos de Nerd Fonts (https://www.nerdfonts.com/cheat-sheet). Cada glifo
// ocupa una celda; sin una Nerd Font en la terminal se ven como cuadros, por
// eso el árbol los dibuja solo si se activan con SetIcons.
const (
	iconDir      = ""
	iconDirOpen  = ""
	iconFile     = ""
	iconText     = ""
	iconImage    = ""
	iconArchive  = ""
	iconLock     = ""
	iconGear     = ""
	iconGit      = ""
	iconShell    = ""
	iconMarkdown = ""
	iconGo       = ""
	iconJava     = ""
	iconKotlin   = ""
	iconCSharp   = ""
	iconPython   = ""
	iconJS       = ""
	iconTS       = ""
	iconHTML     = ""
	iconCSS      = ""
	iconJSON     = ""
	iconRust     = ""
	iconC        = ""
	iconCpp      = ""
	iconDocker   = ""
	iconSQL      = ""
)

var extIcons = map[string]string{
	".go": iconGo, ".java": iconJava, ".kt": iconKotlin, ".kts": iconKotlin,
	".cs": iconCSharp, ".py": iconPython, ".js": iconJS, ".mjs": iconJS,
	".ts": iconTS, ".tsx": iconTS, ".jsx": iconJS, ".html": iconHTML,
	".css": iconCSS, ".scss": iconCSS, ".json": iconJSON, ".jsonl": iconJSON,
	".md": iconMarkdown, ".txt": iconText, ".rs": iconRust, ".c": iconC,
	".h": iconC, ".cpp": iconCpp, ".hpp": iconCpp, ".sql": iconSQL,
	".sh": iconShell, ".bash": iconShell, ".zsh": iconShell,
	".yml": iconGear, ".yaml": iconGear, ".toml": iconGear, ".xml": iconGear,
	".mod": iconGo, ".sum": iconLock, ".lock": iconLock,
	".png": iconImage, ".jpg": iconImage, ".jpeg": iconImage, ".gif": iconImage,
	".svg": iconImage, ".ico": iconImage,
	".zip": iconArchive, ".tar": iconArchive, ".gz": iconArchive, ".jar": iconArchive,
}

var nameIcons = map[string]string{
	"dockerfile": iconDocker, ".gitignore": iconGit, ".gitattributes": iconGit,
	"makefile": iconGear, "license": iconText, "go.mod": iconGo, "go.sum": iconLock,
}

// iconFor devuelve el icono de un archivo o carpeta.
func iconFor(name string, dir, expanded bool) string {
	if dir {
		if expanded {
			return iconDirOpen
		}
		return iconDir
	}
	lower := strings.ToLower(name)
	if ic, ok := nameIcons[lower]; ok {
		return ic
	}
	if ic, ok := extIcons[filepath.Ext(lower)]; ok {
		return ic
	}
	return iconFile
}
