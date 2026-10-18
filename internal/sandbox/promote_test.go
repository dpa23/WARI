package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func promoteFixture(t *testing.T) (*Run, string, string) {
	t.Helper()
	root, out := t.TempDir(), t.TempDir()
	return &Run{Root: root, OutDir: out}, root, out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func skipNoSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("los enlaces simbólicos requieren privilegios en Windows")
	}
}

func TestPromoteFile(t *testing.T) {
	run, root, out := promoteFixture(t)
	write(t, filepath.Join(out, "target", "app.jar"), "jar")
	write(t, filepath.Join(root, "target", "app.jar"), "viejo")
	if err := Promote(run, "target/app.jar"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "target", "app.jar")); got != "jar" {
		t.Fatalf("contenido = %q", got)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "target"))
	if len(entries) != 1 {
		t.Fatalf("quedaron temporales: %v", entries)
	}
}

func TestPromoteCreatesParents(t *testing.T) {
	run, root, out := promoteFixture(t)
	write(t, filepath.Join(out, "dist", "a", "b.txt"), "b")
	if err := Promote(run, "dist/a/b.txt"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "dist", "a", "b.txt")); got != "b" {
		t.Fatalf("contenido = %q", got)
	}
}

func TestPromoteDirectory(t *testing.T) {
	run, root, out := promoteFixture(t)
	write(t, filepath.Join(out, "build", "x.txt"), "x")
	write(t, filepath.Join(out, "build", "sub", "y.txt"), "y")
	if err := os.MkdirAll(filepath.Join(out, "build", "vacio"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Promote(run, "build"); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(root, "build", "x.txt")) != "x" ||
		readFile(t, filepath.Join(root, "build", "sub", "y.txt")) != "y" {
		t.Fatal("contenido distinto")
	}
	if fi, err := os.Stat(filepath.Join(root, "build", "vacio")); err != nil || !fi.IsDir() {
		t.Fatalf("falta el directorio vacío: %v", err)
	}
}

func TestPromoteRejectsPathsOutsideRoot(t *testing.T) {
	run, root, out := promoteFixture(t)
	write(t, filepath.Join(filepath.Dir(out), "secreto.txt"), "s")
	write(t, filepath.Join(out, "ok.txt"), "ok")
	for _, rel := range []string{"", "..", "../secreto.txt", "a/../../x", "/etc/passwd", filepath.Join(out, "ok.txt")} {
		err := Promote(run, rel)
		if !errors.Is(err, ErrPromote) {
			t.Errorf("%q: err = %v, quería ErrPromote", rel, err)
		}
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatalf("el proyecto cambió: %v", entries)
	}
	if err := Promote(nil, "x"); err == nil {
		t.Error("run nulo debía fallar")
	}
}

func TestPromoteRejectsSymlinkArtifact(t *testing.T) {
	skipNoSymlinks(t)
	run, root, out := promoteFixture(t)
	outside := filepath.Join(t.TempDir(), "fuera.txt")
	write(t, outside, "fuera")
	if err := os.Symlink(outside, filepath.Join(out, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Promote(run, "link"); !errors.Is(err, ErrPromote) {
		t.Fatalf("err = %v", err)
	}
	// Enlace relativo que se queda dentro de las salidas: también se rechaza.
	write(t, filepath.Join(out, "real.txt"), "r")
	if err := os.Symlink("real.txt", filepath.Join(out, "rel")); err != nil {
		t.Fatal(err)
	}
	if err := Promote(run, "rel"); !errors.Is(err, ErrPromote) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("el proyecto cambió: %v", entries)
	}
}

func TestPromoteRejectsSymlinkInsideDirectoryWithoutPartialCopy(t *testing.T) {
	skipNoSymlinks(t)
	run, root, out := promoteFixture(t)
	write(t, filepath.Join(out, "build", "a.txt"), "a")
	write(t, filepath.Join(out, "build", "z.txt"), "z")
	if err := os.Symlink("/etc/passwd", filepath.Join(out, "build", "m-link")); err != nil {
		t.Fatal(err)
	}
	if err := Promote(run, "build"); !errors.Is(err, ErrPromote) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("copia parcial: %v", entries)
	}
}

func TestPromoteRejectsSymlinkedDirectories(t *testing.T) {
	skipNoSymlinks(t)
	// Directorio intermedio de origen enlazado.
	run, root, out := promoteFixture(t)
	real := filepath.Join(out, "real")
	write(t, filepath.Join(real, "f.txt"), "f")
	if err := os.Symlink(real, filepath.Join(out, "via")); err != nil {
		t.Fatal(err)
	}
	if err := Promote(run, "via/f.txt"); !errors.Is(err, ErrPromote) {
		t.Fatalf("origen: err = %v", err)
	}

	// Directorio de destino enlazado hacia fuera del proyecto.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "target")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(out, "target", "app.jar"), "jar")
	if err := Promote(run, "target/app.jar"); err == nil {
		t.Fatal("destino con enlace debía fallar")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("se escribió fuera del proyecto: %v", entries)
	}

	// Destino que ya es un enlace a otro archivo del proyecto.
	run2, root2, out2 := promoteFixture(t)
	write(t, filepath.Join(root2, "real.txt"), "intacto")
	if err := os.Symlink("real.txt", filepath.Join(root2, "app.jar")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(out2, "app.jar"), "jar")
	if err := Promote(run2, "app.jar"); !errors.Is(err, ErrPromote) {
		t.Fatalf("destino enlace: err = %v", err)
	}
	if got := readFile(t, filepath.Join(root2, "real.txt")); got != "intacto" {
		t.Fatalf("real.txt = %q", got)
	}
}

func TestPromoteMissingArtifact(t *testing.T) {
	run, _, _ := promoteFixture(t)
	if err := Promote(run, "no/existe"); err == nil {
		t.Fatal("artefacto inexistente debía fallar")
	}
}

func TestPromoteRejectsWhileRunning(t *testing.T) {
	installFake(t, "podman")
	t.Setenv("FAKE_HANG", "1")
	c := &Container{RunsDir: t.TempDir()}
	root := testProject(t)
	run, err := c.Run(context.Background(), Spec{Root: root, Image: "alpine", Command: []string{"sleep", "99"}})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Stop()

	write(t, filepath.Join(run.OutDir, "artifact.txt"), "nuevo")
	err = Promote(run, "artifact.txt")
	if !errors.Is(err, ErrPromote) || !strings.Contains(err.Error(), "la ejecución sigue en marcha") {
		t.Fatalf("Promote debía rechazar mientras la ejecución sigue en marcha, obtuvo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "artifact.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no debía copiarse nada mientras la ejecución sigue en marcha")
	}

	// Al terminar la ejecución, Promote sí debe permitir la copia
	if err := run.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := Promote(run, "artifact.txt"); err != nil {
		t.Fatalf("Promote tras terminar: %v", err)
	}
	if readFile(t, filepath.Join(root, "artifact.txt")) != "nuevo" {
		t.Fatal("el archivo no se copió tras terminar la ejecución")
	}
}
