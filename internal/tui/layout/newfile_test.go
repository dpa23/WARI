package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/assets"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/snippets"
	"github.com/dpa23/wari/internal/templates"
)

func embeddedAssets(t *testing.T) (map[string]snippets.Set, []templates.Template) {
	t.Helper()
	sets, err := snippets.LoadDir(assets.FS, "snippets")
	if err != nil {
		t.Fatal(err)
	}
	tpls, err := templates.Load(assets.FS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	return sets, tpls
}

func enter() tea.KeyPressMsg { return press(tea.KeyEnter, 0) }

// javaProject es un proyecto Maven con un archivo en com/empresa/api, abierto.
func javaProject(t *testing.T) (Model, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "src/main/java/com/empresa/api/Existing.java", "package com.empresa.api;\n\nclass Existing {}\n")
	sets, tpls := embeddedAssets(t)
	m := newLayout(t, Config{
		Root: root, File: filepath.Join(root, "src/main/java/com/empresa/api/Existing.java"),
		Snippets: sets, Templates: tpls,
	}, 140, 30)
	return m, root
}

func TestNewFileFromPaletteCreatesControllerWithPackage(t *testing.T) {
	m, root := javaProject(t)

	m, _ = send(m, press('p', tea.ModCtrl))
	m = typeText(m, "nuevo archivo")
	m, _ = send(m, enter())
	if !m.pal.open || m.pal.mode != palTemplates {
		t.Fatalf("debía abrir el selector de plantillas: open=%v mode=%v", m.pal.open, m.pal.mode)
	}
	m = typeText(m, "java controller")
	if len(m.pal.results) == 0 {
		t.Fatal("el filtro por lenguaje y nombre no encontró nada")
	}
	m, _ = send(m, enter())
	if m.pal.mode != palPath {
		t.Fatalf("modo = %v, quiero palPath", m.pal.mode)
	}
	if got, want := string(m.pal.query), "src/main/java/com/empresa/api/"; got != want {
		t.Fatalf("ruta prellenada = %q, quiero %q", got, want)
	}
	m = typeText(m, "UserController")
	m, _ = send(m, enter())

	if m.pal.open {
		t.Fatalf("la paleta debía cerrarse (nota: %q)", m.pal.note)
	}
	path := filepath.Join(root, "src/main/java/com/empresa/api/UserController.java")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"package com.empresa.api;", "public class UserController", "@RestController"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}
	if strings.Contains(got, "{{") {
		t.Errorf("quedaron variables sin sustituir:\n%s", got)
	}
	if !m.hasEd || m.ed.Path() != path || m.focus != focusEditor {
		t.Errorf("debía abrirse en el editor: hasEd=%v path=%q focus=%v", m.hasEd, m.ed.Path(), m.focus)
	}
	if !strings.Contains(m.msg, "creado") {
		t.Errorf("msg = %q", m.msg)
	}
	if !strings.Contains(screen(m), "UserController.java") {
		t.Errorf("el árbol debía mostrar el archivo nuevo:\n%s", screen(m))
	}
}

func TestNewFileKeyOpensPickerAndEscCancels(t *testing.T) {
	m, _ := javaProject(t)
	if keys := config.Default()["new_file"]; len(keys) == 0 {
		t.Fatal("falta la acción new_file")
	}
	m, _ = send(m, press('f', tea.ModAlt))
	if !m.pal.open || m.pal.mode != palTemplates {
		t.Fatalf("alt+f debía abrir el selector: open=%v mode=%v", m.pal.open, m.pal.mode)
	}
	m, _ = send(m, enter())
	m, _ = send(m, press(tea.KeyEscape, 0))
	if m.pal.open {
		t.Error("Esc debía cerrar la paleta")
	}
	// Con la paleta cerrada, ctrl+p vuelve a ser la de comandos.
	m, _ = send(m, press('p', tea.ModCtrl))
	if m.pal.mode != palCommands {
		t.Errorf("modo = %v, quiero palCommands", m.pal.mode)
	}
}

func TestNewFileNeverOverwrites(t *testing.T) {
	m, root := javaProject(t)
	existing := filepath.Join(root, "src/main/java/com/empresa/api/Existing.java")
	before, _ := os.ReadFile(existing)

	m, _ = send(m, press('f', tea.ModAlt))
	m = typeText(m, "java clase")
	m, _ = send(m, enter())
	m = typeText(m, "Existing")
	m, _ = send(m, enter())

	if !m.pal.open || m.pal.mode != palPath || !strings.Contains(m.pal.note, "ya existe") {
		t.Fatalf("debía avisar y seguir en la ruta: open=%v mode=%v nota=%q", m.pal.open, m.pal.mode, m.pal.note)
	}
	after, _ := os.ReadFile(existing)
	if string(before) != string(after) {
		t.Errorf("se sobrescribió el archivo existente:\n%s", after)
	}
}

func TestNewFileRejectsBadPaths(t *testing.T) {
	m, root := javaProject(t)
	for _, tc := range []struct{ typed, want string }{
		{"../fuera/Foo", "dentro de la raíz"},
		{"-x", "nombre de clase"}, // tras la carpeta prellenada queda "…/api/-x"
	} {
		mm, _ := send(m, press('f', tea.ModAlt))
		mm = typeText(mm, "java clase")
		mm, _ = send(mm, enter())
		if strings.HasPrefix(tc.typed, "../") {
			mm.pal.query = nil
		}
		mm = typeText(mm, tc.typed)
		mm, _ = send(mm, enter())
		if !mm.pal.open || !strings.Contains(mm.pal.note, tc.want) {
			t.Errorf("%q: open=%v nota=%q, quiero %q", tc.typed, mm.pal.open, mm.pal.note, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "fuera")); err == nil {
		t.Error("se creó algo fuera de la raíz")
	}
}

func TestNewFileOutsideSourceRootShowsErrorAndKeepsPath(t *testing.T) {
	m, root := javaProject(t)
	m, _ = send(m, press('f', tea.ModAlt))
	m = typeText(m, "java clase")
	m, _ = send(m, enter())
	m.pal.query = []rune("lib/Foo")
	m, _ = send(m, enter())
	if !m.pal.open || m.pal.mode != palPath || !strings.Contains(m.pal.note, "src/main/java") {
		t.Fatalf("open=%v mode=%v nota=%q", m.pal.open, m.pal.mode, m.pal.note)
	}
	if string(m.pal.query) != "lib/Foo" {
		t.Errorf("la ruta escrita debía conservarse: %q", m.pal.query)
	}
	if _, err := os.Stat(filepath.Join(root, "lib")); err == nil {
		t.Error("no debía crear carpetas si la plantilla no se pudo renderizar")
	}
}

func TestNewFileAsksTemplateVariables(t *testing.T) {
	root := t.TempDir()
	tpl, err := templates.Parse([]byte("---\nname: Ruta\nlang: java\next: txt\nvars: [basePath]\n---\nruta {{basePath}} de {{ClassName}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newLayout(t, Config{Root: root, Templates: []templates.Template{tpl}}, 120, 30)
	m, _ = send(m, press('f', tea.ModAlt))
	m, _ = send(m, enter())
	m = typeText(m, "datos")
	m, _ = send(m, enter())
	if m.pal.mode != palVar || !strings.HasPrefix(m.pal.prompt, "basePath") {
		t.Fatalf("debía pedir basePath: mode=%v prompt=%q", m.pal.mode, m.pal.prompt)
	}
	m = typeText(m, "/api")
	m, _ = send(m, enter())
	data, err := os.ReadFile(filepath.Join(root, "datos.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ruta /api de datos\n" {
		t.Errorf("contenido = %q", data)
	}
}

func TestNewFileDefaultPackageDropsPackageLine(t *testing.T) {
	m, root := javaProject(t)
	m, _ = send(m, press('f', tea.ModAlt))
	m = typeText(m, "java clase")
	m, _ = send(m, enter())
	m.pal.query = []rune("src/main/java/Top")
	m, _ = send(m, enter())
	data, err := os.ReadFile(filepath.Join(root, "src/main/java/Top.java"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "package") || !strings.Contains(string(data), "class Top") {
		t.Errorf("contenido:\n%s", data)
	}
}

func TestNewFileWithoutTemplatesWarns(t *testing.T) {
	m := newLayout(t, Config{}, 100, 24)
	m, _ = send(m, press('f', tea.ModAlt))
	if m.pal.open || m.msg == "" {
		t.Errorf("open=%v msg=%q", m.pal.open, m.msg)
	}
}

func TestNewFileSymlinkIntermediateDirRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Parse([]byte("---\nname: Simple\nlang: text\next: txt\n---\ncontenido\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newLayout(t, Config{Root: root, Templates: []templates.Template{tpl}}, 120, 30)
	m, _ = send(m, press('f', tea.ModAlt))
	m, _ = send(m, enter())
	m.pal.query = []rune("link/Escaped.txt")
	m, _ = send(m, enter())

	if _, err := os.Stat(filepath.Join(outside, "Escaped.txt")); err == nil {
		t.Fatal("vulnerabilidad: se creó el archivo en el directorio externo apuntado por symlink")
	}
	if !m.pal.open || m.pal.mode != palPath {
		t.Fatalf("debía seguir en el paso de ruta con error: open=%v mode=%v nota=%q", m.pal.open, m.pal.mode, m.pal.note)
	}
}

func TestSnippetTabKeepsFocusInEditor(t *testing.T) {
	root := t.TempDir()
	file := write(t, root, "Foo.java", "foreach\n")
	sets, _ := embeddedAssets(t)
	m := newLayout(t, Config{Root: root, File: file, Snippets: sets}, 120, 30)
	if m.focus != focusEditor {
		t.Fatal("el foco debía estar en el editor")
	}
	m, _ = send(m, press(tea.KeyEnd, 0), press(tea.KeyTab, 0))
	if !m.ed.SnippetActive() || m.ed.Buffer().SelectedText() != "Object" {
		t.Fatalf("sesión=%v sel=%q", m.ed.SnippetActive(), m.ed.Buffer().SelectedText())
	}
	m, _ = send(m, press(tea.KeyTab, 0))
	m, _ = send(m, press(tea.KeyTab, tea.ModShift)) // atrás, sin mover el foco
	if m.focus != focusEditor || m.ed.Buffer().SelectedText() != "Object" {
		t.Errorf("foco=%v sel=%q", m.focus, m.ed.Buffer().SelectedText())
	}
	m, _ = send(m, press(tea.KeyEscape, 0), press(tea.KeyTab, tea.ModShift))
	if m.focus == focusEditor {
		t.Error("terminada la sesión, shift+tab debía volver a mover el foco")
	}
}
