package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakePath crea ejecutables vacíos en un PATH aislado.
func fakePath(t *testing.T, names ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH falso solo en Unix")
	}
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestServerForPath(t *testing.T) {
	tests := map[string]string{
		"src/A.java": "jdtls", "B.KT": "kotlin-language-server", "x.kts": "kotlin-language-server",
		"p/C.cs": "csharp-ls",
	}
	for path, want := range tests {
		d, ok := ServerForPath(path)
		if !ok || d.Name != want {
			t.Errorf("%s -> %q ok=%v, quería %q", path, d.Name, ok, want)
		}
	}
	if _, ok := ServerForPath("main.go"); ok {
		t.Error("go no tiene servidor conocido")
	}
	for _, d := range Known {
		if d.LanguageID == "" || d.Command == "" || len(d.Exts) == 0 {
			t.Errorf("definición incompleta: %+v", d)
		}
	}
}

func TestDetectAndAvailable(t *testing.T) {
	fakePath(t, "jdtls", "csharp-ls")
	if p, err := Known[0].Detect(); err != nil || filepath.Base(p) != "jdtls" {
		t.Errorf("Detect jdtls: %q, %v", p, err)
	}
	_, err := Known[1].Detect()
	if !errors.Is(err, ErrServerNotFound) {
		t.Errorf("kotlin debía faltar: %v", err)
	}
	av := Available()
	if len(av) != 2 || av[0].Name != "jdtls" || av[1].Name != "csharp-ls" {
		t.Errorf("Available=%v", av)
	}
}

func TestJdtlsArgsUseDataDirOutsideProject(t *testing.T) {
	root := t.TempDir()
	args := Known[0].CommandArgs(root)
	if len(args) != 2 || args[0] != "-data" {
		t.Fatalf("args=%v", args)
	}
	if rel, err := filepath.Rel(root, args[1]); err == nil && rel != ".." && len(rel) > 0 && rel[0] != '.' {
		t.Errorf("el directorio de datos está dentro del proyecto: %s", args[1])
	}
	if again := Known[0].CommandArgs(root); again[1] != args[1] {
		t.Error("el directorio debe ser estable por raíz")
	}
	if other := Known[0].CommandArgs(root + "2"); other[1] == args[1] {
		t.Error("raíces distintas necesitan directorios distintos")
	}
	if Known[2].CommandArgs(root) != nil {
		t.Error("csharp-ls no lleva argumentos")
	}
}
