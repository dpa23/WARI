package fontinstall

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dpa23/wari/assets"
)

func setup(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("solo Linux y macOS")
	}
	t.Setenv("WARI_FC_CACHE", "0") // sin tocar la caché de fontconfig
	t.Setenv("WARI_FC_LIST", "0")  // sin mirar las fuentes reales del sistema
	return t.TempDir()
}

func TestInstallWritesTheEmbeddedFontOnceAndIsIdempotent(t *testing.T) {
	home := setup(t)
	ctx := context.Background()
	path, err := Install(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := assets.NerdFont()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) || len(want) < 1<<20 {
		t.Fatalf("la fuente instalada no coincide con la embebida: %v (%d bytes)", err, len(got))
	}
	if filepath.Dir(path) != Dir(home) {
		t.Errorf("se instaló fuera de la carpeta de fuentes del usuario: %s", path)
	}
	st1, _ := os.Stat(path)
	if _, err := Install(ctx, home); err != nil {
		t.Fatal(err)
	}
	if st2, _ := os.Stat(path); !st2.ModTime().Equal(st1.ModTime()) {
		t.Error("una segunda instalación idéntica no debía reescribir el archivo")
	}
}

func TestEnsureInstallsOnlyWhenNoNerdFontExists(t *testing.T) {
	home := setup(t)
	ctx := context.Background()
	if Present(ctx, home) && !hasSystemNerd() {
		t.Fatal("un home vacío no debía tener Nerd Font")
	}
	if hasSystemNerd() {
		t.Skip("el sistema ya trae una Nerd Font en /usr/share/fonts")
	}
	if _, inst, err := Ensure(ctx, home); err != nil || !inst {
		t.Fatalf("sin fuente debía instalar: %v %v", inst, err)
	}
	if _, inst, err := Ensure(ctx, home); err != nil || inst {
		t.Fatalf("con la fuente ya puesta no debía instalar otra vez: %v %v", inst, err)
	}
	t.Setenv("WARI_NO_FONT_INSTALL", "1")
	other := t.TempDir()
	if _, inst, _ := Ensure(ctx, other); inst {
		t.Fatal("WARI_NO_FONT_INSTALL=1 debía impedir la instalación")
	}
}

// hasSystemNerd indica que /usr/share/fonts ya trae una Nerd Font (Present la
// vería aunque el home esté vacío).
func hasSystemNerd() bool {
	found := false
	_ = filepath.WalkDir("/usr/share/fonts", func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && (strings.Contains(strings.ToLower(e.Name()), "nerdfont") || strings.Contains(strings.ToLower(e.Name()), "nerd font")) {
			found = true
		}
		return nil
	})
	return found
}
