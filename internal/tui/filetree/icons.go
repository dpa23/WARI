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
// "auto" elige Nerd (la fuente viene en assets/fonts) salvo con
// WARI_NERD_FONT=0 o en una consola TERM=linux/dumb, donde cae a emoji. Un valor desconocido es
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
		// Nerd Font es lo oficial y viene en el repo (assets/fonts); solo se
		// cae a emoji si se pide (WARI_NERD_FONT=0) o en una consola sin
		// glifos propios (TERM=linux o dumb).
		term := strings.ToLower(env("TERM"))
		if env("WARI_NERD_FONT") == "0" || term == "linux" || term == "dumb" {
			return IconsEmoji, nil
		}
		return IconsNerd, nil
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
