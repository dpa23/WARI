package layout

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/tui"
	"github.com/dpa23/wari/internal/tui/filetree"
	"github.com/dpa23/wari/internal/tui/theme"
)

// --- Ayudantes --------------------------------------------------------------

func write(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func project(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "src/Main.java", "public class Main {\n    int x;\n}\n")
	write(t, root, "src/util/Helper.java", "class Helper {}\n")
	write(t, root, "README.md", "# demo\n")
	write(t, root, "notes.txt", "hola\n")
	write(t, root, "blob.bin", "ab\x00cd")
	return root
}

func newLayout(t *testing.T, cfg Config, w, h int) Model {
	t.Helper()
	if cfg.Root == "" {
		cfg.Root = project(t)
	}
	cfg.Chat = tui.Config{}
	cfg.ShowChat = true // las pruebas parten del panel de chat visible
	m := New(cfg)
	m, _ = m.update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func press(code rune, mod tea.KeyMod) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code, Mod: mod} }

func text(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: []rune(s)[0], Text: s} }

func typeText(m Model, s string) Model {
	for _, r := range s {
		m, _ = m.update(text(string(r)))
	}
	return m
}

// send aplica msgs y devuelve el último cmd no nulo.
func send(m Model, msgs ...tea.Msg) (Model, tea.Cmd) {
	var last tea.Cmd
	for _, msg := range msgs {
		var c tea.Cmd
		m, c = m.update(msg)
		if c != nil {
			last = c
		}
	}
	return m, last
}

// feed ejecuta el cmd (si es de los que terminan en el acto) y entrega su mensaje.
func feed(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	if msg := cmd(); msg != nil {
		m, _ = m.update(msg)
	}
	return m
}

func lines(m Model) []string { return strings.Split(m.View().Content, "\n") }

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

func screen(m Model) string { return plain(m.View().Content) }

func checkDims(t *testing.T, m Model, w, h int, label string) {
	t.Helper()
	ls := lines(m)
	if len(ls) > h {
		t.Fatalf("%s: %d filas > alto %d", label, len(ls), h)
	}
	for i, l := range ls {
		if got := lipgloss.Width(l); got > w {
			t.Fatalf("%s: fila %d ancho %d > %d: %q", label, i, got, w, plain(l))
		}
	}
	if len(ls) != h {
		t.Fatalf("%s: %d filas, quería exactamente %d", label, len(ls), h)
	}
}

// --- Pruebas ----------------------------------------------------------------

func TestInitialLayout(t *testing.T) {
	m := newLayout(t, Config{}, 120, 30)
	v := m.View()
	if !v.AltScreen {
		t.Error("el layout raíz debe usar AltScreen")
	}
	s := screen(m)
	for _, want := range []string{"Archivos", "Editor", "Agentes", "(sin archivo)", "ctrl+p paleta"} {
		if !strings.Contains(s, want) {
			t.Errorf("falta %q en la vista", want)
		}
	}
	if m.focus != focusTree {
		t.Errorf("foco inicial = %v", m.focus)
	}
	g := m.geometry()
	// Árbol a la izquierda, editor en medio y chat a la derecha, sin huecos.
	if !g.showTree || !g.showEd || !g.showChat || g.tree.X != 0 ||
		g.ed.X != g.tree.W+1 || g.chat.X != g.ed.X+g.ed.W+1 || g.chat.X+g.chat.W != 120 {
		t.Errorf("reparto = %+v", g)
	}
	checkDims(t, m, 120, 30, "inicial")
}

func TestOpenFileFromConfigAndTitle(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, "src", "Main.java")}, 120, 30)
	if !m.hasEd || m.focus != focusEditor {
		t.Fatalf("hasEd=%v focus=%v", m.hasEd, m.focus)
	}
	s := screen(m)
	if !strings.Contains(s, "src/Main.java") || !strings.Contains(s, "public class Main") {
		t.Errorf("no se ve el archivo:\n%s", s)
	}
	m, _ = send(m, text("z"))
	if !m.ed.Dirty() || !strings.Contains(screen(m), "src/Main.java ●") || !strings.Contains(screen(m), "sin guardar") {
		t.Error("el estado sucio no se muestra")
	}
	// ctrl+s es global: guarda aunque el foco esté en otro panel.
	m, _ = send(m, press(tea.KeyTab, tea.ModShift)) // al árbol
	if m.focus != focusTree {
		t.Fatal("shift+tab no fue al árbol")
	}
	m, cmd := send(m, press('s', tea.ModCtrl))
	m = feed(m, cmd)
	if m.ed.Dirty() {
		t.Error("tras guardar debe estar limpio")
	}
	data, _ := os.ReadFile(filepath.Join(root, "src", "Main.java"))
	if !strings.HasPrefix(string(data), "zpublic class Main") {
		t.Errorf("archivo = %q", data)
	}
	if !strings.Contains(screen(m), "guardado src/Main.java") {
		t.Error("falta el aviso de guardado")
	}
}

func TestFocusCycleAndTabInEditor(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 120, 30)
	// Sin archivo: tab recorre árbol -> editor -> agentes -> árbol.
	var seen []focus
	for range 4 {
		seen = append(seen, m.focus)
		m, _ = send(m, press(tea.KeyTab, 0))
	}
	want := []focus{focusTree, focusEditor, focusChat, focusTree}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("ciclo = %v, quería %v", seen, want)
		}
	}
	// Tras cuatro tab el foco está en el editor; shift+tab vuelve al árbol y
	// otra vez a los agentes (el orden es circular).
	m, _ = send(m, press(tea.KeyTab, tea.ModShift), press(tea.KeyTab, tea.ModShift))
	if m.focus != focusChat {
		t.Errorf("shift+tab x2: %v", m.focus)
	}

	// Con un archivo en el editor, tab inserta espacios y no mueve el foco.
	m = newLayout(t, Config{Root: root, File: filepath.Join(root, "notes.txt")}, 120, 30)
	m, _ = send(m, press(tea.KeyTab, 0))
	if m.focus != focusEditor || m.ed.Buffer().String() != "    hola" {
		t.Fatalf("tab en el editor: foco=%v buffer=%q", m.focus, m.ed.Buffer().String())
	}
	m, _ = send(m, press(tea.KeyTab, tea.ModShift))
	if m.focus != focusTree {
		t.Errorf("shift+tab desde el editor: %v", m.focus)
	}
	m, _ = send(m, press(tea.KeyTab, 0))
	if m.focus != focusEditor {
		t.Errorf("tab desde el árbol: %v", m.focus)
	}
}

func TestFocusIsVisible(t *testing.T) {
	m := newLayout(t, Config{}, 120, 30)
	head := lines(m)[0]
	if !strings.Contains(head, headFocused.Render(fit(" Archivos", m.geometry().lay.Rects[idTree].W))) {
		t.Error("el título del panel con foco no tiene el estilo de foco")
	}
	m2, _ := send(m, press(tea.KeyTab, 0))
	if lines(m2)[0] == head {
		t.Error("cambiar el foco no cambió la vista")
	}
}

func TestTogglePanelsAndAutoFold(t *testing.T) {
	m := newLayout(t, Config{}, 120, 30)
	m, _ = send(m, press('b', tea.ModCtrl))
	if s := screen(m); strings.Contains(s, "Archivos") || !strings.Contains(s, "Agentes") {
		t.Error("ctrl+b debía ocultar solo el árbol")
	}
	checkDims(t, m, 120, 30, "sin árbol")
	m, _ = send(m, press('g', tea.ModCtrl))
	if s := screen(m); strings.Contains(s, "Agentes") {
		t.Error("ctrl+g debía ocultar los agentes")
	}
	checkDims(t, m, 120, 30, "solo editor")
	m, _ = send(m, press('b', tea.ModCtrl), press('g', tea.ModCtrl))
	if s := screen(m); !strings.Contains(s, "Archivos") || !strings.Contains(s, "Agentes") {
		t.Error("los paneles debían volver")
	}

	// Plegado automático: con 60 columnas los agentes no caben; con 30, ni el árbol.
	m, _ = send(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	if s := screen(m); !strings.Contains(s, "Archivos") || strings.Contains(s, "Agentes") {
		t.Errorf("a 60 columnas debía quedar árbol+editor:\n%s", s)
	}
	m, _ = send(m, tea.WindowSizeMsg{Width: 30, Height: 20})
	if s := screen(m); strings.Contains(s, "Archivos") || strings.Contains(s, "Agentes") {
		t.Errorf("a 30 columnas debía quedar solo el editor:\n%s", s)
	}
	// Al agrandar vuelven (las banderas del usuario no cambiaron).
	m, _ = send(m, tea.WindowSizeMsg{Width: 140, Height: 20})
	if s := screen(m); !strings.Contains(s, "Archivos") || !strings.Contains(s, "Agentes") {
		t.Error("al agrandar debían volver los paneles")
	}
}

func TestFocusMovesWhenPanelFolds(t *testing.T) {
	m := newLayout(t, Config{}, 120, 30)
	m, _ = send(m, press(tea.KeyTab, tea.ModShift)) // agentes
	if m.focus != focusChat {
		t.Fatal("no se fue a agentes")
	}
	m, _ = send(m, press('g', tea.ModCtrl))
	if m.focus != focusEditor {
		t.Errorf("al plegar el panel con foco debía pasar al editor, foco = %v", m.focus)
	}
}

func TestResizeNeverOverflows(t *testing.T) {
	root := project(t)
	for _, withFile := range []bool{false, true} {
		cfg := Config{Root: root}
		if withFile {
			cfg.File = filepath.Join(root, "src", "Main.java")
		}
		m := newLayout(t, cfg, 120, 30)
		for _, sz := range [][2]int{{200, 60}, {120, 30}, {80, 24}, {60, 15}, {40, 10}, {30, 8}, {20, 5}, {12, 3}, {10, 2}, {5, 1}, {1, 1}, {3, 40}} {
			for _, palette := range []bool{false, true} {
				m2, _ := send(m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
				if palette {
					m2, _ = send(m2, press('p', tea.ModCtrl), text("a"))
				}
				checkDims(t, m2, sz[0], sz[1], fmt.Sprintf("resize %dx%d paleta=%v archivo=%v", sz[0], sz[1], palette, withFile))
				if c := m2.View().Cursor; c != nil && (c.X < 0 || c.X >= sz[0] || c.Y < 0 || c.Y >= sz[1]) {
					t.Errorf("%dx%d: cursor fuera de la pantalla: %+v", sz[0], sz[1], c.Position)
				}
			}
		}
	}
}

func TestOpenFileFromTree(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 120, 30)
	// Filas: raíz, src, blob.bin, notes.txt, README.md (carpetas primero).
	m, _ = send(m, press(tea.KeyEnd, 0)) // README.md
	m, cmd := send(m, press(tea.KeyEnter, 0))
	if _, ok := cmd().(filetree.OpenFileMsg); !ok {
		t.Fatal("enter sobre un archivo debía emitir OpenFileMsg")
	}
	m = feed(m, cmd)
	if !m.hasEd || m.ed.Path() != filepath.Join(root, "README.md") || m.focus != focusEditor {
		t.Fatalf("hasEd=%v path=%q focus=%v", m.hasEd, m.ed.Path(), m.focus)
	}
	if !strings.Contains(screen(m), "# demo") {
		t.Error("el contenido no se ve")
	}
}

func TestBinaryAndMissingFilesAreRejected(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 120, 30)
	m, _ = send(m, filetree.OpenFileMsg{Path: filepath.Join(root, "blob.bin")})
	if m.hasEd || !strings.Contains(m.msg, "binario") {
		t.Errorf("binario: hasEd=%v msg=%q", m.hasEd, m.msg)
	}
	m, _ = send(m, filetree.OpenFileMsg{Path: filepath.Join(root, "no-existe.txt")})
	if m.hasEd || !strings.Contains(m.msg, "no se pudo abrir") {
		t.Errorf("inexistente: hasEd=%v msg=%q", m.hasEd, m.msg)
	}
}

func TestDirtyFileNeedsConfirmationToSwitch(t *testing.T) {
	root := project(t)
	notes, readme := filepath.Join(root, "notes.txt"), filepath.Join(root, "README.md")
	m := newLayout(t, Config{Root: root, File: notes}, 120, 30)
	m, _ = send(m, text("!"))
	m, _ = send(m, filetree.OpenFileMsg{Path: readme})
	if m.ed.Path() != notes || !strings.Contains(m.msg, "sin guardar") {
		t.Fatalf("debía rechazar el cambio: path=%q msg=%q", m.ed.Path(), m.msg)
	}
	if data, _ := os.ReadFile(notes); string(data) != "hola\n" {
		t.Error("no debe tocar el archivo en disco")
	}
	m, _ = send(m, filetree.OpenFileMsg{Path: readme})
	if m.ed.Path() != readme {
		t.Errorf("la segunda apertura debía descartar: path=%q", m.ed.Path())
	}
}

func TestQuitWithDirtyNeedsConfirmation(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, "notes.txt")}, 120, 30)
	isQuit := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}
	// Limpio: sale directo.
	if _, cmd := send(m, press('c', tea.ModCtrl)); !isQuit(cmd) {
		t.Fatal("ctrl+c con el archivo limpio debía salir")
	}
	m, _ = send(m, text("!"))
	m, cmd := send(m, press('c', tea.ModCtrl))
	if isQuit(cmd) || !strings.Contains(m.msg, "cambios sin guardar") {
		t.Fatalf("debía avisar: msg=%q", m.msg)
	}
	// Otra tecla desarma la confirmación.
	m, _ = send(m, press(tea.KeyLeft, 0))
	if _, cmd := send(m, press('c', tea.ModCtrl)); isQuit(cmd) {
		t.Error("tras otra tecla debía volver a pedir confirmación")
	}
	m, _ = send(m, press('c', tea.ModCtrl))
	if _, cmd := send(m, press('c', tea.ModCtrl)); !isQuit(cmd) {
		t.Error("la segunda pulsación seguida debía salir")
	}
}

// openPalette abre la paleta con ctrl+p y entrega la lista de archivos que
// recorre el Cmd en segundo plano.
func openPalette(m Model) Model {
	m, cmd := send(m, press('p', tea.ModCtrl))
	return feed(m, cmd)
}

func TestPaletteOpensFilesAndRunsActions(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 120, 30)
	m = openPalette(m)
	if !m.pal.open {
		t.Fatal("ctrl+p no abrió la paleta")
	}
	s := screen(m)
	for _, want := range []string{"> ", "Guardar archivo", "Salir", "ctrl+s"} {
		if !strings.Contains(s, want) {
			t.Errorf("la paleta no muestra %q:\n%s", want, s)
		}
	}
	if c := m.View().Cursor; c == nil || c.Y != 0 || c.X != 2 {
		t.Errorf("cursor de la paleta = %+v", c)
	}

	// Búsqueda difusa por nombre: "hlp" encuentra src/util/Helper.java.
	m = typeText(m, "hlp")
	if len(m.pal.results) == 0 || m.pal.items[m.pal.results[0]].id != cmdOpenPrefix+"src/util/Helper.java" {
		t.Fatalf("resultados = %v", m.pal.results)
	}
	if !strings.Contains(screen(m), "src/util/Helper.java") {
		t.Error("el resultado no se ve")
	}
	m, _ = send(m, press(tea.KeyBackspace, 0), press(tea.KeyBackspace, 0), press(tea.KeyBackspace, 0))
	if len(m.pal.query) != 0 {
		t.Error("backspace no borró la consulta")
	}
	m = typeText(m, "hlp")
	m, _ = send(m, press(tea.KeyEnter, 0))
	if m.pal.open || !m.hasEd || m.ed.Path() != filepath.Join(root, "src", "util", "Helper.java") || m.focus != focusEditor {
		t.Fatalf("no abrió el archivo: open=%v path=%q", m.pal.open, m.ed.Path())
	}

	// Escape cierra sin hacer nada.
	m, _ = send(m, press('p', tea.ModCtrl), text("x"), press(tea.KeyEscape, 0))
	if m.pal.open {
		t.Error("esc no cerró la paleta")
	}

	// Acciones: alternar árbol desde la paleta ("arbol" sin tilde coincide).
	m, _ = send(m, press('p', tea.ModCtrl))
	m = typeText(m, "arbol")
	m = selectCmd(t, m, cmdToggleTree)
	m, _ = send(m, press(tea.KeyEnter, 0))
	if m.treeOn() {
		t.Error("la acción de la paleta no alternó el árbol")
	}

	// Pegar texto en la paleta.
	m, _ = send(m, press('p', tea.ModCtrl), tea.PasteMsg{Content: "guard"})
	if string(m.pal.query) != "guard" || m.pal.items[m.pal.results[0]].id != cmdSave {
		t.Errorf("pegado: query=%q", string(m.pal.query))
	}
}

func TestPaletteQuitAndFocusCommands(t *testing.T) {
	m := newLayout(t, Config{}, 120, 30)
	m, _ = send(m, press('p', tea.ModCtrl))
	m = typeText(m, "agentes")
	m = selectCmd(t, m, cmdFocusChat)
	m, _ = send(m, press(tea.KeyEnter, 0))
	if m.focus != focusChat {
		t.Errorf("foco = %v", m.focus)
	}
	m, _ = send(m, press('p', tea.ModCtrl))
	m = typeText(m, "salir")
	_, cmd := send(m, press(tea.KeyEnter, 0))
	if cmd == nil {
		t.Fatal("'Salir' sin cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("'Salir' debía terminar el programa")
	}
}

// selectCmd mueve la selección de la paleta hasta la acción id.
func selectCmd(t *testing.T, m Model, id string) Model {
	t.Helper()
	idx := -1
	for i, r := range m.pal.results {
		if m.pal.items[r].id == id {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("la paleta no ofrece %q con la consulta %q", id, string(m.pal.query))
	}
	for range idx {
		m, _ = send(m, press(tea.KeyDown, 0))
	}
	return m
}

func TestFuzzyScore(t *testing.T) {
	cases := []struct {
		q, target string
		ok        bool
	}{
		{"", "x", true},
		{"hlp", "src/util/Helper.java", true},
		{"MAIN", "src/Main.java", true},
		{"xyz", "src/Main.java", false},
		{"jm", "src/Main.java", false},
		{"arbol", "Alternar árbol de archivos", true},
	}
	for _, c := range cases {
		if _, ok := fuzzyScore(c.q, c.target); ok != c.ok {
			t.Errorf("fuzzy(%q,%q) = %v", c.q, c.target, ok)
		}
	}
	a, _ := fuzzyScore("main", "src/Main.java")
	b, _ := fuzzyScore("main", "src/domain/remains.txt")
	if a <= b {
		t.Errorf("el nombre de archivo debía puntuar más: %d <= %d", a, b)
	}
}

func TestCursorTranslation(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, "src", "Main.java")}, 120, 30)
	g := m.geometry()
	// Editor: gutter de 1 dígito + 2 = 3; cursor en (0,0) del buffer.
	c := m.View().Cursor
	if c == nil || c.X != g.ed.X+3 || c.Y != g.ed.Y {
		t.Fatalf("cursor del editor = %+v, quería (%d,%d)", c, g.ed.X+3, g.ed.Y)
	}
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	c = m.View().Cursor
	if c == nil || c.X != g.ed.X+3+len("    int x;") || c.Y != g.ed.Y+1 {
		t.Fatalf("cursor tras mover = %+v", c)
	}
	// Árbol: sin cursor real.
	m, _ = send(m, press(tea.KeyTab, tea.ModShift))
	if m.View().Cursor != nil {
		t.Error("el árbol no debe mostrar cursor")
	}
	// Agentes: cursor dentro de su columna.
	m, _ = send(m, press(tea.KeyTab, tea.ModShift))
	c = m.View().Cursor
	if m.focus != focusChat || c == nil || c.X < g.chat.X || c.X >= g.chat.X+g.chat.W {
		t.Fatalf("foco=%v cursor del chat = %+v (chatX=%d)", m.focus, c, g.chat.X)
	}
}

func TestKeysComeFromConfig(t *testing.T) {
	km := config.Default()
	km["toggle_tree"] = []string{"ctrl+t"}
	km["palette"] = []string{"ctrl+k"}
	km["focus_next"] = []string{"ctrl+l"}
	root := project(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, "notes.txt"), Keymap: km}, 120, 30)

	m, _ = send(m, press('b', tea.ModCtrl)) // ya no es toggle_tree
	if !m.treeOn() {
		t.Error("ctrl+b ya no debía alternar el árbol")
	}
	m, _ = send(m, press('t', tea.ModCtrl))
	if m.treeOn() {
		t.Error("ctrl+t debía alternar el árbol")
	}
	m, _ = send(m, press('p', tea.ModCtrl))
	if m.pal.open {
		t.Error("ctrl+p ya no abre la paleta")
	}
	m, _ = send(m, press('k', tea.ModCtrl))
	if !m.pal.open || !strings.Contains(screen(m), "ctrl+s") {
		t.Error("ctrl+k debía abrir la paleta")
	}
	m, _ = send(m, press(tea.KeyEscape, 0))
	// focus_next remapeado a una tecla que el editor no usa: funciona dentro del editor.
	m, _ = send(m, press('l', tea.ModCtrl))
	if m.focus != focusChat {
		t.Errorf("ctrl+l desde el editor: foco = %v", m.focus)
	}
	if !strings.Contains(screen(m), "ctrl+k paleta") {
		t.Error("la barra de estado no refleja la configuración")
	}
}

func TestWindowTitleAndInit(t *testing.T) {
	m := newLayout(t, Config{}, 80, 24)
	if m.View().WindowTitle != "WARI" || m.Init() != nil {
		t.Error("título o Init inesperados")
	}
	m.w, m.h = 0, 0
	if m.View().Content != "" || m.View().Cursor != nil {
		t.Error("tamaño 0 debe dar vista vacía")
	}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestPaletteQuitWithDirtyNeedsConfirmation(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, "notes.txt")}, 120, 30)
	m, _ = send(m, text("!"))
	runQuit := func(m Model) (Model, tea.Cmd) {
		m, _ = send(m, press('p', tea.ModCtrl))
		m = typeText(m, "salir")
		return send(m, press(tea.KeyEnter, 0))
	}
	m, cmd := runQuit(m)
	if isQuit(cmd) || !strings.Contains(m.msg, "cambios sin guardar") {
		t.Fatalf("'Salir' con cambios debía avisar: msg=%q", m.msg)
	}
	// Repetir 'Salir' (abrir la paleta no desarma) confirma.
	if _, cmd := runQuit(m); !isQuit(cmd) {
		t.Error("la segunda petición de 'Salir' debía salir")
	}
	// La tecla de salir también cuenta como segunda petición.
	if _, cmd := send(m, press('c', tea.ModCtrl)); !isQuit(cmd) {
		t.Error("ctrl+c tras el aviso de la paleta debía salir")
	}
	// Cancelar la paleta desarma la confirmación.
	m, _ = send(m, press('p', tea.ModCtrl), press(tea.KeyEscape, 0))
	if _, cmd := send(m, press('c', tea.ModCtrl)); isQuit(cmd) {
		t.Error("tras cancelar la paleta debía volver a pedir confirmación")
	}
}

func TestNonUTF8FileIsRejected(t *testing.T) {
	root := project(t)
	latin1 := "a\xf1o\n" // "año" en Latin-1
	p := write(t, root, "latin1.txt", latin1)
	m := newLayout(t, Config{Root: root}, 120, 30)
	m, _ = send(m, filetree.OpenFileMsg{Path: p})
	if m.hasEd || !strings.Contains(m.msg, "UTF-8") {
		t.Errorf("hasEd=%v msg=%q, quería rechazo por UTF-8", m.hasEd, m.msg)
	}
	if data, _ := os.ReadFile(p); string(data) != latin1 {
		t.Error("no debe tocar el archivo")
	}
	// Un UTF-8 válido con acentos sí se abre.
	ok := write(t, root, "utf8.txt", "año\n")
	m, _ = send(m, filetree.OpenFileMsg{Path: ok})
	if !m.hasEd || m.ed.Path() != ok {
		t.Errorf("UTF-8 válido: hasEd=%v msg=%q", m.hasEd, m.msg)
	}
}

func TestPaletteAcceptsAltGrText(t *testing.T) {
	m := newLayout(t, Config{}, 120, 30)
	m, _ = send(m, press('p', tea.ModCtrl))
	// En Windows, AltGr llega como Text con ctrl+alt.
	m, _ = send(m, tea.KeyPressMsg{Code: '@', Text: "@", Mod: tea.ModCtrl | tea.ModAlt})
	if string(m.pal.query) != "@" {
		t.Errorf("consulta = %q, quería \"@\"", string(m.pal.query))
	}
	// Una combinación sin texto no escribe nada.
	m, _ = send(m, press('x', tea.ModCtrl))
	if string(m.pal.query) != "@" {
		t.Errorf("consulta = %q tras ctrl+x", string(m.pal.query))
	}
}

func TestPendingOpenIsDisarmedByOtherKeys(t *testing.T) {
	root := project(t)
	notes, readme := filepath.Join(root, "notes.txt"), filepath.Join(root, "README.md")
	m := newLayout(t, Config{Root: root, File: notes}, 120, 30)
	m, _ = send(m, text("!"))
	m, _ = send(m, filetree.OpenFileMsg{Path: readme})
	if m.pendingOpen != readme {
		t.Fatalf("pendingOpen = %q, quería armado", m.pendingOpen)
	}
	// Seguir editando desarma: abrir README otra vez vuelve a avisar.
	m, _ = send(m, text("?"))
	if m.pendingOpen != "" {
		t.Error("escribir debía desarmar la confirmación")
	}
	m, _ = send(m, filetree.OpenFileMsg{Path: readme})
	if m.ed.Path() != notes || !strings.Contains(m.msg, "sin guardar") {
		t.Fatalf("debía volver a avisar: path=%q msg=%q", m.ed.Path(), m.msg)
	}
	// Un atajo global también desarma.
	m, _ = send(m, press('b', tea.ModCtrl)) // toggle_tree
	if m.pendingOpen != "" {
		t.Error("un atajo global debía desarmar la confirmación")
	}
}

func TestOpenFromTreeConfirmsWithSecondEnter(t *testing.T) {
	root := project(t)
	notes, readme := filepath.Join(root, "notes.txt"), filepath.Join(root, "README.md")
	m := newLayout(t, Config{Root: root, File: notes}, 120, 30)
	m, _ = send(m, text("!"))
	m, _ = send(m, press(tea.KeyTab, tea.ModShift)) // al árbol
	if m.focus != focusTree {
		t.Fatalf("foco = %v", m.focus)
	}
	m, _ = send(m, press(tea.KeyEnd, 0)) // README.md
	m, cmd := send(m, press(tea.KeyEnter, 0))
	m = feed(m, cmd)
	if m.ed.Path() != notes || m.pendingOpen != readme {
		t.Fatalf("primer Enter: path=%q pending=%q", m.ed.Path(), m.pendingOpen)
	}
	m, cmd = send(m, press(tea.KeyEnter, 0))
	m = feed(m, cmd)
	if m.ed.Path() != readme {
		t.Errorf("el segundo Enter debía abrir README: path=%q msg=%q", m.ed.Path(), m.msg)
	}
}

func TestPaletteFileListIsAsyncAndCached(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 120, 30)
	m, cmd := send(m, press('p', tea.ModCtrl))
	if cmd == nil {
		t.Fatal("ctrl+p debía devolver un Cmd que recorra el proyecto")
	}
	hasFile := func(m Model) bool {
		for _, it := range m.pal.items {
			if strings.HasPrefix(it.id, cmdOpenPrefix) {
				return true
			}
		}
		return false
	}
	if hasFile(m) {
		t.Error("al abrir no debía haber archivos: el recorrido va en el Cmd")
	}
	m = feed(m, cmd)
	if !hasFile(m) {
		t.Fatal("tras el Cmd debía haber archivos")
	}
	for _, it := range m.pal.items {
		if len(it.folded) != len([]rune(it.title)) {
			t.Fatalf("el título plegado de %q no se calculó", it.title)
		}
	}

	// Cerrar y reabrir: la lista cacheada está al instante, aunque aparezcan
	// archivos nuevos (llegan con el refresco).
	m, _ = send(m, press(tea.KeyEscape, 0))
	write(t, root, "nuevo.txt", "x")
	m, cmd = send(m, press('p', tea.ModCtrl))
	if !hasFile(m) {
		t.Error("al reabrir debía usarse la lista cacheada")
	}
	for _, it := range m.pal.items {
		if it.title == "nuevo.txt" {
			t.Error("el archivo nuevo no debía estar antes del refresco")
		}
	}
	m = feed(m, cmd)
	var found bool
	for _, it := range m.pal.items {
		found = found || it.title == "nuevo.txt"
	}
	if !found {
		t.Error("el refresco debía traer el archivo nuevo")
	}
}

func TestSaveWarnsWhenFileChangedOnDisk(t *testing.T) {
	root := project(t)
	notes := filepath.Join(root, "notes.txt")
	m := newLayout(t, Config{Root: root, File: notes}, 120, 30)
	m, _ = send(m, text("!"))
	if err := os.WriteFile(notes, []byte("de otro programa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, cmd := send(m, press('s', tea.ModCtrl))
	m = feed(m, cmd)
	if !strings.Contains(m.msg, "el archivo cambió en disco; ctrl+s otra vez para sobrescribir") {
		t.Errorf("msg = %q", m.msg)
	}
	if data, _ := os.ReadFile(notes); string(data) != "de otro programa\n" {
		t.Errorf("no debía pisar el archivo: %q", data)
	}
	m, cmd = send(m, press('s', tea.ModCtrl))
	m = feed(m, cmd)
	if data, _ := os.ReadFile(notes); string(data) != "!hola\n" {
		t.Errorf("el segundo guardado debía sobrescribir: %q", data)
	}
	if !strings.Contains(m.msg, "guardado") {
		t.Errorf("msg = %q", m.msg)
	}
}

func TestDisplayRelativeToRoot(t *testing.T) {
	m := Model{root: filepath.FromSlash("/x/y")}
	cases := map[string]string{
		filepath.FromSlash("/x/y/a/b.go"):   "a/b.go",
		filepath.FromSlash("/x/y/..oculto"): "..oculto", // un nombre que empieza con ".." sigue dentro
		filepath.FromSlash("/x/..z"):        filepath.FromSlash("/x/..z"),
		filepath.FromSlash("/x"):            filepath.FromSlash("/x"),
		filepath.FromSlash("/otro/f.go"):    filepath.FromSlash("/otro/f.go"),
	}
	for in, want := range cases {
		if got := m.display(in); got != want {
			t.Errorf("display(%q) = %q, quería %q", in, got, want)
		}
	}
}

// qfake es un agente mínimo para probar la parada global desde el layout.
type qfake struct {
	mu      sync.Mutex
	ch      chan agent.Event
	cancels int
}

func (f *qfake) Start(context.Context, agent.Task) error { return nil }
func (f *qfake) Send(string) error                       { return nil }
func (f *qfake) Events() <-chan agent.Event              { return f.ch }
func (f *qfake) Pause() error                            { return nil }
func (f *qfake) Resume() error                           { return nil }
func (f *qfake) Close() error                            { return nil }
func (f *qfake) Status() agent.Status                    { return agent.StatusIdle }
func (f *qfake) Cancel() error {
	f.mu.Lock()
	f.cancels++
	f.mu.Unlock()
	return nil
}
func (f *qfake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancels
}

func TestStatusBarShowsAgentsAndGlobalStop(t *testing.T) {
	// Sin supervisor: contadores en cero y el atajo de parada siempre visible,
	// incluso con poco ancho.
	for _, w := range []int{120, 60, 48} {
		m := newLayout(t, Config{}, w, 24)
		bar := lines(m)[len(lines(m))-1]
		if want := "activos 0/2 · en cola 0 · ctrl+k parar todo"; !strings.Contains(plain(bar), want) {
			t.Errorf("ancho %d: la barra no contiene %q: %q", w, want, plain(bar))
		}
	}
	// Con un supervisor de 3 y un agente corriendo.
	sup := orch.New(3)
	defer sup.Close()
	f := &qfake{ch: make(chan agent.Event, 4)}
	sup.Submit("a", f, agent.Task{Prompt: "x"}, orch.Budget{})
	m := New(Config{ShowChat: true, Root: project(t), Chat: tui.Config{Supervisor: sup}})
	m, _ = m.update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if !strings.Contains(screen(m), "activos 0/3 · en cola 0") {
		t.Errorf("el límite del supervisor no se muestra:\n%s", screen(m))
	}

	// ctrl+k es global: vale con el foco en el árbol y cancela a todos.
	if m.focus != focusTree {
		t.Fatalf("foco = %v", m.focus)
	}
	m, cmd := send(m, press('k', tea.ModCtrl))
	if cmd == nil {
		t.Fatal("stop_all debe devolver un Cmd")
	}
	cmd()
	if f.count() != 1 {
		t.Fatalf("cancels = %d", f.count())
	}
	if !strings.Contains(screen(m), "parando todos los agentes") {
		t.Error("falta el aviso de la parada global")
	}
}

func TestLeaderKeyActsAsAlt(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, "notes.txt")}, 120, 30)
	m, _ = send(m, press('e', tea.ModCtrl))
	if !m.leaderArmed {
		t.Fatal("ctrl+e debía armar la tecla líder")
	}
	m, _ = send(m, text("1")) // equivale a alt+1: foco en el árbol
	if m.leaderArmed || m.focus != focusTree {
		t.Errorf("líder+1: armada=%v foco=%v", m.leaderArmed, m.focus)
	}
	m, _ = send(m, press('e', tea.ModCtrl), press(tea.KeyEscape, 0))
	if m.leaderArmed {
		t.Error("esc debía desarmar la tecla líder")
	}
	m, _ = send(m, text("2")) // sin líder: no cambia el foco
	if m.focus == focusEditor {
		t.Error("'2' sin líder no debía mover el foco")
	}
}

func TestBackgroundColorMsgUpdatesTheme(t *testing.T) {
	defer theme.SetDark(true)
	m := newLayout(t, Config{}, 80, 24)
	m, _ = send(m, tea.BackgroundColorMsg{Color: color.White})
	if theme.Dark() {
		t.Error("un fondo blanco debía dejar el tema claro")
	}
	_, _ = send(m, tea.BackgroundColorMsg{Color: color.Black})
	if !theme.Dark() {
		t.Error("un fondo negro debía dejar el tema oscuro")
	}
}

func TestInitAsksBackgroundOnlyWhenUnknown(t *testing.T) {
	theme.SetDark(true)
	defer theme.SetDark(true)
	if m := newLayout(t, Config{}, 80, 24); m.Init() != nil {
		t.Error("con el tema resuelto Init no debe consultar a la terminal")
	}
}

func TestArrowKeysKeepLayoutInsideScreen(t *testing.T) {
	root := project(t)
	for _, size := range [][2]int{{202, 49}, {120, 30}, {80, 24}} {
		m := newLayout(t, Config{Root: root, File: filepath.Join(root, "notes.txt")}, size[0], size[1])
		for _, focus := range []func(Model) Model{
			func(m Model) Model { return m.focusTreePanel() },
			func(m Model) Model { return m.focusEditorPanel() },
			func(m Model) Model { return m.focusAgentsPanel() },
		} {
			m = focus(m)
			for _, k := range []tea.KeyPressMsg{
				press(tea.KeyUp, 0), press(tea.KeyUp, 0), press(tea.KeyDown, 0),
				press(tea.KeyUp, tea.ModAlt), press(tea.KeyDown, tea.ModAlt),
				press(tea.KeyPgUp, 0), press(tea.KeyPgDown, 0),
			} {
				m, _ = send(m, k)
				checkDims(t, m, size[0], size[1], "tras "+k.String())
			}
		}
	}
}

func TestStatusBarAlertShortensAgentsInfoWholeWords(t *testing.T) {
	m := newLayout(t, Config{}, 80, 24)
	vs := m.agentsInfoVariants()
	if got := fitVariant(vs, 200); got != vs[0] {
		t.Errorf("con espacio debe usar la forma completa: %q", got)
	}
	if got := fitVariant(vs, 22); got != "act 0/2 · cola 0" {
		t.Errorf("a 22 celdas = %q", got)
	}
	if got := fitVariant(vs, 5); got != "0/2·0" {
		t.Errorf("a 5 celdas = %q", got)
	}
}
