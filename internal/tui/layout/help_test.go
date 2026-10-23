package layout

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHelpOverlayTriggerAndRule(t *testing.T) {
	root := project(t)

	// 1. En focusTree (sin escribir): '?' abre la ayuda
	m := newLayout(t, Config{Root: root}, 80, 24)
	if m.focus != focusTree {
		t.Fatalf("foco inicial = %v; want focusTree", m.focus)
	}
	m, _ = send(m, text("?"))
	if !m.helpOpen {
		t.Fatal("'?' en focusTree debió abrir el overlay de ayuda")
	}

	// Esc cierra la ayuda
	m, _ = send(m, press(tea.KeyEsc, 0))
	if m.helpOpen {
		t.Fatal("Esc debió cerrar el overlay de ayuda")
	}

	// 2. En focusEditor sin archivo abierto (!m.hasEd): '?' abre la ayuda
	m = m.focusPanel(focusEditor)
	if m.hasEd {
		t.Fatal("no debe haber editor abierto todavía")
	}
	m, _ = send(m, text("?"))
	if !m.helpOpen {
		t.Fatal("'?' en focusEditor sin archivo abierto debió abrir la ayuda")
	}
	// '?' con la ayuda abierta la cierra
	m, _ = send(m, text("?"))
	if m.helpOpen {
		t.Fatal("'?' con la ayuda abierta debió cerrarla")
	}

	// 3. Abrimos un archivo: focusEditor con archivo abierto (m.hasEd).
	// Regla: '?' NO debe abrir la ayuda porque el usuario está redactando código.
	m.openFile(root + "/src/Main.java")
	if !m.hasEd || m.focus != focusEditor {
		t.Fatalf("hasEd=%v focus=%v", m.hasEd, m.focus)
	}
	m, _ = send(m, text("?"))
	if m.helpOpen {
		t.Fatal("'?' en editor con archivo abierto NO debió abrir la ayuda")
	}

	// F1 en editor con archivo abierto SÍ debe abrir la ayuda (atajo universal)
	m, _ = send(m, tea.KeyPressMsg{Code: tea.KeyF1})
	if !m.helpOpen {
		t.Fatal("F1 en editor debió abrir la ayuda")
	}
	// F1 con la ayuda abierta la cierra
	m, _ = send(m, tea.KeyPressMsg{Code: tea.KeyF1})
	if m.helpOpen {
		t.Fatal("F1 con la ayuda abierta debió cerrarla")
	}

	// 4. En focusChat (entrada de texto activa):
	// Regla: '?' NO debe abrir la ayuda porque el usuario está escribiendo un mensaje.
	m = m.focusPanel(focusChat)
	if m.focus != focusChat || !m.chat.Writing() {
		t.Fatalf("foco chat=%v writing=%v", m.focus, m.chat.Writing())
	}
	m, _ = send(m, text("?"))
	if m.helpOpen {
		t.Fatal("'?' en focusChat NO debió abrir la ayuda (el usuario redacta texto)")
	}

	// F1 en focusChat SÍ debe abrir la ayuda
	m, _ = send(m, tea.KeyPressMsg{Code: tea.KeyF1})
	if !m.helpOpen {
		t.Fatal("F1 en chat debió abrir la ayuda")
	}
	m, _ = send(m, press(tea.KeyEsc, 0))
	if m.helpOpen {
		t.Fatal("Esc debió cerrar la ayuda")
	}
}

func TestHelpOverlayContentAndGroups(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 100, 30)
	m, _ = send(m, tea.KeyPressMsg{Code: tea.KeyF1})
	if !m.helpOpen {
		t.Fatal("F1 debió abrir la ayuda")
	}

	s := screen(m)
	// Verificar que los 5 grupos de contexto requeridos están presentes
	for _, group := range []string{"Navegación", "Editor", "Agentes", "Diffs", "Pipeline"} {
		if !strings.Contains(s, group) {
			t.Errorf("el overlay no contiene el grupo %q:\n%s", group, s)
		}
	}

	// Verificar atajos clave del Keymap
	for _, k := range []string{"tab", "shift+tab", "ctrl+s", "ctrl+t", "ctrl+r", "alt+p"} {
		if !strings.Contains(s, k) {
			t.Errorf("el overlay debe listar la tecla %q:\n%s", k, s)
		}
	}
}

func TestHelpOverlayNoOverflow(t *testing.T) {
	root := project(t)
	sizes := [][2]int{
		{120, 40},
		{80, 24},
		{60, 20},
		{40, 15},
		{25, 8},
		{15, 4},
		{8, 2},
	}

	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		m := newLayout(t, Config{Root: root}, w, h)
		m.helpOpen = true
		checkDims(t, m, w, h, "help overlay")
	}
}

func TestHelpOverlayScroll(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 50, 15) // altura pequeña para forzar scroll
	m.helpOpen = true

	if m.helpScroll != 0 {
		t.Errorf("scroll inicial = %d; want 0", m.helpScroll)
	}

	// Desplazar hacia abajo
	m, _ = send(m, press(tea.KeyDown, 0))
	if m.helpScroll != 1 {
		t.Errorf("scroll tras Down = %d; want 1", m.helpScroll)
	}

	// Page down
	m, _ = send(m, press(tea.KeyPgDown, 0))
	if m.helpScroll <= 1 {
		t.Errorf("scroll tras PgDown = %d; want > 1", m.helpScroll)
	}

	// Desplazar hacia arriba
	m, _ = send(m, press(tea.KeyUp, 0))
	// Page up vuelve a 0
	m, _ = send(m, press(tea.KeyPgUp, 0))
	if m.helpScroll != 0 {
		t.Errorf("scroll tras PgUp = %d; want 0", m.helpScroll)
	}
}

func TestPaletteAgentActionsAndHints(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root}, 120, 30)

	items := m.paletteItems()
	findItem := func(id string) *item {
		for i := range items {
			if items[i].id == id {
				return &items[i]
			}
		}
		return nil
	}

	// 1. Acciones de agentes que deben estar presentes y con su tecla
	expected := []struct {
		id   string
		hint string
	}{
		{cmdAgentPause, "ctrl+o"},
		{cmdAgentStop, "ctrl+x"},
		{cmdDiffFile, "ctrl+r"},
		{cmdRevertFile, "alt+r"},
		{cmdRevertTurn, "alt+t"},
		{cmdRevertAgent, "alt+a"},
	}
	for _, exp := range expected {
		it := findItem(exp.id)
		if it == nil {
			t.Errorf("paleta debe incluir la acción %q", exp.id)
			continue
		}
		if it.hint != exp.hint {
			t.Errorf("acción %q: hint = %q; want %q", exp.id, it.hint, exp.hint)
		}
	}

	// 2. Pistas de 'Ir a...' deben estar rellenas
	focusChecks := []struct {
		id   string
		hint string
	}{
		{cmdFocusTree, "alt+1"},
		{cmdFocusEditor, "alt+2"},
		{cmdFocusChat, "alt+3"},
	}
	for _, fc := range focusChecks {
		it := findItem(fc.id)
		if it == nil {
			t.Errorf("paleta debe incluir %q", fc.id)
			continue
		}
		if it.hint != fc.hint {
			t.Errorf("%q hint = %q; want %q", fc.id, it.hint, fc.hint)
		}
	}

	// 3. Ejecución de comandos de agentes desde la paleta
	// Con chat cerrado, ejecutar una acción de agente debe abrir el chat y relayout
	m.setChatOn(false)
	m, _ = m.runCommand(cmdAgentPause)
	if !m.chatOn() {
		t.Error("runCommand(cmdAgentPause) debió mostrar el panel de chat")
	}

	m.setChatOn(false)
	m, _ = m.runCommand(cmdDiffFile)
	if !m.chatOn() {
		t.Error("runCommand(cmdDiffFile) debió mostrar el panel de chat")
	}

	m.setChatOn(false)
	m, _ = m.runCommand(cmdRevertFile)
	if !m.chatOn() {
		t.Error("runCommand(cmdRevertFile) debió mostrar el panel de chat")
	}

	m.setChatOn(false)
	m, _ = m.runCommand(cmdRevertTurn)
	if !m.chatOn() {
		t.Error("runCommand(cmdRevertTurn) debió mostrar el panel de chat")
	}

	m.setChatOn(false)
	m, _ = m.runCommand(cmdRevertAgent)
	if !m.chatOn() {
		t.Error("runCommand(cmdRevertAgent) debió mostrar el panel de chat")
	}

	m.setChatOn(false)
	m, _ = m.runCommand(cmdAgentStop)
	if !m.chatOn() {
		t.Error("runCommand(cmdAgentStop) debió mostrar el panel de chat")
	}
}
