package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/tui"
	"github.com/dpa23/wari/internal/workspace/checkpoint"
)

func numbered(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "linea %d\n", i)
	}
	return sb.String()
}

// diffLayout arma un layout con un agente pausado que editó src/Big.txt (dos
// hunks) y notes.txt abierto en el editor con un cambio sin guardar.
func diffLayout(t *testing.T, w, h int) (m Model, root, target, original string) {
	t.Helper()
	root = project(t)
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	original = numbered(40)
	target = write(t, root, "src/Big.txt", original)
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sup := orch.New(2)
	t.Cleanup(sup.Close)
	if err := sup.AttachWorkspace(orch.Workspace{Root: root, Store: store, RunID: "t-"}); err != nil {
		t.Fatal(err)
	}
	var made []*efake
	m = New(Config{Root: root, File: filepath.Join(root, "notes.txt"), Chat: tui.Config{
		Supervisor: sup,
		NewAgent:   func() agent.Agent { f := newEfake(); made = append(made, f); return f },
	}})
	d := newDriver(t, m)
	d.run(d.m.Init()) // escucha las novedades del supervisor
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	d.m = d.m.focusPanel(focusChat)
	d.send(typeKeys("hola")...)
	d.send(press(tea.KeyEnter, 0))
	if len(made) != 1 {
		t.Fatalf("agentes = %d", len(made))
	}
	made[0].ch <- agent.FileEditProposed{Tool: "Edit", Path: target}
	d.until("edición con checkpoint", func(m Model) bool {
		eds := sup.Edits(1)
		return len(eds) > 0 && eds[0].Saved && strings.Contains(screen(m), "✎ src/Big.txt")
	})
	cur := strings.Replace(original, "linea 3\n", "TRES\n", 1)
	cur = strings.Replace(cur, "linea 35\n", "TREINTA Y CINCO\n", 1)
	write(t, root, "src/Big.txt", cur)
	d.send(press('o', tea.ModCtrl)) // pausa: con el agente corriendo no se revierte
	d.until("agente pausado", func(Model) bool { return sup.Snapshots()[0].State == orch.Paused })
	d.drain(20 * time.Millisecond)

	m = d.m.focusPanel(focusEditor)
	m, _ = send(m, text("!")) // cambio sin guardar en notes.txt
	if !m.ed.Dirty() {
		t.Fatal("el editor debía quedar sucio")
	}
	return m, root, target, original
}

// centerColumn devuelve el texto de la columna del editor/diff.
func centerColumn(m Model) string {
	g := m.geometry()
	var sb strings.Builder
	for _, l := range strings.Split(screen(m), "\n")[g.headH : g.headH+g.bodyH] {
		r := []rune(l)
		lo, hi := min(g.edX, len(r)), min(g.edX+g.edW, len(r))
		sb.WriteString(string(r[lo:hi]) + "\n")
	}
	return sb.String()
}

func TestCtrlROpensDiffInCenterAndEscRestoresEditor(t *testing.T) {
	m, _, _, _ := diffLayout(t, 140, 30)
	path := m.ed.Path()
	hist := screen(m)
	if !strings.Contains(hist, "✎ src/Big.txt") {
		t.Fatalf("el historial debía mostrar la edición:\n%s", hist)
	}

	m, cmd := send(m, press('r', tea.ModCtrl))
	m = feed(m, cmd)
	if !m.diffShown() || m.focus != focusEditor {
		t.Fatalf("diffShown=%v foco=%v", m.diffShown(), m.focus)
	}
	center := centerColumn(m)
	// El segundo hunk queda fuera de la primera pantalla: se ve con n.
	for _, want := range []string{"-linea 3", "+TRES", "@@ -1,6 +1,6 @@"} {
		if !strings.Contains(center, want) {
			t.Errorf("el centro no muestra %q:\n%s", want, center)
		}
	}
	scr := screen(m)
	if !strings.Contains(scr, "diff src/Big.txt · turno 1 · hunk 1/2") {
		t.Fatalf("título del diff ausente:\n%s", scr)
	}
	if !strings.Contains(scr, "✎ src/Big.txt") {
		t.Fatal("el panel de agentes debía conservar su historial")
	}
	if strings.Contains(scr, "public class") {
		t.Fatal("el editor no debía verse")
	}
	if m.cursor() != nil {
		t.Fatal("sin cursor sobre la vista de solo lectura")
	}
	checkDims(t, m, 140, 30, "diff abierto")

	// Con el diff abierto el editor no recibe texto ni pegados.
	m, _ = send(m, text("z"), tea.PasteMsg{Content: "pegado"})
	m, _ = send(m, press('s', tea.ModCtrl)) // guardar sigue siendo global; no debe fallar

	m, _ = send(m, press(tea.KeyEscape, 0))
	if m.diffShown() || m.focus != focusEditor {
		t.Fatalf("Esc debía volver al editor: diffShown=%v foco=%v", m.diffShown(), m.focus)
	}
	if m.ed.Path() != path || !strings.HasSuffix(path, "notes.txt") {
		t.Fatalf("archivo = %q, quería %q", m.ed.Path(), path)
	}
	if !strings.Contains(screen(m), "hola") {
		t.Fatalf("el editor debía volver a verse:\n%s", screen(m))
	}
}

func TestDiffEscKeepsUnsavedChanges(t *testing.T) {
	m, _, _, _ := diffLayout(t, 140, 30)
	before := m.ed.Buffer().String()
	m, cmd := send(m, press('r', tea.ModCtrl))
	m = feed(m, cmd)
	m, _ = send(m, text("z"), tea.PasteMsg{Content: "pegado"}, press(tea.KeyTab, 0))
	m, _ = send(m, press(tea.KeyEscape, 0))
	if !m.ed.Dirty() {
		t.Fatal("el archivo debía seguir con cambios sin guardar")
	}
	if got := m.ed.Buffer().String(); got != before {
		t.Fatalf("el buffer cambió con el diff abierto:\n%q\n%q", got, before)
	}
}

func TestDiffHunkNavigation(t *testing.T) {
	m, _, _, _ := diffLayout(t, 140, 30)
	m, cmd := send(m, press('r', tea.ModCtrl))
	m = feed(m, cmd)
	title := func() string { return m.chat.DiffTitle() }
	if !strings.Contains(title(), "hunk 1/2") {
		t.Fatalf("título = %q", title())
	}
	m, _ = send(m, text("n"))
	if !strings.Contains(title(), "hunk 2/2") || !strings.Contains(centerColumn(m), "+TREINTA Y CINCO") {
		t.Fatalf("n debía ir al segundo hunk: %q\n%s", title(), centerColumn(m))
	}
	m, _ = send(m, text("n")) // no pasa del último
	if !strings.Contains(title(), "hunk 2/2") {
		t.Fatalf("título = %q", title())
	}
	m, _ = send(m, text("p"))
	if !strings.Contains(title(), "hunk 1/2") || !strings.Contains(centerColumn(m), "+TRES") {
		t.Fatalf("p debía volver al primero: %q", title())
	}
	m, _ = send(m, press(tea.KeyPgDown, 0))
	m, _ = send(m, press(tea.KeyPgUp, 0))
	checkDims(t, m, 140, 30, "navegación")
}

func TestDiffRevertFileWithConfirmationFromCenter(t *testing.T) {
	m, _, target, original := diffLayout(t, 140, 30)
	m, cmd := send(m, press('r', tea.ModCtrl))
	m = feed(m, cmd)

	m, cmd = send(m, press('r', tea.ModAlt))
	if cmd != nil {
		t.Fatal("la primera pulsación solo pide confirmación")
	}
	if !strings.Contains(centerColumn(m), "repite alt+r") {
		t.Fatalf("la confirmación debía verse en el centro:\n%s", centerColumn(m))
	}
	if b, _ := os.ReadFile(target); string(b) == original {
		t.Fatal("no debe revertir sin confirmar")
	}
	// Otra tecla desarma la confirmación.
	m, _ = send(m, text("n"))
	if strings.Contains(centerColumn(m), "repite alt+r") {
		t.Fatal("otra tecla debía desarmar la confirmación")
	}
	m, _ = send(m, press('r', tea.ModAlt))
	m, cmd = send(m, press('r', tea.ModAlt))
	if cmd == nil {
		t.Fatal("la segunda pulsación debía revertir")
	}
	m = feed(m, cmd)
	if b, _ := os.ReadFile(target); string(b) != original {
		t.Fatalf("el archivo no volvió al checkpoint:\n%s", b)
	}
	if m.diffShown() {
		t.Fatal("el diff debía cerrarse tras revertir")
	}
	if !m.ed.Dirty() || !strings.HasSuffix(m.ed.Path(), "notes.txt") {
		t.Fatal("el editor debía conservar su archivo y su estado sucio")
	}
}

func TestDiffRevertTurnAndAgentAskConfirmation(t *testing.T) {
	for _, tc := range []struct {
		key  rune
		want string
	}{{'t', "repite alt+t"}, {'a', "repite alt+a"}} {
		m, _, target, original := diffLayout(t, 140, 30)
		m, cmd := send(m, press('r', tea.ModCtrl))
		m = feed(m, cmd)
		m, _ = send(m, press(tc.key, tea.ModAlt))
		if !strings.Contains(centerColumn(m), tc.want) {
			t.Fatalf("%c: falta la confirmación:\n%s", tc.key, centerColumn(m))
		}
		m, cmd = send(m, press(tc.key, tea.ModAlt))
		feed(m, cmd)
		if b, _ := os.ReadFile(target); string(b) != original {
			t.Fatalf("%c: no revirtió", tc.key)
		}
	}
}

func TestDiffWithoutEditsOnlyAnnounces(t *testing.T) {
	m := newLayout(t, Config{}, 120, 24)
	m, _ = send(m, press('r', tea.ModCtrl))
	if m.diffShown() || !strings.Contains(m.msg, "no hay ediciones") {
		t.Fatalf("diffShown=%v msg=%q", m.diffShown(), m.msg)
	}
}

func TestOpeningAFileClosesTheDiff(t *testing.T) {
	m, root, _, _ := diffLayout(t, 140, 30)
	m, cmd := send(m, press('r', tea.ModCtrl))
	m = feed(m, cmd)
	m.pendingOpen = filepath.Join(root, "README.md") // confirma descartar el cambio sucio
	m.openFile(filepath.Join(root, "README.md"))
	if m.diffShown() {
		t.Fatal("abrir otro archivo debía cerrar el diff")
	}
}

func TestDiffResizeNeverOverflows(t *testing.T) {
	m, _, _, _ := diffLayout(t, 140, 30)
	m, cmd := send(m, press('r', tea.ModCtrl))
	m = feed(m, cmd)
	m, _ = send(m, text("n"), press(tea.KeyRight, 0), press(tea.KeyRight, 0))
	sizes := [][2]int{{140, 30}, {100, 24}, {70, 20}, {50, 14}, {40, 12}, {30, 8}, {24, 6}, {20, 4}, {12, 3}, {8, 2}, {5, 1}, {1, 1}, {140, 30}}
	for _, s := range sizes {
		m, _ = send(m, tea.WindowSizeMsg{Width: s[0], Height: s[1]})
		checkDims(t, m, s[0], s[1], fmt.Sprintf("%dx%d", s[0], s[1]))
		m, _ = send(m, text("p"), press(tea.KeyPgDown, 0), press(tea.KeyLeft, 0))
		checkDims(t, m, s[0], s[1], fmt.Sprintf("%dx%d tras teclas", s[0], s[1]))
		// Con el panel de agentes y el árbol plegados, el centro usa todo el ancho.
	}
	m, _ = send(m, press('b', tea.ModCtrl))
	m, _ = send(m, press('g', tea.ModCtrl))
	checkDims(t, m, 140, 30, "plegados")
	if !strings.Contains(centerColumn(m), "+TRES") && !strings.Contains(centerColumn(m), "@@") {
		t.Fatalf("diff no visible:\n%s", screen(m))
	}
}
