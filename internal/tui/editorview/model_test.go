package editorview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/editor"
)

// --- Ayudantes --------------------------------------------------------------

func press(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func typ(s string) tea.KeyPressMsg {
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// send aplica msgs en orden y devuelve el modelo y el último cmd no nulo.
func send(m Model, msgs ...tea.Msg) (Model, tea.Cmd) {
	var last tea.Cmd
	for _, msg := range msgs {
		var c tea.Cmd
		m, c = m.Update(msg)
		if c != nil {
			last = c
		}
	}
	return m, last
}

// newFile escribe content en un archivo temporal y abre un editor sobre él.
func newFile(t *testing.T, name, content string, w, h int) (Model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	buf, fi, err := editor.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := New(buf, fi, path, DefaultKeyMap())
	m.SetSize(w, h)
	m.Focus()
	return m, path
}

// plain quita las secuencias de escape de s.
func plain(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < '@' || s[i] > '~') {
				i++
			}
			continue
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

func numbered(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		sb.WriteString("linea ")
		sb.WriteString(strings.Repeat("x", i%7))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// --- Render ----------------------------------------------------------------

func TestRenderGutterAndWidth(t *testing.T) {
	m, _ := newFile(t, "A.java", "public class A {\n    int x = 1; // comentario muy largo que no cabe en el ancho\n}\n", 30, 5)
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 5 {
		t.Fatalf("filas = %d, quería 5", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 30 {
			t.Errorf("fila %d: ancho %d, quería 30: %q", i, w, plain(l))
		}
	}
	p := plain(lines[0])
	if !strings.HasPrefix(p, "1  public class A {") {
		t.Errorf("fila 0 sin número de línea: %q", p)
	}
	if !strings.HasPrefix(plain(lines[2]), "3  }") {
		t.Errorf("fila 2: %q", plain(lines[2]))
	}
	if strings.TrimSpace(plain(lines[3])) != "" {
		t.Errorf("fila más allá del final no vacía: %q", plain(lines[3]))
	}
	if !strings.Contains(view, "\x1b[38;2;") {
		t.Error("el código Java no tiene color")
	}
}

func TestRenderPlainTextAndUnicode(t *testing.T) {
	m, _ := newFile(t, "notas.txt", "año canción 日本語 emoji\n\tcon tab\nx\x01y\n", 25, 4)
	if m.hl.lexer != nil {
		t.Fatal(".txt no debería tener lexer")
	}
	for i, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w != 25 {
			t.Errorf("fila %d: ancho %d: %q", i, w, plain(l))
		}
	}
	if p := plain(strings.Split(m.View(), "\n")[1]); !strings.HasPrefix(p, "2      con tab") {
		t.Errorf("tab mal expandido: %q", p)
	}
}

func TestRenderKotlinAndCSharp(t *testing.T) {
	for name, src := range map[string]string{
		"A.kt": "fun main() { println(\"hola\") }\n",
		"A.cs": "class A { static void Main() { var s = \"hola\"; } }\n",
	} {
		m, _ := newFile(t, name, src, 60, 2)
		if m.hl.lexer == nil {
			t.Errorf("%s: sin lexer", name)
		}
		if !strings.Contains(m.View(), "\x1b[38;2;") {
			t.Errorf("%s: sin color", name)
		}
	}
}

func TestRenderNeverExceedsWidth(t *testing.T) {
	src := "público static void método(String áéíóú, int 日本語) { // 日本語日本語日本語日本語日本語日本語\n}\n"
	for w := 1; w <= 40; w++ {
		m, _ := newFile(t, "A.java", src, w, 3)
		m, _ = send(m, press(tea.KeyEnd, 0))
		for i, l := range strings.Split(m.View(), "\n") {
			if got := lipgloss.Width(l); got != w {
				t.Fatalf("w=%d fila %d: ancho %d: %q", w, i, got, plain(l))
			}
		}
	}
}

func TestEmptySizeAndEmptyBuffer(t *testing.T) {
	m, _ := newFile(t, "vacio.java", "", 20, 3)
	if got := m.View(); strings.Count(got, "\n") != 2 {
		t.Errorf("buffer vacío: %q", plain(got))
	}
	m.SetSize(0, 0)
	if m.View() != "" || m.Cursor() != nil {
		t.Error("tamaño 0 debe dar vista vacía y sin cursor")
	}
}

// --- Cursor y scroll -------------------------------------------------------

func TestCursorMovesAndScrollFollows(t *testing.T) {
	m, _ := newFile(t, "n.txt", numbered(100), 40, 10)
	if c := m.Cursor(); c == nil || c.X != 5 || c.Y != 0 {
		// gutter = 3 dígitos + 2 = 5
		t.Fatalf("cursor inicial = %+v", c)
	}
	for range 25 {
		m, _ = send(m, press(tea.KeyDown, 0))
	}
	if got := m.buf.Cursor().Line; got != 25 {
		t.Fatalf("línea = %d", got)
	}
	if m.top != 16 {
		t.Errorf("top = %d, quería 16 (cursor en la última fila)", m.top)
	}
	if c := m.Cursor(); c == nil || c.Y != 9 {
		t.Errorf("cursor = %+v, quería Y=9", c)
	}
	first := plain(strings.Split(m.View(), "\n")[0])
	if !strings.Contains(first, " 17 ") {
		t.Errorf("primera fila = %q, quería línea 17", first)
	}
	for range 25 {
		m, _ = send(m, press(tea.KeyUp, 0))
	}
	if m.top != 0 {
		t.Errorf("top = %d tras subir", m.top)
	}
}

func TestPageUpDownAndDocKeys(t *testing.T) {
	m, _ := newFile(t, "n.txt", numbered(100), 40, 10)
	m, _ = send(m, press(tea.KeyPgDown, 0))
	if l := m.buf.Cursor().Line; l != 9 {
		t.Errorf("pgdown: línea %d, quería 9", l)
	}
	m, _ = send(m, press(tea.KeyPgUp, 0))
	if l := m.buf.Cursor().Line; l != 0 {
		t.Errorf("pgup: línea %d", l)
	}
	m, _ = send(m, press(tea.KeyEnd, tea.ModCtrl))
	if l := m.buf.Cursor().Line; l != 99 {
		t.Errorf("ctrl+end: línea %d", l)
	}
	if c := m.Cursor(); c == nil || c.Y != 9 {
		t.Errorf("ctrl+end: cursor %+v", c)
	}
	m, _ = send(m, press(tea.KeyHome, tea.ModCtrl))
	if p := m.buf.Cursor(); p != (editor.Pos{}) || m.top != 0 {
		t.Errorf("ctrl+home: %+v top=%d", p, m.top)
	}
}

func TestHorizontalScroll(t *testing.T) {
	long := strings.Repeat("0123456789", 10)
	m, _ := newFile(t, "l.txt", long+"\ncorta\n", 20, 3)
	m, _ = send(m, press(tea.KeyEnd, 0))
	if m.xoff == 0 {
		t.Fatal("no hubo scroll horizontal")
	}
	c := m.Cursor()
	if c == nil || c.X >= 20 || c.X < 5 {
		t.Fatalf("cursor fuera: %+v", c)
	}
	row := plain(strings.Split(m.View(), "\n")[0])
	if !strings.HasSuffix(strings.TrimRight(row, " "), "789") {
		t.Errorf("fila = %q", row)
	}
	m, _ = send(m, press(tea.KeyHome, 0))
	if m.xoff != 0 {
		t.Errorf("xoff = %d tras home", m.xoff)
	}
}

func TestWordMoveAndHomeEnd(t *testing.T) {
	m, _ := newFile(t, "w.txt", "uno dos tres\n", 40, 3)
	m, _ = send(m, press(tea.KeyRight, tea.ModCtrl))
	if c := m.buf.Cursor().Col; c != 4 {
		t.Errorf("ctrl+right: col %d", c)
	}
	m, _ = send(m, press(tea.KeyEnd, 0))
	if c := m.buf.Cursor().Col; c != 12 {
		t.Errorf("end: col %d", c)
	}
	m, _ = send(m, press(tea.KeyLeft, tea.ModCtrl))
	if c := m.buf.Cursor().Col; c != 8 {
		t.Errorf("ctrl+left: col %d", c)
	}
	m, _ = send(m, press(tea.KeyHome, 0))
	if c := m.buf.Cursor().Col; c != 0 {
		t.Errorf("home: col %d", c)
	}
}

// --- Edición, guardado y deshacer ------------------------------------------

func TestInsertAndSave(t *testing.T) {
	m, path := newFile(t, "e.txt", "hola\n", 40, 3)
	if m.Dirty() {
		t.Fatal("recién abierto no debe estar sucio")
	}
	m, _ = send(m, press(tea.KeyEnd, 0), typ("!"), typ(" "), typ("ñ"))
	if !m.Dirty() {
		t.Fatal("tras escribir debe estar sucio")
	}
	if got := m.buf.String(); got != "hola! ñ" {
		t.Fatalf("buffer = %q", got)
	}
	m, cmd := send(m, press('s', tea.ModCtrl))
	if m.Dirty() {
		t.Error("tras guardar debe estar limpio")
	}
	msg, ok := cmd().(SavedMsg)
	if !ok || msg.Err != nil || msg.Path != path {
		t.Fatalf("SavedMsg = %+v", msg)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hola! ñ\n" {
		t.Fatalf("archivo = %q err=%v", data, err)
	}
}

func TestSaveErrorKeepsDirty(t *testing.T) {
	m, path := newFile(t, "e.txt", "x\n", 40, 3)
	m, _ = send(m, typ("a"))
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	m, cmd := send(m, press('s', tea.ModCtrl))
	if msg := cmd().(SavedMsg); msg.Err == nil {
		t.Error("quería error al guardar en un directorio borrado")
	}
	if !m.Dirty() {
		t.Error("tras un guardado fallido debe seguir sucio")
	}
}

func TestEditKeys(t *testing.T) {
	m, _ := newFile(t, "e.txt", "ab\ncd\n", 40, 3)
	m, _ = send(m, press(tea.KeyEnd, 0), press(tea.KeyEnter, 0), typ("x"))
	if got := m.buf.String(); got != "ab\nx\ncd" {
		t.Fatalf("enter: %q", got)
	}
	m, _ = send(m, press(tea.KeyBackspace, 0), press(tea.KeyBackspace, 0))
	if got := m.buf.String(); got != "ab\ncd" {
		t.Fatalf("backspace: %q", got)
	}
	m, _ = send(m, press(tea.KeyHome, tea.ModCtrl), press(tea.KeyDelete, 0))
	if got := m.buf.String(); got != "b\ncd" {
		t.Fatalf("delete: %q", got)
	}
	m, _ = send(m, press(tea.KeyTab, 0))
	if got := m.buf.String(); got != "    b\ncd" {
		t.Fatalf("tab: %q", got)
	}
	m, _ = send(m, tea.PasteMsg{Content: "P\nQ"})
	if got := m.buf.String(); got != "    P\nQb\ncd" {
		t.Fatalf("paste: %q", got)
	}
}

func TestAutoIndentOnEnter(t *testing.T) {
	m, _ := newFile(t, "A.java", "    int x;\n", 40, 3)
	m, _ = send(m, press(tea.KeyEnd, 0), press(tea.KeyEnter, 0), typ("y"))
	if got := m.buf.String(); got != "    int x;\n    y" {
		t.Fatalf("buffer = %q", got)
	}
}

func TestNoOpEditsStayClean(t *testing.T) {
	m, _ := newFile(t, "e.txt", "a\n", 40, 3)
	m, _ = send(m, press(tea.KeyBackspace, 0), press(tea.KeyEnd, tea.ModCtrl), press(tea.KeyDelete, 0),
		press('x', tea.ModCtrl), press(tea.KeyLeft, tea.ModAlt|tea.ModShift))
	if m.Dirty() {
		t.Error("las teclas sin efecto no deben ensuciar")
	}
}

func TestUndoRedoKeys(t *testing.T) {
	m, _ := newFile(t, "u.txt", "base\n", 40, 3)
	m.buf.SetUndoGroupPause(0) // cada tecla es un grupo
	m, _ = send(m, press(tea.KeyEnd, 0), typ("1"), typ("2"))
	m, _ = send(m, press('z', tea.ModCtrl))
	if got := m.buf.String(); got != "base1" {
		t.Fatalf("undo: %q", got)
	}
	m, _ = send(m, press('z', tea.ModCtrl))
	if got := m.buf.String(); got != "base" {
		t.Fatalf("undo 2: %q", got)
	}
	if m.Dirty() {
		t.Error("deshacer hasta el estado guardado debe dejarlo limpio")
	}
	m, _ = send(m, press('y', tea.ModCtrl), press('y', tea.ModCtrl))
	if got := m.buf.String(); got != "base12" {
		t.Fatalf("redo: %q", got)
	}
	if !m.Dirty() {
		t.Error("tras rehacer debe estar sucio")
	}
}

// --- Selección --------------------------------------------------------------

func TestShiftSelection(t *testing.T) {
	m, _ := newFile(t, "s.txt", "hola mundo\notra\n", 40, 3)
	m, _ = send(m, press(tea.KeyRight, tea.ModShift), press(tea.KeyRight, tea.ModShift), press(tea.KeyRight, tea.ModShift))
	if got := m.buf.SelectedText(); got != "hol" {
		t.Fatalf("selección = %q", got)
	}
	if !strings.Contains(m.View(), sgrSelect) {
		t.Error("la selección no se ve con fondo distinto")
	}
	m, _ = send(m, press(tea.KeyRight, 0))
	if got := m.buf.SelectedText(); got != "" {
		t.Errorf("tras mover sin shift queda selección %q", got)
	}
	if strings.Contains(m.View(), sgrSelect) {
		t.Error("sin selección no debe haber fondo")
	}
	// Selección multilínea con shift+down y palabra con ctrl+shift.
	m, _ = send(m, press(tea.KeyHome, tea.ModCtrl), press(tea.KeyRight, tea.ModCtrl|tea.ModShift))
	if got := m.buf.SelectedText(); got != "hola " {
		t.Errorf("palabra = %q", got)
	}
	m, _ = send(m, press(tea.KeyDown, tea.ModShift))
	if got := m.buf.SelectedText(); got != "hola mundo\notra" {
		// El cursor queda en col 5 de la línea 1 ("otra" tiene 4: se recorta).
		t.Errorf("multilínea = %q", got)
	}
	for i, l := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(l); w != 40 {
			t.Errorf("fila %d con selección: ancho %d", i, w)
		}
	}
	// Escribir reemplaza la selección.
	m, _ = send(m, typ("Z"))
	if got := m.buf.String(); got != "Z" {
		t.Errorf("reemplazo = %q", got)
	}
}

func TestSelectAllKey(t *testing.T) {
	m, _ := newFile(t, "s.txt", "a\nb\n", 40, 3)
	m, _ = send(m, press('a', tea.ModCtrl))
	if got := m.buf.SelectedText(); got != "a\nb" {
		t.Errorf("selección = %q", got)
	}
}

// --- Foco ------------------------------------------------------------------

func TestUnfocusedDoesNothing(t *testing.T) {
	m, _ := newFile(t, "f.txt", "abc\n", 40, 3)
	m.Blur()
	if m.Focused() || m.Cursor() != nil {
		t.Fatal("sin foco no debe haber cursor")
	}
	m, cmd := send(m, typ("x"), press(tea.KeyRight, 0), press(tea.KeyDelete, 0), tea.PasteMsg{Content: "zzz"}, press('s', tea.ModCtrl))
	if cmd != nil || m.Dirty() || m.buf.String() != "abc" || m.buf.Cursor() != (editor.Pos{}) {
		t.Errorf("sin foco actuó: %q %+v dirty=%v", m.buf.String(), m.buf.Cursor(), m.Dirty())
	}
	m.Focus()
	if m.Cursor() == nil {
		t.Error("con foco debe haber cursor")
	}
}

// --- Rendimiento ------------------------------------------------------------

func TestViewPerformanceGrande(t *testing.T) {
	buf, fi, err := editor.Open("../../../testdata/java/Grande.java")
	if err != nil {
		t.Fatal(err)
	}
	if buf.LineCount() < 5000 {
		t.Fatalf("Grande.java tiene %d líneas", buf.LineCount())
	}
	m := New(buf, fi, fi.Path, DefaultKeyMap())
	m.SetSize(120, 40)
	m.Focus()

	m.View() // calienta: la primera tokenización compila las reglas del lexer

	var worst, total time.Duration
	const n = 50
	for i := range n {
		// Posiciones repartidas por todo el archivo, incluida la última.
		m.buf.SetCursorPos(editor.Pos{Line: (i * (buf.LineCount() - 1)) / (n - 1), Col: 5})
		m.top, m.xoff = m.scroll()
		start := time.Now()
		v := m.View()
		d := time.Since(start)
		total += d
		worst = max(worst, d)
		if strings.Count(v, "\n") != 39 {
			t.Fatalf("filas = %d", strings.Count(v, "\n")+1)
		}
	}
	t.Logf("View() con Grande.java (%d líneas, 120x40): media %v, peor %v", buf.LineCount(), total/n, worst)
	limit := 10 * time.Millisecond
	if raceEnabled {
		limit *= 20 // el detector de carreras ralentiza esta ruta ~20x
	}
	if total/n >= limit {
		t.Errorf("media %v >= %v", total/n, limit)
	}
}

func TestMultilineCommentWithinLookback(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("/**\n")
	for range 10 {
		sb.WriteString(" * texto de comentario\n")
	}
	sb.WriteString(" */\nclass A {}\n")
	m, _ := newFile(t, "A.java", sb.String(), 40, 4)
	m.top = 5 // la ventana empieza dentro del comentario
	view := plain(m.View())
	if !strings.Contains(view, "texto de comentario") {
		t.Fatalf("vista inesperada: %q", view)
	}
	// Todas las líneas de la ventana (dentro del comentario) deben usar el
	// mismo color: el del comentario.
	rows := strings.Split(m.View(), "\n")
	want := colorOf(rows[0])
	for i, r := range rows[:3] {
		if colorOf(r) != want {
			t.Errorf("fila %d con color distinto del comentario", i)
		}
	}
}

// colorOf devuelve el primer SGR de color de primer plano de la parte de texto
// de una fila (después del gutter).
func colorOf(row string) string {
	i := strings.Index(row, sgrReset)
	if i < 0 {
		return ""
	}
	rest := row[i+len(sgrReset):]
	j := strings.IndexByte(rest, 'm')
	if j < 0 {
		return ""
	}
	return rest[:j+1]
}
