package editorview

import (
	"strings"
	"testing"
)

func TestWordAtCursorAndPrefix(t *testing.T) {
	m, _ := newFile(t, "A.java", "class Año { int contador_1; }\n", 60, 5)
	m.Focus()
	m.GoTo(0, 7) // tras la A de "Año"
	if w, line, col := m.WordAtCursor(); w != "Año" || line != 0 || col != 6 {
		t.Errorf("WordAtCursor = %q %d:%d", w, line, col)
	}
	if p := m.WordPrefix(); p != "A" {
		t.Errorf("WordPrefix = %q (el cursor está tras la A)", p)
	}
	m.GoTo(0, 9) // justo después de "Año"
	if w, _, _ := m.WordAtCursor(); w != "Año" {
		t.Errorf("justo tras la palabra aún la toca: %q", w)
	}
	m.GoTo(0, 10) // entre un espacio y "{"
	if w, _, _ := m.WordAtCursor(); w != "" {
		t.Errorf("sobre un signo: %q", w)
	}
	if m.WordPrefix() != "" {
		t.Error("sin prefijo")
	}
	m.GoTo(0, 17)
	if w, _, col := m.WordAtCursor(); w != "contador_1" || col != 16 {
		t.Errorf("contador_1: %q col %d", w, col)
	}
}

func TestReplacePrefixIsOneUndoStep(t *testing.T) {
	m, _ := newFile(t, "A.java", "class A {\n    Prod\n}\n", 60, 8)
	m.Focus()
	m.GoTo(1, 8)
	if !m.ReplacePrefix("Producto") {
		t.Fatal("debía reemplazar")
	}
	if got := string(m.Buffer().Line(1)); got != "    Producto" {
		t.Errorf("línea = %q", got)
	}
	if c := m.Buffer().Cursor(); c.Line != 1 || c.Col != 12 {
		t.Errorf("cursor = %+v", c)
	}
	if !m.Dirty() {
		t.Error("el buffer debía quedar sucio")
	}
	m, _ = send(m, press('z', 4)) // ctrl+z
	if got := string(m.Buffer().Line(1)); got != "    Prod" {
		t.Errorf("tras deshacer = %q", got)
	}
	// Sin prefijo no hace nada.
	m.GoTo(0, 0)
	if m.ReplacePrefix("X") {
		t.Error("sin prefijo no debía reemplazar")
	}
}

func TestGoToCentersAndClamps(t *testing.T) {
	m, _ := newFile(t, "A.java", numbered(100), 40, 10)
	m.Focus()
	m.GoTo(50, 0)
	if c := m.Buffer().Cursor(); c.Line != 50 {
		t.Fatalf("cursor = %+v", c)
	}
	top, _ := m.scroll()
	if top != 45 {
		t.Errorf("la línea debía quedar centrada: top = %d", top)
	}
	if v := m.View(); !strings.Contains(plain(v), "51") {
		t.Errorf("la línea 51 debía verse:\n%s", plain(v))
	}
	m.GoTo(999, 999)
	if c := m.Buffer().Cursor(); c.Line != 99 {
		t.Errorf("fuera de rango: %+v", c)
	}
	m.GoTo(-5, -5)
	if c := m.Buffer().Cursor(); c.Line != 0 || c.Col != 0 {
		t.Errorf("negativo: %+v", c)
	}
	if m.Text() != m.Buffer().String() {
		t.Error("Text debía ser el contenido del buffer")
	}
}
