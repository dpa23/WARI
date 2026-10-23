// Package fontinstall deja la fuente de los iconos (la Nerd Font que trae el
// binario) donde la terminal la encuentre, sin que el usuario configure nada
// fuera de WARI. Las terminales dibujan los glifos que su fuente principal no
// tiene con otra fuente instalada (fontconfig en Linux, CoreText en macOS), así
// que basta con que la fuente exista en la carpeta de fuentes del usuario.
package fontinstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dpa23/wari/assets"
	"github.com/dpa23/wari/internal/proc"
)

// ErrUnsupported indica que en este sistema no se instala sola.
var ErrUnsupported = errors.New("fontinstall: instalación automática no disponible en este sistema")

// ownName es el archivo que instala WARI: lleva su prefijo para reconocerlo.
const ownName = "WARI-" + assets.FontFileName

// Dir es la carpeta de fuentes del usuario ("" si no se conoce).
func Dir(home string) string {
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".local", "share", "fonts")
	case "darwin":
		return filepath.Join(home, "Library", "Fonts")
	}
	return ""
}

// Present indica que ya hay una Nerd Font instalada: un archivo con "NerdFont"
// o "Nerd Font" en su nombre en las carpetas del usuario, o (con fc-list) una
// fuente del sistema con el glifo del óvalo de PHP, U+E73D.
func Present(ctx context.Context, home string) bool {
	dirs := []string{Dir(home), filepath.Join(home, ".fonts")}
	if runtime.GOOS == "linux" {
		dirs = append(dirs, "/usr/share/fonts", "/usr/local/share/fonts")
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		found := false
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err != nil || found {
				return filepath.SkipDir
			}
			n := strings.ToLower(e.Name())
			if !e.IsDir() && (strings.Contains(n, "nerdfont") || strings.Contains(n, "nerd font") || strings.Contains(n, "symbolsnerd")) {
				found = true
			}
			return nil
		})
		if found {
			return true
		}
	}
	if path, err := exec.LookPath("fc-list"); err == nil && os.Getenv("WARI_FC_LIST") != "0" {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if p, err := proc.Start(ctx, proc.Spec{Path: path, Args: []string{":charset=e73d", "family"}}); err == nil {
			out := make([]byte, 0, 256)
			buf := make([]byte, 256)
			for len(out) < 4096 {
				n, err := p.Stdout().Read(buf)
				out = append(out, buf[:n]...)
				if err != nil {
					break
				}
			}
			_ = p.Wait()
			p.Close()
			if strings.TrimSpace(string(out)) != "" {
				return true
			}
		}
	}
	return false
}

// Install escribe la fuente embebida en la carpeta de fuentes del usuario y
// refresca la caché de fontconfig si existe. Devuelve la ruta. No pisa un
// archivo propio idéntico.
func Install(ctx context.Context, home string) (string, error) {
	dir := Dir(home)
	if dir == "" {
		return "", ErrUnsupported
	}
	data, err := assets.NerdFont()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ownName)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == string(data) {
		return path, nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	if fc, err := exec.LookPath("fc-cache"); err == nil && os.Getenv("WARI_FC_CACHE") != "0" {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if p, err := proc.Start(ctx, proc.Spec{Path: fc, Args: []string{"-f", dir}}); err == nil {
			_ = p.Wait()
			p.Close()
		}
	}
	return path, nil
}

// Ensure instala la fuente si no hay ninguna Nerd Font y el usuario no lo
// desactivó con WARI_NO_FONT_INSTALL=1. installed dice si instaló algo.
func Ensure(ctx context.Context, home string) (path string, installed bool, err error) {
	if os.Getenv("WARI_NO_FONT_INSTALL") == "1" || home == "" || Dir(home) == "" {
		return "", false, nil
	}
	if Present(ctx, home) {
		return "", false, nil
	}
	path, err = Install(ctx, home)
	return path, err == nil, err
}
