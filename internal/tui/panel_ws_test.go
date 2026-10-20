package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/workspace/checkpoint"
)

var (
	keyCtrlR = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	keyAltR  = tea.KeyPressMsg{Code: 'r', Mod: tea.ModAlt}
	keyAltT  = tea.KeyPressMsg{Code: 't', Mod: tea.ModAlt}
	keyAltA  = tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt}
)

// wsHarness es un phar con espacio de trabajo real (archivos temporales y
// checkpoints).
type wsHarness struct {
	*phar
	root  string
	store *checkpoint.Store
}

func newWsHarness(t *testing.T, perms agent.PermissionMode) *wsHarness {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	h := &phar{t: t, sup: orch.New(2)}
	if err := h.sup.AttachWorkspace(orch.Workspace{Root: root, Store: store, RunID: "t-"}); err != nil {
		t.Fatal(err)
	}
	h.m = NewPanel(Config{
		Supervisor: h.sup,
		NewAgent: func() agent.Agent {
			f := newPfake()
			h.made = append(h.made, f)
			return f
		},
		Task: agent.Task{Dir: root, Permissions: perms},
	})
	h.m.spinning = true
	h.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	t.Cleanup(h.sup.Close)
	return &wsHarness{phar: h, root: root, store: store}
}

func (w *wsHarness) path(rel string) string { return filepath.Join(w.root, rel) }

func (w *wsHarness) write(rel, body string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(w.path(rel)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.path(rel), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wsHarness) hash(rel string) string {
	w.t.Helper()
	h, err := checkpoint.SHA256(w.path(rel))
	if err != nil {
		w.t.Fatal(err)
	}
	return h
}

// edit hace que el agente i proponga editar rel y, cuando el panel ya lo
// mostró, escribe el contenido nuevo.
func (w *wsHarness) edit(i int, rel, body string) {
	w.t.Helper()
	w.made[i].emit(agent.FileEditProposed{Tool: "Edit", Path: w.path(rel)})
	w.until("edición de "+rel, func() bool {
		return strings.Contains(w.m.views[i].entries[len(w.m.views[i].entries)-1].text, rel)
	})
	w.write(rel, body)
}

// diffScreen es lo que el layout dibujaría en el panel central: el diff.
func (w *wsHarness) diffScreen() string {
	return ansiRe.ReplaceAllString(strings.Join(w.m.DiffLines(100, 30), "\n"), "")
}

func (w *wsHarness) pressAll(ks ...tea.KeyPressMsg) {
	for _, k := range ks {
		w.press(k)
	}
}

func TestEditShowsRelativePathAndDiffHint(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAsk)
	w.write("src/A.java", "uno\ndos\n")
	w.say("hola")
	w.untilState(0, "running")
	w.wantNoView("edita sin preguntar")
	w.edit(0, "src/A.java", "uno\nDOS\n")
	w.wantView("✎ src/A.java")
	w.wantNoView("✎ " + w.root)
	w.wantView("ctrl+r ver diff")
}

func TestDiffViewShowsChangesAndCycles(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAsk)
	w.write("a.txt", "a-vieja\n")
	w.write("b.txt", "b-vieja\n")
	w.say("hola")
	w.untilState(0, "running")
	w.edit(0, "a.txt", "a-nueva\n")
	w.edit(0, "b.txt", "b-nueva\n")

	w.press(keyCtrlR) // abre el de la última edición
	w.until("diff de b", func() bool { return strings.Contains(w.diffScreen(), "+b-nueva") })
	if !strings.Contains(w.diffScreen(), "-b-vieja") || !strings.Contains(w.diffScreen(), "@@ -1 +1 @@") {
		t.Fatalf("diff incompleto:\n%s", w.diffScreen())
	}
	if got := w.m.DiffTitle(); !strings.Contains(got, "diff b.txt · turno 1") {
		t.Fatalf("título = %q", got)
	}
	w.wantView("diff b.txt (turno 1)")
	// El historial sigue visible: el diff va al panel central.
	w.wantView("✎ a.txt")
	w.wantNoView("+b-nueva")

	w.press(keyCtrlR) // siguiente (da la vuelta)
	w.until("diff de a", func() bool { return strings.Contains(w.diffScreen(), "+a-nueva") })
	if !strings.Contains(w.diffScreen(), "-a-vieja") {
		t.Fatalf("diff de a incompleto:\n%s", w.diffScreen())
	}

	w.press(keyEsc)
	if w.m.DiffOpen() {
		t.Fatal("Esc debía cerrar el diff")
	}
	w.wantView("✎ a.txt")
	if got := w.made[0].get(&w.made[0].cancels); got != 0 {
		t.Fatal("Esc con el diff abierto solo lo cierra, no detiene al agente")
	}
}

func TestDiffWithoutEditsSaysSo(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAsk)
	w.say("hola")
	w.untilState(0, "running")
	w.press(keyCtrlR)
	w.wantView("no hay ediciones que mostrar")
}

func TestRevertNeedsConfirmationAndRestoresIdentical(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAsk)
	w.write("a.txt", "original\n")
	w.write("b.txt", "otro\n")
	hashA, hashB := w.hash("a.txt"), w.hash("b.txt")
	w.say("hola")
	w.untilState(0, "running")
	w.edit(0, "a.txt", "cambiado\n")
	w.edit(0, "b.txt", "cambiado\n")

	// Con el agente corriendo no se revierte.
	w.pressAll(keyAltT, keyAltT)
	w.until("error", func() bool { return strings.Contains(w.view(), "no se pudo revertir") })
	w.wantView("páusalo o detenlo")

	w.press(keyCtrlO)
	w.untilState(0, "paused")

	// Una pulsación sola abre el diálogo modal de confirmación.
	w.press(keyAltT)
	w.wantView("[Enter] Confirmar")
	w.wantView("a.txt, b.txt")
	if w.hash("a.txt") == hashA {
		t.Fatal("la primera pulsación no debe revertir")
	}
	// Otra tecla (no volátil: el diálogo sigue activo).
	w.typeText("x")
	w.wantView("[Enter] Confirmar")
	// Confirmar con Enter ejecuta la reversión.
	w.press(keyEnter)
	w.until("revertido", func() bool { return w.hash("a.txt") == hashA })
	if w.hash("b.txt") != hashB {
		t.Fatal("b.txt debe quedar idéntico al checkpoint")
	}
	w.until("listo", func() bool { return strings.Contains(w.view(), "listo") })
}

func TestRevertFileUsesTheDiffSelection(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAsk)
	w.write("a.txt", "A0\n")
	w.write("b.txt", "B0\n")
	hashA := w.hash("a.txt")
	w.say("hola")
	w.untilState(0, "running")
	w.edit(0, "a.txt", "A1\n")
	w.edit(0, "b.txt", "B1\n")
	w.press(keyCtrlO)
	w.untilState(0, "paused")

	w.press(keyCtrlR) // b
	w.press(keyCtrlR) // a
	w.until("diff de a", func() bool { return strings.Contains(w.diffScreen(), "+A1") })
	w.pressAll(keyAltR, keyAltR)
	w.until("a revertido", func() bool { return w.hash("a.txt") == hashA })
	if b, _ := os.ReadFile(w.path("b.txt")); string(b) != "B1\n" {
		t.Fatalf("b.txt no debía tocarse: %q", b)
	}
	// El diff se cierra tras revertir.
	w.until("diff cerrado", func() bool { return !w.m.DiffOpen() })
}

func TestRevertAgentRestoresEverythingAndDeletesNewFiles(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAsk)
	w.write("a.txt", "A0\n")
	hashA := w.hash("a.txt")
	w.say("hola")
	w.untilState(0, "running")
	w.edit(0, "a.txt", "A1\n")
	w.edit(0, "nuevo.txt", "creado\n")
	w.press(keyCtrlO)
	w.untilState(0, "paused")
	w.pressAll(keyAltA, keyAltA)
	w.until("revertido", func() bool {
		_, err := os.Stat(w.path("nuevo.txt"))
		return os.IsNotExist(err) && w.hash("a.txt") == hashA
	})
}

func TestConflictShowsAlertAndPausesSecondAgent(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAcceptEdits)
	w.write("x.txt", "X0\n")
	w.wantNoView("choque")
	w.say("uno")
	w.press(keyCtrlT)
	w.say("dos")
	w.untilState(0, "running")
	w.untilState(1, "running")
	w.wantView("edita sin preguntar")

	w.edit(0, "x.txt", "X-por-1\n")
	w.made[1].emit(agent.FileEditProposed{Tool: "Edit", Path: w.path("x.txt")})
	w.untilState(1, "paused")
	if w.m.Alert() != "choque: agente 2 quiere editar x.txt, reclamado por agente 1" {
		t.Fatalf("alerta = %q", w.m.Alert())
	}
	w.wantView("⚠ choque: agente 2 quiere editar x.txt, reclamado por agente 1")
	w.wantView("ctrl+o reanuda y le da el archivo")
	w.wantView("0 tok ⚠ choque") // la fila de la lista

	// El dueño ve que alguien quiso su archivo.
	w.press(keyAltUp)
	w.wantView("agente 2 quiso editar x.txt, que tienes reclamado")
	if b, _ := os.ReadFile(w.path("x.txt")); string(b) != "X-por-1\n" {
		t.Fatal("el segundo agente no debe haber editado")
	}

	// Reanudar al segundo resuelve el choque.
	w.press(keyAltDown)
	w.press(keyCtrlO)
	w.untilState(1, "running")
	w.until("sin alerta", func() bool { return w.m.Alert() == "" })
	w.wantView("agente 2 toma x.txt, que era de agente 1")
}

func TestRevertWithoutSupervisorIsHarmless(t *testing.T) {
	m := NewPanel(Config{})
	for _, k := range []tea.KeyPressMsg{keyCtrlR, keyAltR, keyAltT, keyAltA} {
		var cmd tea.Cmd
		m, cmd = m.Handle(k)
		if cmd != nil {
			t.Fatal("sin supervisor no hay nada que hacer")
		}
	}
}

func TestDiffTimesOutGracefullyOnError(t *testing.T) {
	w := newWsHarness(t, agent.PermissionAsk)
	w.write("a.txt", "A0\n")
	w.say("hola")
	w.untilState(0, "running")
	w.edit(0, "a.txt", "A1\n")
	// Se borra el checkpoint a mano: el diff debe mostrar el error, no romper.
	if err := os.RemoveAll(filepath.Join(w.root, ".wari")); err != nil {
		t.Fatal(err)
	}
	w.press(keyCtrlR)
	w.until("error de diff", func() bool { return strings.Contains(w.diffScreen(), "no se pudo obtener el diff") })
}

func TestConflictAssistedResolutionResumeAndStop(t *testing.T) {
	keyF6 := tea.KeyPressMsg{Code: tea.KeyF6}
	keyEsc := tea.KeyPressMsg{Code: tea.KeyEscape}
	key1 := tea.KeyPressMsg{Text: "1"}
	key2 := tea.KeyPressMsg{Text: "2"}

	t.Run("opcion 1 ceder y reanudar", func(t *testing.T) {
		w := newWsHarness(t, agent.PermissionAcceptEdits)
		w.write("x.txt", "X0\n")
		w.say("uno")
		w.press(keyCtrlT)
		w.say("dos")
		w.untilState(0, "running")
		w.untilState(1, "running")

		w.edit(0, "x.txt", "X-por-1\n")
		w.made[1].emit(agent.FileEditProposed{Tool: "Edit", Path: w.path("x.txt")})
		w.untilState(1, "paused")
		if w.m.Alert() == "" {
			t.Fatal("debía haber alerta de choque")
		}

		// Cambiar al agente 1 para verificar que FocusConflict / F6 enfoca al agente 2 pausado
		w.press(keyAltUp)
		if w.m.cur().id == w.m.alert.id {
			t.Fatal("el agente 1 no debía ser el pausado en choque")
		}

		// Pulsar F6 activa la resolución asistida
		w.press(keyF6)
		if !w.m.ConflictActive() {
			t.Fatal("debía estar activo el modo resolución de choque")
		}
		if w.m.cur().id != w.m.alert.id {
			t.Fatalf("debía enfocar al agente pausado (%d), pero está en %d", w.m.alert.id, w.m.cur().id)
		}
		w.wantView("[1] ceder archivo (reanudar)")
		w.wantView("[2] detener agente")

		// Probar que Esc cancela el diálogo asistido sin parar al agente
		w.press(keyEsc)
		if w.m.ConflictActive() {
			t.Fatal("Esc debía cancelar el modo de resolución asistida")
		}
		if w.m.Alert() == "" {
			t.Fatal("la alerta debía seguir viva")
		}

		// Volver a activar y pulsar 1 (ceder archivo / reanudar)
		w.press(keyF6)
		if !w.m.ConflictActive() {
			t.Fatal("debía volver a activarse con F6")
		}
		w.press(key1)
		w.untilState(1, "running")
		w.until("sin alerta", func() bool { return w.m.Alert() == "" })
		if w.m.ConflictActive() {
			t.Fatal("resolución debía desactivar conflictActive")
		}
		w.wantView("agente 2 toma x.txt, que era de agente 1")
	})

	t.Run("opcion 2 detener agente", func(t *testing.T) {
		w := newWsHarness(t, agent.PermissionAcceptEdits)
		w.write("y.txt", "Y0\n")
		w.say("uno")
		w.press(keyCtrlT)
		w.say("dos")
		w.untilState(0, "running")
		w.untilState(1, "running")

		w.edit(0, "y.txt", "Y-por-1\n")
		w.made[1].emit(agent.FileEditProposed{Tool: "Edit", Path: w.path("y.txt")})
		w.untilState(1, "paused")

		w.press(keyF6)
		if !w.m.ConflictActive() {
			t.Fatal("debía estar activo el modo resolución de choque")
		}
		w.press(key2)
		w.untilState(1, "stopped")
		w.until("sin alerta", func() bool { return w.m.Alert() == "" })
		if w.m.ConflictActive() {
			t.Fatal("detener debía desactivar conflictActive")
		}
	})
}
