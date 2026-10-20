// Package theme resuelve si el fondo de la terminal es oscuro y ofrece colores
// adaptativos. Reemplaza a lipgloss/compat, que lo consulta al importarse
// (antes de main): esa consulta manda secuencias a la terminal en modo normal
// y, si la respuesta llega tarde, la terminal la escribe en pantalla como
// "^[[?1;2c". Aquí la consulta es perezosa (solo la hace el TUI, nunca
// -version ni -prompt) y se puede evitar con WARI_THEME o COLORFGBG.
package theme

import (
	"image/color"
	"os"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
)

var (
	mu       sync.Mutex
	resolved bool
	dark     bool
)

// Dark indica si el fondo es oscuro. Orden: SetDark, WARI_THEME=dark|light,
// COLORFGBG ("fg;bg" de rxvt, xterm y otras) y, como último recurso, la
// consulta a la terminal.
func Dark() bool {
	mu.Lock()
	defer mu.Unlock()
	if !resolved {
		dark = detect(os.Getenv, func() bool { return lipgloss.HasDarkBackground(os.Stdin, os.Stdout) })
		resolved = true
	}
	return dark
}

// SetDark fija el valor sin consultar a la terminal (pruebas y -theme).
func SetDark(v bool) {
	mu.Lock()
	defer mu.Unlock()
	dark, resolved = v, true
}

func detect(env func(string) string, query func() bool) bool {
	switch strings.ToLower(env("WARI_THEME")) {
	case "dark":
		return true
	case "light":
		return false
	}
	if parts := strings.Split(env("COLORFGBG"), ";"); len(parts) >= 2 {
		if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			// 0-6 y 8 son colores oscuros del ANSI de 16; 7 y 9-15, claros.
			return bg <= 6 || bg == 8
		}
	}
	return query()
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
