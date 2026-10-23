// Package icons es la tabla central de glifos del panel de agentes (estados,
// CLIs y acciones de la interfaz) con cuatro juegos: Nerd Font, emoji, ASCII y
// ninguno. No importa a ningún otro paquete de WARI.
//
// Anchos: en Nerd y ASCII cada glifo mide exactamente 1 celda; los emoji
// pueden medir 2.
package icons

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Mode elige el juego de glifos.
type Mode int

const (
	// Off no dibuja glifos (las funciones devuelven "").
	Off Mode = iota
	// Nerd usa glifos de Nerd Fonts (https://www.nerdfonts.com/cheat-sheet):
	// una celda cada uno, pero sin una Nerd Font se ven como cuadros.
	Nerd
	// Emoji se dibuja sin fuente especial (hasta dos celdas).
	Emoji
	// ASCII es el respaldo universal: un carácter por glifo.
	ASCII
)

var current atomic.Int32

func init() { current.Store(int32(Emoji)) }

// Use fija el juego de glifos activo (seguro entre goroutines).
func Use(m Mode) { current.Store(int32(m)) }

// Current devuelve el juego activo; por omisión Emoji.
func Current() Mode { return Mode(current.Load()) }

// Glyph es la clave de un glifo.
type Glyph string

// Estados de agente.
const (
	Working  Glyph = "working"
	Waiting  Glyph = "waiting" // espera tu respuesta
	Ready    Glyph = "ready"   // inactivo, listo
	Queued   Glyph = "queued"
	Paused   Glyph = "paused"
	Stopped  Glyph = "stopped"
	Failed   Glyph = "failed"
	New      Glyph = "new"
	Exited   Glyph = "exited"
	Conflict Glyph = "conflict"
	Budget   Glyph = "budget"
)

// CLIs.
const (
	Claude   Glyph = "claude"
	Agy      Glyph = "agy"
	Codex    Glyph = "codex"
	OpenCode Glyph = "opencode"
	Terminal Glyph = "terminal"
)

// Interfaz.
const (
	Add    Glyph = "add" // pestaña "+"
	Edit   Glyph = "edit"
	Folder Glyph = "folder"
)

type set struct{ nerd, emoji, ascii string }

// Los códigos Nerd son de Font Awesome (rango PUA U+F000..) y se comprobaron
// contra JetBrainsMonoNerdFont-Regular.ttf con fc-query.
var states = map[Glyph]set{
	Working:  {"", "🔄", "*"}, // nf-fa-circle_o_notch
	Waiting:  {"", "❓", "?"}, // nf-fa-question_circle
	Ready:    {"", "🟢", "o"}, // nf-fa-circle
	Queued:   {"", "🕐", "."}, // nf-fa-clock_o
	Paused:   {"", "💤", "="}, // nf-fa-pause
	Stopped:  {"", "🛑", "#"}, // nf-fa-stop
	Failed:   {"", "❌", "!"}, // nf-fa-times_circle
	New:      {"", "✨", "+"}, // nf-fa-star
	Exited:   {"", "🚪", "-"}, // nf-fa-sign_out
	Conflict: {"", "💥", "~"}, // nf-fa-warning (exclamation_triangle)
	Budget:   {"", "💰", "$"}, // nf-fa-dollar (usd)
}

var clis = map[Glyph]set{
	Claude:   {"", "🧠", "C"}, // nf-fa-asterisk
	Agy:      {"", "🚀", "A"}, // nf-fa-rocket
	Codex:    {"", "📜", "X"}, // nf-fa-code
	OpenCode: {"", "🧩", "O"}, // nf-fa-cube
	Terminal: {"", "💻", ">"}, // nf-fa-terminal
}

var ui = map[Glyph]set{
	Add:    {"", "➕", "+"}, // nf-fa-plus
	Edit:   {"", "📝", "e"}, // nf-fa-pencil
	Folder: {"", "📁", "/"}, // nf-fa-folder
}

func (s set) in(m Mode) string {
	switch m {
	case Nerd:
		return s.nerd
	case Emoji:
		return s.emoji
	case ASCII:
		return s.ascii
	}
	return ""
}

func lookup(t map[Glyph]set, name string) string {
	return t[Glyph(name)].in(Current())
}

// State devuelve el glifo de un estado de agente en el modo actual ("" en Off
// o si el nombre no existe).
func State(name string) string { return lookup(states, name) }

// CLI devuelve el glifo de una CLI en el modo actual.
func CLI(name string) string { return lookup(clis, name) }

// UI devuelve el glifo de un elemento de la interfaz en el modo actual.
func UI(name string) string { return lookup(ui, name) }

// ParseMode interpreta auto, nerd, emoji, ascii u off. "auto" elige Nerd (la
// fuente viene en assets/fonts; scripts/install-font.sh la instala) salvo con
// WARI_NERD_FONT=0 o en una consola TERM=linux/dumb, donde cae a emoji. Un
// valor desconocido es un error.
func ParseMode(v string, env func(string) string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "no", "false", "0", "none":
		return Off, nil
	case "nerd":
		return Nerd, nil
	case "emoji":
		return Emoji, nil
	case "ascii":
		return ASCII, nil
	case "", "auto", "on", "true", "1":
		if env == nil {
			env = func(string) string { return "" }
		}
		// Nerd Font es lo oficial y viene en el repo (assets/fonts); solo se
		// cae a emoji si se pide (WARI_NERD_FONT=0) o en una consola sin
		// glifos propios (TERM=linux o dumb).
		term := strings.ToLower(env("TERM"))
		if env("WARI_NERD_FONT") == "0" || term == "linux" || term == "dumb" {
			return Emoji, nil
		}
		return Nerd, nil
	}
	return Off, fmt.Errorf("icons: %q no es auto, nerd, emoji, ascii ni off", v)
}
