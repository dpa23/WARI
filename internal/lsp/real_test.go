package lsp

import (
	"context"
	"errors"
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

// TestRealServers prueba los servidores que WARI usa de verdad (jdtls,
// kotlin-language-server, csharp-ls) cuando están en el PATH: initialize,
// completar y definición sobre un archivo mínimo. Arrancan lento (jdtls, un
// minuto), así que se omite con -short.
func TestRealServers(t *testing.T) {
	if testing.Short() {
		t.Skip("servidores reales lentos: se omite con -short")
	}
	cases := []struct {
		server  string
		file    string
		src     string
		prefix  string // línea y columna tras el prefijo a completar
		line    int
		col     int
		want    string
		defLine int // posición de la llamada a ayudante() en la línea sana
		defCol  int
		defWant int
		setup   map[string]string
	}{
		{
			server: "jdtls", file: "src/Main.java",
			src:  "public class Main {\n    static String ayudante() { return \"x\"; }\n    public static void main(String[] a) {\n        String s = ayudante();\n        s.leng\n        ayudante();\n    }\n}\n",
			line: 4, col: 14, want: "length",
			defLine: 3, defCol: 22, defWant: 1,
		},
		{
			server: "kotlin-language-server", file: "Main.kt",
			src:  "fun ayudante(): String = \"x\"\n\nfun main() {\n    val s = ayudante()\n    s.leng\n    ayudante()\n}\n",
			line: 4, col: 10, want: "length",
			defLine: 3, defCol: 14, defWant: 0,
		},
		{
			server: "csharp-ls", file: "Program.cs",
			src:  "class P {\n    static string Ayudante() => \"x\";\n    static void Main() {\n        var s = Ayudante();\n        s.Leng\n        Ayudante();\n    }\n}\n",
			line: 4, col: 14, want: "Length",
			defLine: 3, defCol: 20, defWant: 1,
			setup: map[string]string{"p.csproj": "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.server, func(t *testing.T) {
			var def ServerDef
			for _, d := range Known {
				if d.Name == tc.server {
					def = d
				}
			}
			bin, err := def.Detect()
			if err != nil {
				t.Skipf("%s no está en el PATH", tc.server)
			}
			dir := t.TempDir()
			for name, body := range tc.setup {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(dir, tc.file)
			if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(tc.src), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, def.CommandArgs(dir)...)
			cmd.Dir = dir
			in, _ := cmd.StdinPipe()
			out, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

			c := NewClient(out, in, Options{ShutdownTimeout: time.Second})
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			if err := c.Initialize(ctx, "file://"+dir); err != nil {
				if tc.server == "kotlin-language-server" && errors.Is(err, ErrClosed) {
					// Con JDK 25.0.4.1 el servidor muere al leer la versión de Java
					// (IllegalArgumentException en JavaVersion.parse): es de ese servidor.
					t.Skipf("%s no arranca con este JDK: %v", tc.server, err)
				}
				t.Fatalf("initialize: %v", err)
			}
			uri := "file://" + file
			if err := c.DidOpen(uri, def.LanguageID, tc.src); err != nil {
				t.Fatalf("didOpen: %v", err)
			}
			found := false
			var n int
			for i := 0; i < 60 && !found; i++ {
				items, err := c.Complete(ctx, uri, Position{Line: tc.line, Character: tc.col})
				if err != nil {
					t.Fatalf("completion: %v", err)
				}
				n = len(items)
				for _, it := range items {
					if strings.HasPrefix(it.Label, tc.want) {
						found = true
					}
				}
				if !found {
					time.Sleep(2 * time.Second)
				}
			}
			if !found {
				t.Errorf("completion sin %s (%d elementos)", tc.want, n)
			}
			locs, err := c.Definition(ctx, uri, Position{Line: tc.defLine, Character: tc.defCol})
			if err != nil {
				t.Fatalf("definition: %v", err)
			}
			if len(locs) == 0 || locs[0].Range.Start.Line != tc.defWant {
				t.Errorf("definition = %+v, quiero la línea %d", locs, tc.defWant)
			}
			_ = c.Close()
		})
	}
}
