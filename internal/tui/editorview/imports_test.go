package editorview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/assets"
	"github.com/dpa23/wari/internal/editor"
	"github.com/dpa23/wari/internal/imports"
)

func embeddedImports(t *testing.T) map[string]imports.Map {
	t.Helper()
	maps, err := imports.LoadDir(assets.FS, "imports")
	if err != nil {
		t.Fatal(err)
	}
	return maps
}

func withImports(t *testing.T, name, content string, line int) Model {
	t.Helper()
	m, _ := newFile(t, name, content, 60, 20)
	m.SetImports(embeddedImports(t))
	m.Buffer().SetCursorPos(editor.Pos{Line: line, Col: 1 << 20})
	return m
}

func TestImportSuggestionAppearsWhenTypingKnownType(t *testing.T) {
	m := withImports(t, "A.java", "package a;\n\nclass A {\n    \n}", 3)
	for _, tc := range []struct{ typed, want string }{
		{"L", ""}, {"i", ""}, {"s", ""}, {"t", "List"}, {"x", ""}, // "Listx" no está en el mapa
	} {
		m = typeAll(m, tc.typed)
		if got := m.ImportSuggestion(); got != tc.want {
			t.Fatalf("tras %q: sugerencia %q, quiero %q (línea %q)", tc.typed, got, tc.want, string(m.Buffer().Line(3)))
		}
	}
}

func TestImportSuggestionNotShownWhenImportedOrOnImportLine(t *testing.T) {
	m := withImports(t, "A.java", "package a;\n\nimport java.util.List;\n\nclass A {\n    List\n}", 5)
	m, _ = m.Update(press(tea.KeyLeft, 0))
	if got := m.ImportSuggestion(); got != "" {
		t.Errorf("List ya está importado: %q", got)
	}
	m = withImports(t, "A.java", "package a;\n\nimport java.util.Mapx;\nimport java.awt.List;\n\nclass A {}", 3)
	m, _ = m.Update(press(tea.KeyLeft, 0))
	if got := m.ImportSuggestion(); got != "" {
		t.Errorf("en una línea de import no se sugiere: %q", got)
	}
	// Otro tipo List importado (java.awt.List) también evita la sugerencia.
	m = withImports(t, "A.java", "package a;\n\nimport java.awt.List;\n\nclass A {\n    List\n}", 5)
	m, _ = m.Update(press(tea.KeyLeft, 0))
	if got := m.ImportSuggestion(); got != "" {
		t.Errorf("%q", got)
	}
}

func TestAddImportInsertsOnceInPlaceAndIsOneUndoStep(t *testing.T) {
	src := "package a;\n\nimport java.util.Map;\n\nclass A {\n    List\n}"
	m := withImports(t, "A.java", src, 5)
	m, _ = m.Update(press(tea.KeyLeft, 0)) // cursor dentro de "List"
	if m.ImportSuggestion() != "List" {
		t.Fatal("debía sugerir List")
	}
	cur := m.Buffer().Cursor()

	if !m.AddImport("java.util.List") {
		t.Fatal("debía insertar")
	}
	want := "package a;\n\nimport java.util.List;\nimport java.util.Map;\n\nclass A {\n    List\n}"
	if got := m.Buffer().String(); got != want {
		t.Fatalf("\n%s\nquiero:\n%s", got, want)
	}
	if c := m.Buffer().Cursor(); c.Line != cur.Line+1 || c.Col != cur.Col {
		t.Errorf("el cursor debía seguir en su texto: antes %+v, ahora %+v", cur, c)
	}
	if !m.Dirty() {
		t.Error("debía quedar sin guardar")
	}
	if m.ImportSuggestion() != "" {
		t.Error("tras importar ya no hay sugerencia")
	}
	if m.AddImport("java.util.List") {
		t.Error("repetir no debía insertar")
	}
	if strings.Count(m.Buffer().String(), "import java.util.List;") != 1 {
		t.Error("import duplicado")
	}

	m, _ = m.Update(press('z', tea.ModCtrl))
	if got := m.Buffer().String(); got != src {
		t.Errorf("un deshacer debía revertirlo:\n%s", got)
	}
	m, _ = m.Update(press('y', tea.ModCtrl))
	if got := m.Buffer().String(); got != want {
		t.Errorf("rehacer:\n%s", got)
	}
}

func TestAddImportCSharp(t *testing.T) {
	m := withImports(t, "A.cs", "using System;\n\nnamespace App;\n\nclass A\n{\n    List\n}", 6)
	m, _ = m.Update(press(tea.KeyLeft, 0))
	if got := m.ImportCandidates("List"); len(got) != 1 || got[0] != "System.Collections.Generic" {
		t.Fatalf("candidatos: %v", got)
	}
	if m.ImportSuggestion() != "List" {
		t.Fatal("debía sugerir List")
	}
	m.AddImport("System.Collections.Generic")
	if got := m.Buffer().String(); !strings.HasPrefix(got, "using System;\nusing System.Collections.Generic;\n\nnamespace App;") {
		t.Errorf("%s", got)
	}
	if !m.HasImport("List") {
		t.Error("HasImport")
	}
}

func TestAddImportAtEndOfDocWithoutNewline(t *testing.T) {
	m := withImports(t, "A.java", "package a;", 0)
	m.AddImport("java.util.List")
	if got := m.Buffer().String(); got != "package a;\n\nimport java.util.List;" {
		t.Errorf("%q", got)
	}
	if c := m.Buffer().Cursor(); c.Line != 0 {
		t.Errorf("el cursor no debía moverse: %+v", c)
	}
}

func TestAddImportInsertionBelowCursorDoesNotMoveIt(t *testing.T) {
	m := withImports(t, "A.java", "package a;\n\nimport java.util.Map;\nimport org.junit.Test;\n\nclass A {}", 2)
	m.AddImport("org.slf4j.Logger")
	if c := m.Buffer().Cursor(); c.Line != 2 {
		t.Errorf("el import va tras el cursor: %+v", c)
	}
}

func TestImportsIgnoredForOtherLanguages(t *testing.T) {
	m := withImports(t, "notes.txt", "List", 0)
	if m.ImportSuggestion() != "" || len(m.ImportCandidates("List")) != 0 {
		t.Error("un .txt no tiene imports")
	}
}
