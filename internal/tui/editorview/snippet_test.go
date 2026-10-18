package editorview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/assets"
	"github.com/dpa23/wari/internal/editor"
	"github.com/dpa23/wari/internal/snippets"
)

func embeddedSnippets(t *testing.T) map[string]snippets.Set {
	t.Helper()
	sets, err := snippets.LoadDir(assets.FS, "snippets")
	if err != nil {
		t.Fatal(err)
	}
	return sets
}

var (
	tabKey   = press(tea.KeyTab, 0)
	shiftTab = press(tea.KeyTab, tea.ModShift)
	escKey   = press(tea.KeyEscape, 0)
)

// withSnippets abre name con content, el cursor al final de la línea line.
func withSnippets(t *testing.T, name, content string, line int) Model {
	t.Helper()
	m, _ := newFile(t, name, content, 60, 20)
	m.SetSnippets(embeddedSnippets(t))
	m.Buffer().SetCursorPos(editor.Pos{Line: line, Col: 1 << 20})
	return m
}

func typeAll(m Model, s string) Model {
	for _, r := range s {
		m, _ = m.Update(typ(string(r)))
	}
	return m
}

func TestSnippetCtorTabstopsUntilZero(t *testing.T) {
	m := withSnippets(t, "Foo.java", "public class Foo {\n    ctor\n}", 1)

	m, _ = m.Update(tabKey)
	want := "public class Foo {\n    public Foo() {\n        \n    }\n}"
	if got := m.Buffer().String(); got != want {
		t.Fatalf("expansión:\n%q\nquiero:\n%q", got, want)
	}
	if !m.SnippetActive() {
		t.Fatal("la sesión debía abrirse")
	}
	if got := m.Buffer().SelectedText(); got != "Foo" {
		t.Errorf("$1 seleccionado = %q, quiero Foo", got)
	}

	m, _ = m.Update(tabKey) // $2, entre los paréntesis
	if c := m.Buffer().Cursor(); c != (editor.Pos{Line: 1, Col: 15}) || m.Buffer().SelectedText() != "" {
		t.Errorf("$2: cursor %+v, sel %q", c, m.Buffer().SelectedText())
	}
	if !m.SnippetActive() {
		t.Fatal("la sesión debía seguir abierta en $2")
	}

	m, _ = m.Update(tabKey) // $0, en el cuerpo
	if c := m.Buffer().Cursor(); c != (editor.Pos{Line: 2, Col: 8}) {
		t.Errorf("$0: cursor %+v, quiero {2 8}", c)
	}
	if m.SnippetActive() {
		t.Error("llegar a $0 debía terminar la sesión")
	}

	// Tab ya no recorre paradas: inserta espacios.
	m, _ = m.Update(tabKey)
	if line := string(m.Buffer().Line(2)); line != strings.Repeat(" ", 12) {
		t.Errorf("Tab tras la sesión: %q", line)
	}
}

func TestSnippetExpansionIsOneUndoStep(t *testing.T) {
	m := withSnippets(t, "Foo.java", "class Foo {\n    ctor\n}", 1)
	before := m.Buffer().String()
	m, _ = m.Update(tabKey)
	if m.Buffer().String() == before {
		t.Fatal("no expandió")
	}
	if !m.Dirty() {
		t.Error("debía quedar sin guardar")
	}
	m, _ = m.Update(press('z', tea.ModCtrl))
	if got := m.Buffer().String(); got != before {
		t.Errorf("deshacer:\n%q\nquiero:\n%q", got, before)
	}
	if m.SnippetActive() {
		t.Error("deshacer debía terminar la sesión")
	}
	m, _ = m.Update(press('y', tea.ModCtrl))
	if m.Buffer().String() == before {
		t.Error("rehacer debía volver a expandir")
	}
}

func TestSnippetNoMatchInsertsSpaces(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"Foo.java", "xyz"},       // palabra sin snippet
		{"Foo.java", "ctorx"},     // prefijo parcial, no exacto
		{"notes.txt", "ctor"},     // lenguaje sin snippets
		{"Foo.java", "foo ctor("}, // cursor tras un paréntesis
	} {
		m, _ := newFile(t, tc.name, tc.text, 60, 10)
		m.SetSnippets(embeddedSnippets(t))
		m.Buffer().SetCursorPos(editor.Pos{Col: 1 << 20})
		m, _ = m.Update(tabKey)
		if got := m.Buffer().String(); got != tc.text+"    " {
			t.Errorf("%s %q: %q", tc.name, tc.text, got)
		}
		if m.SnippetActive() {
			t.Errorf("%q: no debía abrir sesión", tc.text)
		}
	}
}

func TestSnippetNotExpandedInsideWord(t *testing.T) {
	m, _ := newFile(t, "Foo.java", "ctorabc", 60, 10)
	m.SetSnippets(embeddedSnippets(t))
	m.Buffer().SetCursorPos(editor.Pos{Col: 4})
	m, _ = m.Update(tabKey)
	if got := m.Buffer().String(); got != "ctor    abc" {
		t.Errorf("%q", got)
	}
}

func TestSnippetLanguages(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"Prog.kt", "fun", "fun name(params): Unit {"},
		{"Prog.cs", "cw", "Console.WriteLine();"},
		{"Prog.java", "sout", "System.out.println();"},
	} {
		m, _ := newFile(t, tc.name, tc.text, 60, 10)
		m.SetSnippets(embeddedSnippets(t))
		m.Buffer().SetCursorPos(editor.Pos{Col: 1 << 20})
		m, _ = m.Update(tabKey)
		if got := string(m.Buffer().Line(0)); got != tc.want {
			t.Errorf("%s: %q, quiero %q", tc.name, got, tc.want)
		}
	}
}

func TestSnippetShiftTabAndEscape(t *testing.T) {
	m := withSnippets(t, "Foo.java", "foreach", 0)
	m, _ = m.Update(tabKey)
	if got := m.Buffer().SelectedText(); got != "Object" {
		t.Fatalf("$1 = %q", got)
	}
	m, _ = m.Update(tabKey)
	if got := m.Buffer().SelectedText(); got != "item" {
		t.Fatalf("$2 = %q", got)
	}
	m, _ = m.Update(shiftTab)
	if got := m.Buffer().SelectedText(); got != "Object" {
		t.Errorf("shift+tab: %q, quiero Object", got)
	}
	m, _ = m.Update(shiftTab) // en la primera no hace nada, pero la consume
	if got := m.Buffer().SelectedText(); got != "Object" || !m.SnippetActive() {
		t.Errorf("shift+tab en la primera: %q activa=%v", got, m.SnippetActive())
	}
	m, _ = m.Update(escKey)
	if m.SnippetActive() {
		t.Error("Esc debía terminar la sesión")
	}
	// Terminada, shift+tab no es del editor.
	if m.SnippetActive() {
		t.Error("sesión viva")
	}
}

func TestSnippetTypingAdjustsStopsAndMirrors(t *testing.T) {
	m := withSnippets(t, "Foo.java", "fori", 0)
	m, _ = m.Update(tabKey)
	if got := string(m.Buffer().Line(0)); got != "for (int i = 0; i < n; i++) {" {
		t.Fatalf("expansión: %q", got)
	}
	m = typeAll(m, "idx") // reemplaza "i" y se copia a los espejos
	if got := string(m.Buffer().Line(0)); got != "for (int idx = 0; idx < n; idx++) {" {
		t.Fatalf("espejos: %q", got)
	}
	if c := m.Buffer().Cursor(); c != (editor.Pos{Line: 0, Col: 12}) {
		t.Errorf("el cursor debía quedar tras lo escrito: %+v", c)
	}

	m, _ = m.Update(tabKey) // $2 = n, que se desplazó
	if got := m.Buffer().SelectedText(); got != "n" {
		t.Fatalf("$2 = %q", got)
	}
	m = typeAll(m, "size")
	m, _ = m.Update(shiftTab) // vuelve a $1, que ahora cubre "idx"
	if got := m.Buffer().SelectedText(); got != "idx" {
		t.Errorf("$1 tras volver = %q, quiero idx", got)
	}
	m, _ = m.Update(tabKey)
	if got := m.Buffer().SelectedText(); got != "size" {
		t.Errorf("$2 tras avanzar = %q, quiero size", got)
	}
	if got := string(m.Buffer().Line(0)); got != "for (int idx = 0; idx < size; idx++) {" {
		t.Errorf("línea final: %q", got)
	}
	m, _ = m.Update(tabKey) // $0
	if m.SnippetActive() {
		t.Error("$0 debía terminar la sesión")
	}
	if c := m.Buffer().Cursor(); c != (editor.Pos{Line: 1, Col: 4}) {
		t.Errorf("$0: %+v", c)
	}
}

func TestSnippetBackspaceInPlaceholder(t *testing.T) {
	m := withSnippets(t, "Foo.java", "foreach", 0)
	m, _ = m.Update(tabKey)
	m, _ = m.Update(press(tea.KeyBackspace, 0)) // borra el placeholder seleccionado
	if got := string(m.Buffer().Line(0)); got != "for ( item : collection) {" {
		t.Fatalf("%q", got)
	}
	m = typeAll(m, "Str")
	m, _ = m.Update(tabKey)
	if got := m.Buffer().SelectedText(); got != "item" {
		t.Errorf("$2 = %q", got)
	}
}

func TestSnippetEndsWhenCursorLeaves(t *testing.T) {
	m := withSnippets(t, "Foo.java", "sout\nfoo", 0)
	m, _ = m.Update(tabKey)
	if !m.SnippetActive() {
		t.Fatal("sin sesión")
	}
	m, _ = m.Update(press(tea.KeyDown, 0))
	if m.SnippetActive() {
		t.Error("salir del texto expandido debía terminar la sesión")
	}
}

func TestSnippetOnlyZeroHasNoSession(t *testing.T) {
	m, _ := newFile(t, "Foo.java", "x ", 60, 10)
	sets := map[string]snippets.Set{"java": {Snippets: []snippets.Snippet{{Name: "z", Prefixes: []string{"z"}, Body: []string{"zero$0"}}}}}
	m.SetSnippets(sets)
	m.Buffer().SetCursorPos(editor.Pos{Col: 2})
	m, _ = m.Update(typ("z"))
	m, _ = m.Update(tabKey)
	if got := m.Buffer().String(); got != "x zero" || m.SnippetActive() {
		t.Errorf("%q activa=%v", got, m.SnippetActive())
	}
}

func TestSnippetDollarInTextIsLiteral(t *testing.T) {
	m, _ := newFile(t, "Foo.java", "d", 60, 10)
	sets := map[string]snippets.Set{"java": {Snippets: []snippets.Snippet{{Name: "d", Prefixes: []string{"d"}, Body: []string{`cost \$5 $0`}}}}}
	m.SetSnippets(sets)
	m.Buffer().SetCursorPos(editor.Pos{Col: 1})
	m, _ = m.Update(tabKey)
	if got := m.Buffer().String(); got != "cost $5 " {
		t.Errorf("%q", got)
	}
}

func TestSnippetTypingWithMirrorsIsOneUndoStep(t *testing.T) {
	m := withSnippets(t, "Foo.java", "fori", 0)
	m, _ = m.Update(tabKey)
	m = typeAll(m, "j")
	if got := string(m.Buffer().Line(0)); got != "for (int j = 0; j < n; j++) {" {
		t.Fatalf("%q", got)
	}
	m, _ = m.Update(press('z', tea.ModCtrl))
	if got := string(m.Buffer().Line(0)); got != "for (int i = 0; i < n; i++) {" {
		t.Errorf("un solo deshacer debía revertir la tecla y sus espejos: %q", got)
	}
}

func TestSnippetEmptiedStopKeepsSessionAndMirrors(t *testing.T) {
	m := withSnippets(t, "Foo.java", "fori", 0)
	m, _ = m.Update(tabKey)
	m, _ = m.Update(press(tea.KeyBackspace, 0)) // vacía $1 y sus espejos
	if got := string(m.Buffer().Line(0)); got != "for (int  = 0;  < n; ++) {" {
		t.Fatalf("%q", got)
	}
	if !m.SnippetActive() {
		t.Fatal("la sesión debía seguir")
	}
	m = typeAll(m, "kk")
	if got := string(m.Buffer().Line(0)); got != "for (int kk = 0; kk < n; kk++) {" {
		t.Fatalf("%q", got)
	}
	if !m.SnippetActive() {
		t.Fatal("la sesión debía seguir tras rellenar espejos vacíos")
	}
	m, _ = m.Update(tabKey)
	if got := m.Buffer().SelectedText(); got != "n" {
		t.Errorf("$2 = %q", got)
	}
}
