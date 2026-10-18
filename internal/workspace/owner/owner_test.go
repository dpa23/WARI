package owner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"a/b.go":     "a/b.go",
		"./a//b.go":  "a/b.go",
		"a/b/../c":   "a/c",
		"a/":         "a/",
		"":           "",
		".":          "",
		"..":         "",
		"../x":       "",
		"/a/b":       "a/b",
		"src/api///": "src/api/",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, se esperaba %q", in, got, want)
		}
	}
}

func TestRel(t *testing.T) {
	root := t.TempDir()
	if rel, in := Rel(root, filepath.Join(root, "a", "b.go")); rel != "a/b.go" || !in {
		t.Errorf("dentro: %q %v", rel, in)
	}
	if rel, in := Rel(root, "a/b.go"); rel != "a/b.go" || !in {
		t.Errorf("relativa: %q %v", rel, in)
	}
	if _, in := Rel(root, "/etc/passwd"); in {
		t.Error("/etc/passwd no está dentro de la raíz")
	}
	if _, in := Rel(root, filepath.Join(root, "..", "otro")); in {
		t.Error("..: fuera de la raíz")
	}
	// Raíz alcanzada por un enlace simbólico.
	link := filepath.Join(t.TempDir(), "enlace")
	if err := os.Symlink(root, link); err != nil {
		t.Skip("sin enlaces simbólicos")
	}
	if rel, in := Rel(link, filepath.Join(root, "x.go")); rel != "x.go" || !in {
		t.Errorf("enlace: %q %v", rel, in)
	}
}

func TestClaimConflict(t *testing.T) {
	tb := New()
	if c := tb.Claim(1, "a.go"); c != nil {
		t.Fatal(c)
	}
	if c := tb.Claim(1, "a.go"); c != nil {
		t.Fatal("reclamar lo propio no choca")
	}
	c := tb.Claim(2, "./a.go")
	if c == nil || c.Holder != 1 || c.Agent != 2 || c.Path != "a.go" {
		t.Fatalf("choque = %+v", c)
	}
	if h, _, ok := tb.Holder("a.go"); !ok || h != 1 {
		t.Fatal("el choque no debe cambiar al dueño")
	}
	if c := tb.Claim(2, "b.go"); c != nil {
		t.Fatal("archivos distintos no chocan")
	}
}

func TestFolderClaims(t *testing.T) {
	tb := New()
	tb.Claim(1, "src/api/")
	if c := tb.Check(2, "src/api/User.java"); c == nil || c.Holder != 1 || c.Claimed != "src/api/" {
		t.Fatalf("archivo dentro de carpeta reclamada: %+v", c)
	}
	if c := tb.Check(2, "src/other/X.java"); c != nil {
		t.Fatal("otra carpeta no choca")
	}
	if c := tb.Check(2, "src/apix/X.java"); c != nil {
		t.Fatal("prefijo de nombre no es la carpeta")
	}
	tb2 := New()
	tb2.Claim(1, "src/api/User.java")
	if c := tb2.Claim(2, "src/"); c == nil || c.Holder != 1 {
		t.Fatalf("carpeta con un archivo ajeno dentro: %+v", c)
	}
	if c := tb2.Claim(1, "src/"); c != nil {
		t.Fatal("el mismo agente puede reclamar la carpeta")
	}
}

func TestReleaseAndTakeover(t *testing.T) {
	tb := New()
	tb.Claim(1, "a.go")
	tb.Claim(1, "d/")
	tb.Claim(2, "b.go")
	prev, claimed := tb.Takeover(2, "a.go")
	if prev != 1 || claimed != "a.go" {
		t.Fatalf("takeover = %d %q", prev, claimed)
	}
	if c := tb.Check(1, "a.go"); c == nil || c.Holder != 2 {
		t.Fatal("tras el takeover el dueño es 2")
	}
	// Takeover de un archivo dentro de una carpeta ajena mueve la carpeta.
	if prev, claimed := tb.Takeover(2, "d/x.go"); prev != 1 || claimed != "d/" {
		t.Fatalf("takeover de carpeta = %d %q", prev, claimed)
	}
	if got := tb.Release(2); !reflect.DeepEqual(got, []string{"a.go", "b.go", "d/"}) {
		t.Fatalf("Release = %v", got)
	}
	if c := tb.Claim(1, "a.go"); c != nil {
		t.Fatal("liberado: ya no choca")
	}
	if tb.ReleasePath(2, "a.go") {
		t.Fatal("2 no puede liberar lo de 1")
	}
	if !tb.ReleasePath(1, "a.go") || len(tb.Claims(1)) != 0 {
		t.Fatal("ReleasePath")
	}
}

// En macOS el directorio temporal vive bajo un enlace (/var → /private/var):
// la ruta sin resolver y la raíz resuelta deben seguir coincidiendo, también
// para un archivo que aún no existe.
func TestRelPathThroughSymlinkedParent(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "enlace")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("sin enlaces simbólicos")
	}
	if rel, in := Rel(real, filepath.Join(link, "nuevo.go")); rel != "nuevo.go" || !in {
		t.Errorf("ruta por enlace, raíz real: %q %v", rel, in)
	}
	if rel, in := Rel(link, filepath.Join(real, "sub", "x.go")); in && rel != "sub/x.go" {
		t.Errorf("raíz por enlace: %q %v", rel, in)
	}
}
