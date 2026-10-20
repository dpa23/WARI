// Package theme resuelve si el fondo de la terminal es oscuro y ofrece colores
// adaptativos. Reemplaza a lipgloss/compat, que lo consulta al importarse
// (antes de main, con la terminal en modo normal): una respuesta tardía se
// escribía en pantalla como "^[[?1;2c". Aquí no se consulta nunca por cuenta
// propia: lo hace el TUI con bubbletea y se puede evitar con WARI_THEME o
// COLORFGBG.
package theme

import (
	"image/color"
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	mu       sync.Mutex
	resolved bool
	dark     = true
)

// Dark indica si el fondo es oscuro. Orden: SetDark, WARI_THEME=dark|light y
// COLORFGBG ("fg;bg" de rxvt, xterm y otras). Sin ninguna pista asume oscuro
// hasta que el TUI pregunte a la terminal con tea.RequestBackgroundColor (ver
// NeedsQuery y SetDark): bubbletea hace la consulta con la terminal ya en modo
// crudo, así que una respuesta tardía no se escribe en pantalla.
func Dark() bool {
	mu.Lock()
	defer mu.Unlock()
	resolve()
	return dark
}

// NeedsQuery indica que el entorno no dice el color de fondo y conviene
// preguntarlo a la terminal.
func NeedsQuery() bool {
	mu.Lock()
	defer mu.Unlock()
	resolve()
	return !resolved
}

// SetDark fija el valor (respuesta de la terminal, pruebas).
func SetDark(v bool) {
	mu.Lock()
	defer mu.Unlock()
	dark, resolved = v, true
}

var envResolved bool

// resolve aplica las variables de entorno una sola vez; mu debe estar tomado.
func resolve() {
	if envResolved {
		return
	}
	envResolved = true
	if resolved {
		return
	}
	if v, ok := fromEnv(os.Getenv); ok {
		dark, resolved = v, true
	}
}

func fromEnv(env func(string) string) (dark, known bool) {
	switch strings.ToLower(env("WARI_THEME")) {
	case "dark":
		return true, true
	case "light":
		return false, true
	}
	if parts := strings.Split(env("COLORFGBG"), ";"); len(parts) >= 2 {
		if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			// 0-6 y 8 son colores oscuros del ANSI de 16; 7 y 9-15, claros.
			return bg <= 6 || bg == 8, true
		}
	}
	return true, false
}

// AdaptiveColor elige Light u Oscuro según el fondo de la terminal.
type AdaptiveColor struct {
	Light color.Color
	Dark  color.Color
}

// RGBA implementa color.Color.
func (c AdaptiveColor) RGBA() (uint32, uint32, uint32, uint32) {
	if Dark() {
		return c.Dark.RGBA()
	}
	return c.Light.RGBA()
}
