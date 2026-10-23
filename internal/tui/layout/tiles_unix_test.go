//go:build unix

package layout

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/tui"
	"github.com/dpa23/wari/internal/tui/tile"
)

// shReq es una "CLI" de prueba: escribe, lee una línea y la repite.
func shReq(name string) tui.LaunchTerminalMsg {
	return tui.LaunchTerminalMsg{
		CLI: "claude", Model: "sonnet", Path: "/bin/sh", Dir: "/",
		Args: []string{"-c", `printf 'listo ` + name + `\n'; read x; printf 'eco:%s\n' "$x"; sleep 60`},
	}
}

func launch(t *testing.T, m Model, name string) (Model, *tui.TermAgent, tile.ID) {
	t.Helper()
	m, _ = m.update(shReq(name))
	a, id := m.focusedTerm()
	if a == nil {
		t.Fatalf("el agente nuevo debía quedar con el foco: msg=%q", m.msg)
	}
	return m, a, id
}

func waitScreen(t *testing.T, m Model, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if strings.Contains(plain(m.render()), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("la pantalla no mostró %q:\n%s", want, plain(m.render()))
}

func TestLaunchedTerminalGetsItsOwnTileAndTheKeys(t *testing.T) {
	m := newLayout(t, Config{}, 170, 40)
	m.setChatOn(false)
	m, a, id := launch(t, m, "uno")
	defer m.closeTerms()

	if m.focus != focusTerm || id < idTermBase {
		t.Fatalf("foco=%v id=%d", m.focus, id)
	}
	if r := m.geometry().body(id); r.W < termMinW || r.H < termMinH-1 {
		t.Fatalf("el tile del agente es muy pequeño: %+v", r)
	}
	waitScreen(t, m, "listo uno")
	if title := plain(m.render()); !strings.Contains(title, "claude 1") {
		t.Errorf("el título del tile debe llevar el nombre del agente:\n%s", title)
	}
	if c := m.cursor(); c == nil {
		t.Error("el cursor del terminal debe verse")
	}

	// Lo tecleado llega a la CLI, ctrl+c incluido: WARI no sale.
	m, _ = send(m, text("h"), text("i"), press(tea.KeyEnter, 0))
	waitScreen(t, m, "eco:hi")
	m, cmd := send(m, press('c', tea.ModCtrl))
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("ctrl+c con un terminal en foco no debe salir de WARI")
		}
	}

	// Cerrar los terminales mata a la CLI con su grupo.
	pgid := a.Process().PGID()
	m.closeTerms()
	select {
	case <-a.Process().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("closeTerms debe terminar la CLI")
	}
	if err := syscall.Kill(-pgid, 0); err == nil {
		t.Errorf("el grupo %d sigue vivo", pgid)
	}
}

func TestTilesSplitMoveFocusZoomAndClose(t *testing.T) {
	m := newLayout(t, Config{}, 170, 40)
	m.setChatOn(false)
	m, _, one := launch(t, m, "uno")
	defer m.closeTerms()
	m, _, two := launch(t, m, "dos")

	g := m.geometry()
	if g.body(one).W == 0 || g.body(two).W == 0 {
		t.Fatalf("los dos agentes deben verse a 170 columnas: %+v", g.lay)
	}
	// alt+izquierda pasa el foco al tile vecino, aunque una CLI tenga el foco.
	before := m.ts.tree.Focus()
	for _, k := range []tea.KeyPressMsg{press(tea.KeyLeft, tea.ModAlt), press(tea.KeyUp, tea.ModAlt)} {
		m, _ = send(m, k)
	}
	if m.ts.tree.Focus() == before {
		t.Error("alt+flechas debían mover el foco entre tiles")
	}

	// alt+z maximiza el tile con foco y lo restaura.
	m = m.focusTile(two)
	m, _ = send(m, press('z', tea.ModAlt))
	if g := m.geometry(); g.body(two).W != 170 || g.showEd {
		t.Fatalf("zoom: %+v", g)
	}
	m, _ = send(m, press('z', tea.ModAlt))
	if g := m.geometry(); !g.showEd {
		t.Fatal("el segundo alt+z debía restaurar")
	}

	// Modo tiles: x pide confirmación porque la CLI está viva; la segunda cierra.
	m, _ = send(m, press('h', tea.ModAlt))
	if !m.ts.mode {
		t.Fatal("alt+h debía activar el modo tiles")
	}
	m, _ = send(m, text("x"))
	if !m.ts.tree.Has(two) {
		t.Fatal("la primera x solo debe pedir confirmación")
	}
	m, _ = send(m, text("x"))
	if m.ts.tree.Has(two) || m.ts.terms[two] != nil {
		t.Fatal("la segunda x debía cerrar el tile y su CLI")
	}
	m, _ = send(m, press(tea.KeyEsc, 0))
	if m.ts.mode {
		t.Fatal("esc debía salir del modo tiles")
	}
}

func TestMouseFocusesTilesAndDragsDividers(t *testing.T) {
	m := newLayout(t, Config{}, 170, 40)
	m.setChatOn(false)
	m, _, id := launch(t, m, "uno")
	defer m.closeTerms()
	m = m.focusTile(idEditor)

	g := m.geometry()
	r := g.body(id)
	m, _ = m.update(tea.MouseClickMsg{X: r.X + 2, Y: r.Y + 2, Button: tea.MouseLeft})
	if m.focus != focusTerm || m.ts.tree.Focus() != id {
		t.Fatalf("clic en el agente: foco=%v tile=%d", m.focus, m.ts.tree.Focus())
	}
	m, _ = m.update(tea.MouseClickMsg{X: g.tree.X + 1, Y: g.tree.Y + 1, Button: tea.MouseLeft})
	if m.focus != focusTree {
		t.Fatalf("clic en el árbol: foco=%v", m.focus)
	}

	// Arrastrar el divisor entre el árbol y el resto lo mueve.
	w0 := g.tree.W
	d := g.lay.Dividers[0]
	m, _ = m.update(tea.MouseClickMsg{X: d.Rect.X, Y: d.Rect.Y + 1, Button: tea.MouseLeft})
	m, _ = m.update(tea.MouseMotionMsg{X: d.Rect.X + 10, Y: d.Rect.Y + 1, Button: tea.MouseLeft})
	m, _ = m.update(tea.MouseReleaseMsg{X: d.Rect.X + 10, Y: d.Rect.Y + 1, Button: tea.MouseLeft})
	if w1 := m.geometry().tree.W; w1 <= w0 {
		t.Fatalf("arrastrar el divisor debía ensanchar el árbol: %d -> %d", w0, w1)
	}
	// La rueda sobre una CLI que no pidió el ratón no rompe nada.
	m, _ = m.update(tea.MouseWheelMsg{X: r.X + 2, Y: r.Y + 2, Button: tea.MouseWheelUp})
}

func TestNarrowScreenZoomsTheNewAgent(t *testing.T) {
	m := newLayout(t, Config{}, 90, 28)
	m.setChatOn(false)
	m, _, id := launch(t, m, "uno")
	defer m.closeTerms()
	if g := m.geometry(); g.body(id).W == 0 {
		t.Fatalf("el agente debe verse aunque la pantalla sea estrecha: %+v", g.lay)
	}
}

// gitRepo crea un repositorio con un commit y devuelve su ruta real.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("sin git")
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"}, {"config", "user.name", "t"},
	} {
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "inicial"}} {
		c := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func TestIsolatedAgentGetsAWorktreeAndMergesBack(t *testing.T) {
	root := gitRepo(t)
	m := newLayout(t, Config{Root: root}, 170, 40)
	m.setChatOn(false)
	req := shReq("wt")
	req.Isolated = true
	m, _ = m.update(req)
	a, id := m.focusedTerm()
	if a == nil || a.WT == nil {
		t.Fatalf("el agente debía tener worktree: msg=%q", m.msg)
	}
	defer m.closeTerms()
	if !strings.HasPrefix(a.WT.Path, filepath.Join(root, ".wari", "worktrees")) || a.Process() == nil {
		t.Fatalf("ruta del worktree inesperada: %s", a.WT.Path)
	}
	if !strings.Contains(m.title(id), "⎇ wari/") {
		t.Errorf("el título debe mostrar la rama: %q", m.title(id))
	}

	// El agente edita SU árbol: el proyecto no cambia hasta fusionar.
	if err := os.WriteFile(filepath.Join(a.WT.Path, "nuevo.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "nuevo.txt")); err == nil {
		t.Fatal("el proyecto no debía ver el archivo antes de fusionar")
	}
	// Cerrar con cambios pendientes se niega.
	m = m.closeTile(id)
	if !m.ts.tree.Has(id) || !strings.Contains(m.msg, "sin fusionar") {
		t.Fatalf("cerrar con cambios debía negarse: %q", m.msg)
	}
	// d escribe el parche y lo abre en el editor.
	m = m.worktreeDiff(id)
	if !m.hasEd || !strings.HasSuffix(m.ed.Path(), ".patch") {
		t.Fatalf("d debía abrir el parche: hasEd=%v msg=%q", m.hasEd, m.msg)
	}
	// m pide confirmación y la segunda fusiona.
	m = m.worktreeMerge(id)
	if _, err := os.Stat(filepath.Join(root, "nuevo.txt")); err == nil {
		t.Fatal("la primera m solo debe pedir confirmación")
	}
	m = m.worktreeMerge(id)
	if b, err := os.ReadFile(filepath.Join(root, "nuevo.txt")); err != nil || string(b) != "n\n" {
		t.Fatalf("el proyecto debía recibir el archivo: %v %q (msg=%q)", err, b, m.msg)
	}
	// Ya fusionado no queda nada pendiente: cerrar mata la CLI y borra el worktree.
	path := a.WT.Path
	m = m.closeTile(id)
	m = m.closeTile(id)
	if m.ts.tree.Has(id) {
		t.Fatalf("debía cerrarse: %q", m.msg)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("el worktree sin cambios debía borrarse")
	}
}

func TestIsolatedAgentRefusedOutsideGit(t *testing.T) {
	m := newLayout(t, Config{Root: t.TempDir()}, 170, 40)
	m.setChatOn(false)
	req := shReq("x")
	req.Isolated = true
	m, _ = m.update(req)
	if a, _ := m.focusedTerm(); a != nil {
		t.Fatal("sin git no debía lanzarse el agente aislado")
	}
	if !strings.Contains(m.msg, "no se creó el agente") {
		t.Errorf("debía explicar por qué: %q", m.msg)
	}
}

func TestCloseRefusesWhenChangesCannotBeChecked(t *testing.T) {
	root := gitRepo(t)
	m := newLayout(t, Config{Root: root}, 170, 40)
	m.setChatOn(false)
	req := shReq("x")
	req.Isolated = true
	m, _ = m.update(req)
	a, id := m.focusedTerm()
	if a == nil || a.WT == nil {
		t.Fatalf("sin worktree: %q", m.msg)
	}
	defer m.closeTerms()
	// Se rompe el worktree: Changes falla y no se sabe si hay trabajo.
	if err := os.RemoveAll(a.WT.Path); err != nil {
		t.Fatal(err)
	}
	m = m.closeTile(id)
	m = m.closeTile(id)
	if !m.ts.tree.Has(id) || !strings.Contains(m.msg, "no se pudo comprobar") {
		t.Fatalf("sin poder comprobar los cambios no debía cerrarse: has=%v msg=%q", m.ts.tree.Has(id), m.msg)
	}
}

func TestTabReachesAnAgentThatDoesNotFit(t *testing.T) {
	m := newLayout(t, Config{}, 90, 18)
	m.setChatOn(false)
	m, _, one := launch(t, m, "uno")
	defer m.closeTerms()
	m, _, two := launch(t, m, "dos")
	// A 90 columnas solo cabe uno: el otro está vivo pero sin sitio.
	var hidden tile.ID
	for _, id := range []tile.ID{one, two} {
		if m.geometry().body(id).W == 0 {
			hidden = id
		}
	}
	if hidden == 0 {
		t.Skip("caben los dos; la prueba no aplica")
	}
	for range 6 {
		m = m.moveFocus(1)
		if m.ts.tree.Focus() == hidden && m.geometry().body(hidden).W > 0 {
			return
		}
	}
	t.Fatalf("tab nunca llegó al agente %d sin sitio", hidden)
}

func TestStateTickRearmsOnNextLaunch(t *testing.T) {
	m := newLayout(t, Config{}, 170, 40)
	m.setChatOn(false)
	m, cmd := m.update(shReq("a"))
	if cmd == nil || !m.ts.ticking {
		t.Fatalf("el primer lanzamiento debe armar el tick: cmd=%v ticking=%v", cmd != nil, m.ts.ticking)
	}
	a, id := m.focusedTerm()
	m = m.closeTile(id)
	m = m.closeTile(id) // confirmación: la CLI sigue viva
	_ = a
	m, _ = m.update(tui.PaneTickMsg{}) // sin CLIs vivas el tick se apaga
	if m.ts.ticking {
		t.Fatal("sin CLIs el tick debía apagarse")
	}
	m, cmd = m.update(shReq("b"))
	defer m.closeTerms()
	if cmd == nil || !m.ts.ticking {
		t.Fatal("el siguiente lanzamiento debía volver a armar el tick")
	}
}

func TestDiffOfAnAgentEditingTheProjectLive(t *testing.T) {
	root := gitRepo(t)
	m := newLayout(t, Config{Root: root}, 170, 40)
	m.setChatOn(false)
	m, _, id := launch(t, m, "vivo")
	defer m.closeTerms()
	m = m.worktreeDiff(id)
	if m.hasEd || !strings.Contains(m.msg, "sin cambios") {
		t.Fatalf("sin cambios no debe abrir nada: %q", m.msg)
	}
	// La CLI edita el proyecto: el diff lo muestra contra HEAD.
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\ncambio del agente\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = m.worktreeDiff(id)
	if !m.hasEd || !strings.HasSuffix(m.ed.Path(), "proyecto.patch") {
		t.Fatalf("d debía abrir el parche del proyecto: %q", m.msg)
	}
	if b, err := os.ReadFile(m.ed.Path()); err != nil || !strings.Contains(string(b), "+cambio del agente") {
		t.Fatalf("el parche no incluye la edición: %v %q", err, b)
	}
}
