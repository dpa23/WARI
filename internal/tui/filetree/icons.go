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

// icon es el icono de un archivo en cada juego. color es el color de marca del
// logo oficial (solo en Nerd Font: el emoji ya trae el suyo); "" no pinta.
type icon struct{ nerd, emoji, color string }

func (i icon) in(mode IconMode) string {
	switch mode {
	case IconsNerd:
		return i.nerd
	case IconsEmoji:
		return i.emoji
	}
	return ""
}

// Los glifos son los logos de los lenguajes y herramientas de la Nerd Font
// incluida en assets/fonts (devicons y Material Design); los colores, los de
// marca de cada uno. Los codepoints se comprobaron dibujándolos con esa fuente.
var (
	iconDir      = icon{nerd: "\uf07b", emoji: "📁", color: "#7aa2f7"}     // nf-fa-folder
	iconDirOpen  = icon{nerd: "\uf07c", emoji: "📂", color: "#7aa2f7"}     // nf-fa-folder_open
	iconFile     = icon{nerd: "\uf15b", emoji: "📄"}                       // nf-fa-file
	iconText     = icon{nerd: "\uf15c", emoji: "📝", color: "#9aa5b1"}     // nf-fa-file_text
	iconImage    = icon{nerd: "\uf1c5", emoji: "📷", color: "#a074c4"}     // nf-fa-file_image_o
	iconArchive  = icon{nerd: "\uf1c6", emoji: "📦", color: "#cbcb41"}     // nf-fa-file_archive_o
	iconLock     = icon{nerd: "\ue672", emoji: "🔒", color: "#9aa5b1"}     // nf-seti-lock
	iconGear     = icon{nerd: "\uf013", emoji: "🔧", color: "#9aa5b1"}     // nf-fa-cog
	iconGit      = icon{nerd: "\ue702", emoji: "🔀", color: "#f05032"}     // nf-dev-git
	iconShell    = icon{nerd: "\ue795", emoji: "🐚", color: "#89e051"}     // nf-dev-terminal
	iconMarkdown = icon{nerd: "\ue73e", emoji: "📘", color: "#519aba"}     // nf-dev-markdown
	iconGo       = icon{nerd: "\U000f07d3", emoji: "🐹", color: "#00add8"} // nf-md-language_go
	iconJava     = icon{nerd: "\ue738", emoji: "☕", color: "#e76f00"}     // nf-dev-java
	iconKotlin   = icon{nerd: "\ue634", emoji: "🟣", color: "#7f52ff"}     // nf-seti-kotlin
	iconCSharp   = icon{nerd: "\U000f031b", emoji: "🔷", color: "#9b4f96"} // nf-md-language_csharp
	iconPython   = icon{nerd: "\ue73c", emoji: "🐍", color: "#3776ab"}     // nf-dev-python
	iconPHP      = icon{nerd: "\ue73d", emoji: "🐘", color: "#777bb4"}     // nf-dev-php: el óvalo
	iconJS       = icon{nerd: "\U000f031e", emoji: "🟨", color: "#f7df1e"} // nf-md-language_javascript
	iconTS       = icon{nerd: "\U000f06e6", emoji: "🟦", color: "#3178c6"} // nf-md-language_typescript
	iconHTML     = icon{nerd: "\ue736", emoji: "🌐", color: "#e34f26"}     // nf-dev-html5
	iconCSS      = icon{nerd: "\ue749", emoji: "🎨", color: "#1572b6"}     // nf-dev-css3
	iconJSON     = icon{nerd: "\ue60b", emoji: "📋", color: "#cbcb41"}     // nf-seti-json
	iconXML      = icon{nerd: "\U000f05c0", emoji: "📰", color: "#e37933"} // nf-md-xml
	iconYAML     = icon{nerd: "\ue6a8", emoji: "🔧", color: "#cb171e"}     // nf-seti-yml
	iconRust     = icon{nerd: "\ue7a8", emoji: "🦀", color: "#dea584"}     // nf-dev-rust
	iconC        = icon{nerd: "\ue61e", emoji: "🔩", color: "#599eff"}     // nf-seti-c
	iconCpp      = icon{nerd: "\ue61d", emoji: "🔩", color: "#519aba"}     // nf-seti-cpp
	iconDocker   = icon{nerd: "\ue7b0", emoji: "🐳", color: "#2496ed"}     // nf-dev-docker
	iconSQL      = icon{nerd: "\ue706", emoji: "💾", color: "#e38c00"}     // nf-dev-database
	iconGradle   = icon{nerd: "\ue660", emoji: "🐘", color: "#1ba8cb"}     // nf-seti-gradle
	iconMaven    = icon{nerd: "\U000f0b37", emoji: "☕", color: "#c71a36"} // nf-md-language_java (taza) en rojo Maven
	iconSpring   = icon{nerd: "\U000f032a", emoji: "🍃", color: "#6db33f"} // nf-md-leaf
	iconNode     = icon{nerd: "\U000f0399", emoji: "🟩", color: "#5fa04e"} // nf-md-nodejs
	iconNPM      = icon{nerd: "\ue71e", emoji: "📦", color: "#cb3837"}     // nf-dev-npm
	iconEnv      = icon{nerd: "\uf023", emoji: "🔑", color: "#faf743"}     // nf-fa-lock
)

var extIcons = map[string]icon{
	".go": iconGo, ".java": iconJava, ".kt": iconKotlin, ".kts": iconKotlin,
	".cs": iconCSharp, ".csproj": iconCSharp, ".sln": iconCSharp,
	".py": iconPython, ".php": iconPHP, ".js": iconJS, ".mjs": iconJS, ".cjs": iconJS,
	".ts": iconTS, ".tsx": iconTS, ".jsx": iconJS, ".html": iconHTML, ".htm": iconHTML,
	".css": iconCSS, ".scss": iconCSS, ".sass": iconCSS, ".json": iconJSON, ".jsonl": iconJSON,
	".md": iconMarkdown, ".txt": iconText, ".rs": iconRust, ".c": iconC,
	".h": iconC, ".cpp": iconCpp, ".hpp": iconCpp, ".cc": iconCpp, ".sql": iconSQL,
	".sh": iconShell, ".bash": iconShell, ".zsh": iconShell,
	".yml": iconYAML, ".yaml": iconYAML, ".toml": iconGear, ".xml": iconXML,
	".gradle": iconGradle, ".properties": iconGear, ".ini": iconGear, ".conf": iconGear,
	".mod": iconGo, ".sum": iconLock, ".lock": iconLock,
	".png": iconImage, ".jpg": iconImage, ".jpeg": iconImage, ".gif": iconImage,
	".svg": iconImage, ".ico": iconImage, ".webp": iconImage,
	".zip": iconArchive, ".tar": iconArchive, ".gz": iconArchive, ".jar": iconArchive,
	".war": iconArchive,
}

var nameIcons = map[string]icon{
	"dockerfile": iconDocker, "docker-compose.yml": iconDocker, "docker-compose.yaml": iconDocker,
	".gitignore": iconGit, ".gitattributes": iconGit, ".gitmodules": iconGit,
	"makefile": iconGear, "license": iconText, "go.mod": iconGo, "go.sum": iconLock,
	"pom.xml": iconMaven, "build.gradle": iconGradle, "build.gradle.kts": iconGradle,
	"settings.gradle": iconGradle, "settings.gradle.kts": iconGradle, "gradlew": iconGradle,
	".nvmrc": iconNode, ".node-version": iconNode, "package.json": iconNPM, "package-lock.json": iconNPM, ".env": iconEnv,
	"application.yml": iconSpring, "application.yaml": iconSpring, "application.properties": iconSpring,
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
	switch {
	case strings.HasPrefix(lower, ".env"):
		return iconEnv
	case strings.HasPrefix(lower, "application-") && (strings.HasSuffix(lower, ".yml") ||
		strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".properties")):
		return iconSpring
	}
	if ic, ok := extIcons[filepath.Ext(lower)]; ok {
		return ic
	}
	return iconFile
}
