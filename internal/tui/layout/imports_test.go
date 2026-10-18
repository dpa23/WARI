package layout

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/assets"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/imports"
)

func importLayout(t *testing.T, name, content string) Model {
	t.Helper()
	root := t.TempDir()
	file := write(t, root, name, content)
	maps, err := imports.LoadDir(assets.FS, "imports")
	if err != nil {
		t.Fatal(err)
	}
	m := newLayout(t, Config{Root: root, File: file, Imports: maps}, 140, 30)
	if m.focus != focusEditor {
		t.Fatal("el foco debía estar en el editor")
	}
	return m
}

func TestImportListFromSelectorInsertsOnce(t *testing.T) {
	m := importLayout(t, "A.java", "package a;\n\nclass A {\n    \n}\n")
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	m = typeText(m, "List")

	if !strings.Contains(screen(m), "importar List") {
		t.Fatalf("la línea de estado debía sugerir el import:\n%s", screen(m))
	}
	if !strings.Contains(screen(m), "alt+i") {
		t.Errorf("debía mostrar la tecla de import_type")
	}

	m, _ = send(m, press('i', tea.ModAlt))
	if !m.pal.open || m.pal.mode != palImports {
		t.Fatalf("con varios candidatos debía abrir el selector: open=%v mode=%v", m.pal.open, m.pal.mode)
	}
	titles := []string{}
	for _, idx := range m.pal.results {
		titles = append(titles, m.pal.items[idx].title)
	}
	if len(titles) < 2 || titles[0] != "java.util.List" {
		t.Fatalf("candidatos: %v", titles)
	}
	m, _ = send(m, enter())

	if m.pal.open {
		t.Fatal("el selector debía cerrarse")
	}
	got := m.ed.Buffer().String()
	if want := "package a;\n\nimport java.util.List;\n\nclass A {\n    List\n}"; got != want {
		t.Fatalf("\n%s\nquiero:\n%s", got, want)
	}
	if !strings.Contains(m.msg, "java.util.List") {
		t.Errorf("msg = %q", m.msg)
	}
	if strings.Contains(screen(m), "importar List") {
		t.Error("la sugerencia debía desaparecer")
	}

	// Repetir no duplica.
	m, _ = send(m, press('i', tea.ModAlt))
	if m.pal.open || !strings.Contains(m.msg, "ya está importado") {
		t.Errorf("repetir: open=%v msg=%q", m.pal.open, m.msg)
	}
	if n := strings.Count(m.ed.Buffer().String(), "import java.util.List;"); n != 1 {
		t.Errorf("import repetido %d veces", n)
	}
}

func TestImportSelectorPicksOtherCandidate(t *testing.T) {
	m := importLayout(t, "A.java", "package a;\n\nclass A {\n    List\n}\n")
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	m, _ = send(m, press('i', tea.ModAlt), press(tea.KeyDown, 0), enter())
	if got := m.ed.Buffer().String(); !strings.Contains(got, "import java.awt.List;") || strings.Contains(got, "java.util.List") {
		t.Errorf("%s", got)
	}
}

func TestImportEscCancelsSelector(t *testing.T) {
	m := importLayout(t, "A.java", "package a;\n\nclass A {\n    List\n}\n")
	before := m.ed.Buffer().String()
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	m, _ = send(m, press('i', tea.ModAlt), press(tea.KeyEscape, 0))
	if m.pal.open || m.ed.Buffer().String() != before {
		t.Errorf("Esc no debía cambiar nada: open=%v", m.pal.open)
	}
}

func TestImportSingleCandidateCSharpInsertsDirectly(t *testing.T) {
	m := importLayout(t, "A.cs", "namespace App;\n\nclass A\n{\n    \n}\n")
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	m = typeText(m, "List")
	m, _ = send(m, press('i', tea.ModAlt))
	if m.pal.open {
		t.Fatal("con un solo candidato no hay selector")
	}
	got := m.ed.Buffer().String()
	if !strings.HasPrefix(got, "using System.Collections.Generic;\n\nnamespace App;") {
		t.Errorf("%s", got)
	}
	if !strings.Contains(m.msg, "using System.Collections.Generic") {
		t.Errorf("msg = %q", m.msg)
	}
	m, _ = send(m, press('i', tea.ModAlt))
	if strings.Count(m.ed.Buffer().String(), "using System.Collections.Generic;") != 1 {
		t.Error("using duplicado")
	}
}

func TestImportTypeMessages(t *testing.T) {
	m := importLayout(t, "A.java", "package a;\n\nclass A {}\n")
	m, _ = send(m, press('i', tea.ModAlt)) // el cursor está en la línea 1 vacía
	if m.pal.open || !strings.Contains(m.msg, "cursor sobre un tipo") {
		t.Errorf("msg = %q", m.msg)
	}
	m = importLayout(t, "A.java", "package a;\n\nclass Zzz {}\n")
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnd, 0), press(tea.KeyLeft, 0))
	m, _ = send(m, press('i', tea.ModAlt))
	if !strings.Contains(m.msg, "no hay import conocido") && !strings.Contains(m.msg, "cursor sobre un tipo") {
		t.Errorf("msg = %q", m.msg)
	}
	m = New(Config{})
	m, _ = m.update(press('i', tea.ModAlt))
	if m.msg != "no hay archivo abierto" {
		t.Errorf("sin archivo: %q", m.msg)
	}
	if keys := config.Default()["import_type"]; len(keys) == 0 {
		t.Error("falta la acción import_type")
	}
}

func TestImportFromPalette(t *testing.T) {
	m := importLayout(t, "A.java", "package a;\n\nclass A {\n    Optional\n}\n")
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	m, _ = send(m, press('p', tea.ModCtrl))
	m = typeText(m, "importar el tipo")
	m, _ = send(m, enter())
	if got := m.ed.Buffer().String(); !strings.Contains(got, "import java.util.Optional;") {
		t.Errorf("%s", got)
	}
}
