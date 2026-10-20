//go:build unix

package tui

import (
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
)

func waitBody(t *testing.T, h *phar, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		body = ansiRe.ReplaceAllString(h.m.Body(), "")
		if strings.Contains(body, want) {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("el panel no mostró %q:\n%s", want, body)
	return ""
}

func TestTerminalModeRunsTheRealCLIInsideTheAgent(t *testing.T) {
	h, _ := formHarness(t, agent.Task{Dir: t.TempDir()})
	info := h.m.cfg.Infos["claude"]
	info.Launch = func(task agent.Task, prompt string) (string, []string, error) {
		return "/bin/sh", []string{"-c", `printf 'listo modelo=%s\n' "` + task.Model + `"; read x; printf 'eco:%s\n' "$x"; echo $$ > "` + task.Dir + `/pid"; sleep 60`}, nil
	}
	h.m.cfg.Infos["claude"] = info

	v := h.m.cur()
	m, _ := h.m.openForm(v, "", false)
	h.m = m
	h.m.form.model = 0 // Sonnet
	m, cmd := h.m.formConfirm()
	h.m = m
	if h.m.form != nil || v.pane == nil || !h.m.TerminalActive() {
		t.Fatalf("debía quedar un terminal: form=%v pane=%v", h.m.form, v.pane)
	}
	if cmd == nil {
		t.Error("el primer terminal debe armar la escucha de pantalla")
	}
	defer h.m.ClosePanes()

	body := waitBody(t, h, "listo modelo=sonnet")
	if !strings.Contains(body, "RUNNING") {
		t.Errorf("la lista debe mostrar el estado del terminal:\n%s", body)
	}
	if c := h.m.Cursor(); c == nil {
		t.Error("el cursor del terminal debe verse")
	}

	// Lo tecleado llega a la CLI, no a la entrada de WARI.
	for _, r := range "hi" {
		h.update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	h.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitBody(t, h, "eco:hi")

	// ctrl+c es de la CLI: WARI no sale.
	h.update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if h.m.quitting {
		t.Error("ctrl+c con un terminal en foco no debe salir de WARI")
	}

	// Cerrar los terminales mata a la CLI con su grupo.
	pidPath := v.pane.Process().PGID()
	h.m.ClosePanes()
	select {
	case <-v.pane.Process().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("ClosePanes debe terminar la CLI")
	}
	if err := syscall.Kill(-pidPath, 0); err == nil {
		t.Errorf("el grupo %d sigue vivo", pidPath)
	}
}
