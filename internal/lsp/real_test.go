package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealGopls prueba el cliente contra un servidor de verdad (gopls) si está
// en el PATH: initialize, didOpen, completar y definición. El gestor solo
// conoce jdtls, kotlin-language-server y csharp-ls, pero el cliente es genérico.
func TestRealGopls(t *testing.T) {
	bin, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls no está en el PATH")
	}
	dir := t.TempDir()
	src := "package main\n\nimport \"fmt\"\n\nfunc saludar() string { return \"hola\" }\n\nfunc main() {\n\tfmt.Prin\n\tsaludar()\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module ejemplo\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "serve")
	cmd.Dir = dir
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	c := NewClient(out, in, Options{ShutdownTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := "file://" + dir
	if err := c.Initialize(ctx, root); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	uri := "file://" + file
	if err := c.DidOpen(uri, "go", src); err != nil {
		t.Fatalf("didOpen: %v", err)
	}

	// Línea 7 (índice), tras "\tfmt.Prin": columna 9.
	var items []CompletionItem
	for i := 0; i < 20 && len(items) == 0; i++ {
		items, err = c.Complete(ctx, uri, Position{Line: 7, Character: 9})
		if err != nil {
			t.Fatalf("completion: %v", err)
		}
		if len(items) == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	found := false
	for _, it := range items {
		if strings.HasPrefix(it.Label, "Println") {
			found = true
		}
	}
	if !found {
		t.Errorf("completion sin Println entre %d elementos", len(items))
	}

	locs, err := c.Definition(ctx, uri, Position{Line: 8, Character: 3})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if len(locs) == 0 || locs[0].Range.Start.Line != 4 {
		t.Errorf("definition de saludar = %+v, quiero la línea 4", locs)
	}
	_ = c.Close()
}
