package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dpa23/wari/internal/snippets"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func find(set snippets.Set, name string) (snippets.Snippet, bool) {
	for _, sn := range set.Snippets {
		if sn.Name == name {
			return sn, true
		}
	}
	return snippets.Snippet{}, false
}

func TestLoadSnippetsMergesUserAndProject(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(user, "snippets", "java.json"),
		`{"ctor": {"prefix": "ctor", "body": ["USER"]}, "mio": {"prefix": "mio", "body": ["mio$0"]}}`)
	writeFile(t, filepath.Join(project, "snippets", "java.json"),
		`{"ctor": {"prefix": "ctor", "body": ["PROJECT"]}, "proj": {"prefix": "proj", "body": ["p$0"]}}`)
	writeFile(t, filepath.Join(project, "snippets", "go.json"), `{"x": {"prefix": "x", "body": ["x"]}}`)

	sets, warns := loadSnippets(user, project)
	if len(warns) != 0 {
		t.Fatalf("avisos: %v", warns)
	}
	java := sets["java"]
	if sn, _ := find(java, "ctor"); strings.Join(sn.Body, "") != "PROJECT" {
		t.Errorf("el del proyecto debía ganar por nombre: %v", sn.Body)
	}
	for _, name := range []string{"mio", "proj", "main", "sout"} {
		if _, ok := find(java, name); !ok {
			t.Errorf("falta el snippet %q (los de usuario y proyecto se suman a los embebidos)", name)
		}
	}
	if _, ok := sets["kotlin"]; !ok {
		t.Error("faltan los embebidos de kotlin")
	}
	if _, ok := sets["go"]; !ok {
		t.Error("falta el lenguaje del proyecto")
	}
}

func TestLoadSnippetsSkipsInvalidFiles(t *testing.T) {
	user := t.TempDir()
	writeFile(t, filepath.Join(user, "snippets", "java.json"), `{ roto`)
	sets, warns := loadSnippets(user, "")
	if len(warns) != 1 || !strings.Contains(warns[0], "java.json") {
		t.Errorf("avisos = %v", warns)
	}
	if _, ok := find(sets["java"], "ctor"); !ok {
		t.Error("el archivo inválido no debía quitar los embebidos")
	}
}

func TestLoadSnippetsMissingDirsAreSilent(t *testing.T) {
	_, warns := loadSnippets(filepath.Join(t.TempDir(), "no"), filepath.Join(t.TempDir(), "tampoco"))
	if len(warns) != 0 {
		t.Errorf("avisos = %v", warns)
	}
}

func TestLoadTemplatesEmbedded(t *testing.T) {
	tpls, err := loadTemplates()
	if err != nil || len(tpls) == 0 {
		t.Fatalf("plantillas: %d, err=%v", len(tpls), err)
	}
}

func TestLoadImportsMergesAndValidatesEmbedded(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(user, "imports", "java.json"), `{"Mio": ["x.Mio"], "Logger": ["u.Logger"]}`)
	writeFile(t, filepath.Join(project, "imports", "java.json"), `{"Logger": ["p.Logger"]}`)
	writeFile(t, filepath.Join(project, "imports", "csharp.json"), `{ roto`)

	maps, warns := loadImports(user, project)
	if len(warns) != 1 || !strings.Contains(warns[0], "csharp.json") {
		t.Fatalf("avisos = %v", warns)
	}
	java := maps["java"]
	if got := java.Candidates("Logger"); len(got) != 1 || got[0] != "p.Logger" {
		t.Errorf("el proyecto debía ganar: %v", got)
	}
	if java.Candidates("Mio") == nil || java.Candidates("List") == nil {
		t.Error("los de usuario se suman a los embebidos")
	}
	if got := java.Candidates("List"); len(got) < 2 {
		t.Errorf("List debía tener varios candidatos: %v", got)
	}
	if maps["csharp"].Candidates("List") == nil || maps["kotlin"] == nil {
		t.Error("faltan los embebidos de csharp o kotlin")
	}
}
