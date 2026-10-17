package filetree

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// project crea un árbol de ejemplo.
func project(t *testing.T) string {
	root := t.TempDir()
	for _, f := range []string{
		"README.md", "b.txt", "Zeta.java",
		"src/main/App.java", "src/main/util/Helper.java", "src/Readme.txt",
		".git/config", ".wari/keys.json", "bin/app", "target/x.class", "obj/y", "build/z",
		"docs/guide.md", "logs/app.log", "tmp/cache.dat", "keep/important.md",
	} {
		write(t, root, f, "x")
	}
	write(t, root, ".gitignore", "# comentario\n*.log\n/tmp/\n!keep/important.md\ndocs/guide.md\n")
	return root
}

func press(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

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

func names(m Model) []string {
	var out []string
	for _, r := range m.rows {
		out = append(out, strings.Repeat(" ", r.depth)+r.name)
	}
	return out
}

func newTree(t *testing.T, root string) Model {
	m := New(root)
	m.SetSize(30, 12)
	m.Focus()
	return m
}

func TestInitialOrderAndIgnores(t *testing.T) {
	m := newTree(t, project(t))
	got := names(m)
	// Carpetas primero y luego archivos, sin distinguir mayúsculas. Se ocultan
	// .git, .wari, bin, target, obj, build y lo del .gitignore (logs/app.log y
	// tmp/); docs y logs siguen visibles porque la carpeta no se ignora.
	wantRows := []string{
		filepath.Base(m.Root()),
		" docs", " keep", " logs", " src",
		" .gitignore", " b.txt", " README.md", " Zeta.java",
	}
	if !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("filas:\n got %q\nwant %q", got, wantRows)
	}
	for _, bad := range []string{".git", ".wari", "bin", "target", "obj", "build", "tmp"} {
		for _, r := range got {
			if strings.TrimSpace(r) == bad {
				t.Errorf("%s no debía mostrarse", bad)
			}
		}
	}
}

func TestExpandCollapseAndNavigate(t *testing.T) {
	m := newTree(t, project(t))
	// cursor en la raíz; bajar hasta src (fila 4)
	m, _ = send(m, press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyDown), press(tea.KeyDown))
	if m.rows[m.cursor].name != "src" {
		t.Fatalf("cursor en %q", m.rows[m.cursor].name)
	}
	m, _ = send(m, press(tea.KeyEnter))
	if got := names(m); !contains(got, "  main") || !contains(got, "  Readme.txt") {
		t.Fatalf("src no se desplegó: %q", got)
	}
	// right sobre carpeta ya abierta baja al primer hijo (main)
	m, _ = send(m, press(tea.KeyRight))
	if m.rows[m.cursor].name != "main" {
		t.Fatalf("right: cursor en %q", m.rows[m.cursor].name)
	}
	m, _ = send(m, press(tea.KeyRight), press(tea.KeyDown)) // abre main y baja a util
	if m.rows[m.cursor].name != "util" {
		t.Fatalf("cursor en %q", m.rows[m.cursor].name)
	}
	// left sobre carpeta cerrada sube al padre
	m, _ = send(m, press(tea.KeyLeft))
	if m.rows[m.cursor].name != "main" {
		t.Fatalf("left: cursor en %q", m.rows[m.cursor].name)
	}
	// left sobre carpeta abierta la pliega
	m, _ = send(m, press(tea.KeyLeft))
	if contains(names(m), "   App.java") {
		t.Fatalf("main no se plegó: %q", names(m))
	}
	// enter sobre carpeta cerrada la abre y otra vez la cierra
	m, _ = send(m, press(tea.KeyEnter))
	if !contains(names(m), "   App.java") {
		t.Fatal("enter no desplegó")
	}
	m, _ = send(m, press(tea.KeyEnter))
	if contains(names(m), "   App.java") {
		t.Fatal("enter no plegó")
	}
	// home / end
	m, _ = send(m, press(tea.KeyEnd))
	if m.cursor != len(m.rows)-1 {
		t.Error("end")
	}
	m, _ = send(m, press(tea.KeyHome))
	if m.cursor != 0 {
		t.Error("home")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestEnterOnFileEmitsOpenFileMsg(t *testing.T) {
	root := project(t)
	m := newTree(t, root)
	m, _ = send(m, press(tea.KeyEnd)) // Zeta.java
	_, cmd := send(m, press(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("sin cmd")
	}
	msg, ok := cmd().(OpenFileMsg)
	if !ok || msg.Path != filepath.Join(m.Root(), "Zeta.java") {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestUnfocusedIgnoresKeys(t *testing.T) {
	m := newTree(t, project(t))
	m.Blur()
	m, cmd := send(m, press(tea.KeyDown), press(tea.KeyEnter))
	if m.cursor != 0 || cmd != nil {
		t.Error("sin foco actuó")
	}
}

func TestScrollFollowsCursor(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		write(t, root, "f"+string(rune('a'+i/10))+string(rune('a'+i%10))+".txt", "")
	}
	m := New(root)
	m.SetSize(20, 5)
	m.Focus()
	for i := 0; i < 30; i++ {
		m, _ = send(m, press(tea.KeyDown))
	}
	if m.cursor < m.top || m.cursor >= m.top+5 {
		t.Fatalf("cursor %d fuera de [%d,%d)", m.cursor, m.top, m.top+5)
	}
	m, _ = send(m, press(tea.KeyPgUp))
	if m.cursor != 26 {
		t.Errorf("pgup: cursor %d", m.cursor)
	}
}

func TestViewDimensions(t *testing.T) {
	m := newTree(t, project(t))
	for _, sz := range [][2]int{{30, 12}, {8, 3}, {1, 1}, {50, 30}} {
		m.SetSize(sz[0], sz[1])
		lines := strings.Split(m.View(), "\n")
		if len(lines) != sz[1] {
			t.Fatalf("%v: %d filas", sz, len(lines))
		}
		for i, l := range lines {
			if lipgloss.Width(l) != sz[0] {
				t.Errorf("%v fila %d: ancho %d", sz, i, lipgloss.Width(l))
			}
		}
	}
	m.SetSize(0, 0)
	if m.View() != "" {
		t.Error("tamaño 0")
	}
}

func TestRevealAndListFiles(t *testing.T) {
	root := project(t)
	m := newTree(t, root)
	m.Reveal(filepath.Join(root, "src", "main", "util", "Helper.java"))
	if m.rows[m.cursor].name != "Helper.java" {
		t.Fatalf("cursor en %q", m.rows[m.cursor].name)
	}
	want := []string{".gitignore", "README.md", "Zeta.java", "b.txt", "keep/important.md",
		"src/Readme.txt", "src/main/App.java", "src/main/util/Helper.java"}
	got := ListFiles(root, 0)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListFiles:\n got %q\nwant %q", got, want)
	}
	if got := ListFiles(root, 3); len(got) != 3 {
		t.Errorf("límite: %d", len(got))
	}
}

func TestIgnorePatterns(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".gitignore", "*.tmp\n/only-root\nsub/deep.txt\nnode_*/\n**/gen\n")
	ig := newIgnorer(root)
	cases := []struct {
		rel   string
		dir   bool
		want  bool
		label string
	}{
		{"a.tmp", false, true, "glob en raíz"},
		{"x/a.tmp", false, true, "glob en subcarpeta"},
		{"only-root", false, true, "anclado en raíz"},
		{"x/only-root", false, false, "anclado no aplica en subcarpetas"},
		{"sub/deep.txt", false, true, "ruta con /"},
		{"other/deep.txt", false, false, "ruta con / no aplica a otro"},
		{"node_modules", true, true, "solo carpetas"},
		{"node_modules", false, false, "solo carpetas no aplica a archivos"},
		{"a/b/gen", true, true, "**/ inicial"},
		{"src", true, false, "normal"},
		{"build", true, true, "fija: build carpeta"},
		{"x/build", true, false, "build en subcarpeta no se oculta"},
		{"build", false, false, "build archivo no se oculta"},
		{".git", true, true, "fija: .git"},
	}
	for _, c := range cases {
		name := c.rel[strings.LastIndex(c.rel, "/")+1:]
		if got := ig.ignored(c.rel, name, c.dir); got != c.want {
			t.Errorf("%s (%s): got %v", c.label, c.rel, got)
		}
	}
}

func TestIgnoredDirOnlyAtRoot(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{
		"bin/app",
		"build/out.o",
		"target/classes/A.class",
		"obj/debug.o",
		"src/main/java/com/acme/build/Builder.java",
		"src/main/bin/run.sh",
		"src/main/target/config.xml",
		"src/main/obj/data.txt",
	} {
		write(t, root, f, "content")
	}

	files := ListFiles(root, 0)
	want := []string{
		"src/main/bin/run.sh",
		"src/main/java/com/acme/build/Builder.java",
		"src/main/obj/data.txt",
		"src/main/target/config.xml",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("ListFiles:\n got %q\nwant %q", files, want)
	}

	m := newTree(t, root)
	target := filepath.Join(root, "src", "main", "java", "com", "acme", "build", "Builder.java")
	m.Reveal(target)
	if m.Selected() != target {
		t.Fatalf("Reveal no encontró %s, cursor en %s", target, m.Selected())
	}
	if m.rows[m.cursor].name != "Builder.java" {
		t.Fatalf("se esperaba cursor en Builder.java, got %q", m.rows[m.cursor].name)
	}
}

func TestReveal_DotDotPrefix(t *testing.T) {
	root := t.TempDir()
	write(t, root, "..env", "SECRET=1")
	write(t, root, "sub/..env", "NESTED=1")

	m := newTree(t, root)

	// Revelar archivo con prefijo .. en la raíz
	targetRoot := filepath.Join(root, "..env")
	m.Reveal(targetRoot)
	if m.Selected() != targetRoot {
		t.Fatalf("Reveal no encontró %s, cursor en %s", targetRoot, m.Selected())
	}
	if m.rows[m.cursor].name != "..env" {
		t.Fatalf("se esperaba cursor en ..env, got %q", m.rows[m.cursor].name)
	}

	// Revelar archivo con prefijo .. en subcarpeta
	targetSub := filepath.Join(root, "sub", "..env")
	m.Reveal(targetSub)
	if m.Selected() != targetSub {
		t.Fatalf("Reveal no encontró %s, cursor en %s", targetSub, m.Selected())
	}
	if m.rows[m.cursor].name != "..env" {
		t.Fatalf("se esperaba cursor en ..env de subcarpeta, got %q", m.rows[m.cursor].name)
	}

	// Un archivo fuera de la raíz no debe mover el cursor
	outside := filepath.Join(root, "..", "outside.txt")
	curBefore := m.cursor
	m.Reveal(outside)
	if m.cursor != curBefore {
		t.Fatalf("Reveal no debe actuar sobre archivo fuera de la raíz")
	}
}
