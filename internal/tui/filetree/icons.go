package filetree

import (
	"fmt"
	"path/filepath"
	"strings"
)

// IconMode elige el juego de iconos del árbol.
type IconMode int

const (
	// IconsOff no dibuja iconos.
	IconsOff IconMode = iota
	// IconsNerd usa glifos de Nerd Fonts (https://www.nerdfonts.com/cheat-sheet):
	// una celda cada uno, pero sin una Nerd Font en la terminal se ven como cuadros.
	IconsNerd
	// IconsEmoji usa emoji, que cualquier terminal moderna dibuja sin fuente
	// especial (ocupan dos celdas).
	IconsEmoji
)

// ParseIconMode interpreta el valor de -icons: auto, nerd, emoji u off.
// "auto" elige Nerd si la terminal trae esos glifos de serie (kitty, WezTerm,
// Ghostty) o lo pide WARI_NERD_FONT=1, y si no emoji. Un valor desconocido es
// un error.
func ParseIconMode(v string, env func(string) string) (IconMode, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "no", "false", "0", "none":
		return IconsOff, nil
	case "nerd":
		return IconsNerd, nil
	case "emoji":
		return IconsEmoji, nil
	case "", "auto", "on", "true", "1":
		if env == nil {
			env = func(string) string { return "" }
		}
		term := strings.ToLower(env("TERM") + " " + env("TERM_PROGRAM"))
		if env("WARI_NERD_FONT") == "1" || strings.Contains(term, "kitty") ||
			strings.Contains(term, "wezterm") || strings.Contains(term, "ghostty") {
			return IconsNerd, nil
		}
		return IconsEmoji, nil
	}
	return IconsOff, fmt.Errorf("-icons: %q no es auto, nerd, emoji ni off", v)
}

type icon struct{ nerd, emoji string }

func (i icon) in(mode IconMode) string {
	switch mode {
	case IconsNerd:
		return i.nerd
	case IconsEmoji:
		return i.emoji
	}
	return ""
}

var (
	iconDir      = icon{nerd: "", emoji: "📁"}
	iconDirOpen  = icon{nerd: "", emoji: "📂"}
	iconFile     = icon{nerd: "", emoji: "📄"}
	iconText     = icon{nerd: "", emoji: "📝"}
	iconImage    = icon{nerd: "", emoji: "📷"}
	iconArchive  = icon{nerd: "", emoji: "📦"}
	iconLock     = icon{nerd: "", emoji: "🔒"}
	iconGear     = icon{nerd: "", emoji: "🔧"}
	iconGit      = icon{nerd: "", emoji: "🔀"}
	iconShell    = icon{nerd: "", emoji: "🐚"}
	iconMarkdown = icon{nerd: "", emoji: "📘"}
	iconGo       = icon{nerd: "", emoji: "🐹"}
	iconJava     = icon{nerd: "", emoji: "☕"}
	iconKotlin   = icon{nerd: "", emoji: "🟣"}
	iconCSharp   = icon{nerd: "", emoji: "🔷"}
	iconPython   = icon{nerd: "", emoji: "🐍"}
	iconJS       = icon{nerd: "", emoji: "🟨"}
	iconTS       = icon{nerd: "", emoji: "🟦"}
	iconHTML     = icon{nerd: "", emoji: "🌐"}
	iconCSS      = icon{nerd: "", emoji: "🎨"}
	iconJSON     = icon{nerd: "", emoji: "📋"}
	iconRust     = icon{nerd: "", emoji: "🦀"}
	iconC        = icon{nerd: "", emoji: "🔩"}
	iconCpp      = icon{nerd: "", emoji: "🔩"}
	iconDocker   = icon{nerd: "", emoji: "🐳"}
	iconSQL      = icon{nerd: "", emoji: "💾"}
)

var extIcons = map[string]icon{
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

var nameIcons = map[string]icon{
	"dockerfile": iconDocker, ".gitignore": iconGit, ".gitattributes": iconGit,
	"makefile": iconGear, "license": iconText, "go.mod": iconGo, "go.sum": iconLock,
}

// iconFor devuelve el icono de un archivo o carpeta.
func iconFor(name string, dir, expanded bool) icon {
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
