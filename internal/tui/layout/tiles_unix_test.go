//go:build unix

package layout

import (
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
