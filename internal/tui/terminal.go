package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/proc"
	"github.com/dpa23/wari/internal/term"
)

// Modo terminal: el agente es la CLI interactiva de verdad dentro de un
// terminal virtual (internal/term). WARI no interpreta su salida: los comandos
// "/", los permisos, las skills y el modelo son de la CLI. El panel solo le
// presta la pantalla, le reenvía teclas y pegado, y la mata con su grupo al
// salir.

// paneChangedMsg avisa de que la pantalla de algún terminal cambió.
type paneChangedMsg struct{}

// listenPane espera un aviso de pantalla cambiada y se vuelve a armar tras
// manejarlo.
func listenPane(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return paneChangedMsg{}
	}
}

// terminalLaunchEnv es el entorno de la CLI: el del usuario, con el tipo de
// terminal que sabe emular x/vt.
func terminalLaunchEnv() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TERM=") && !strings.HasPrefix(kv, "COLORTERM=") {
			env = append(env, kv)
		}
	}
	return append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
}

// terminalSize es el espacio del terminal bajo la lista de agentes.
func (m Panel) terminalSize() (cols, rows int) {
	return max(m.width, 1), max(m.height-m.pipeH-m.listH, 1)
}

// startTerminal lanza la CLI del agente en un terminal. prompt, si no está
// vacío, es el primer mensaje.
func (m Panel) startTerminal(v *agentView, prompt string) (tea.Cmd, error) {
	info, ok := m.cfg.Infos[v.cli]
	if !ok || info.Launch == nil {
		return nil, fmt.Errorf("%s no tiene modo terminal", v.cli)
	}
	task := agent.Task{Dir: m.cfg.Task.Dir, Model: v.taskModel, Effort: v.effort, Permissions: m.effectivePermissions(v)}
	if v.role == roleOrchestrator {
		prompt = orchestratorPreamble + prompt
	}
	path, args, err := info.Launch(task, prompt)
	if err != nil {
		return nil, err
	}
	cols, rows := m.terminalSize()
	notify := func() {
		select {
		case m.paneCh <- struct{}{}:
		default:
		}
	}
	pane, err := term.Start(context.Background(), proc.Spec{Path: path, Args: args, Dir: m.cfg.Task.Dir, Env: terminalLaunchEnv()}, cols, rows, notify)
	if err != nil {
		return nil, err
	}
	v.pane = pane
	// Un solo Cmd espera los avisos de todos los terminales.
	if !*m.paneListening {
		*m.paneListening = true
		return listenPane(m.paneCh), nil
	}
	return nil, nil
}

// ClosePanes mata las CLIs de todos los terminales con su grupo de procesos.
// Es idempotente y lo llama quien sale del programa, por la vía que sea.
func (m Panel) ClosePanes() {
	for _, v := range m.views {
		if v.pane != nil {
			_ = v.pane.Close()
		}
	}
}

// TerminalActive indica que el agente seleccionado es una CLI en terminal: el
// layout le deja pasar las teclas en vez de atenderlas él.
func (m Panel) TerminalActive() bool { return m.cur().pane != nil }

// paneRows dibuja el terminal del agente seleccionado en exactamente h filas
// de ancho w.
func (m Panel) paneRows(v *agentView, w, h int) []string {
	lines := v.pane.Lines()
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		if lipgloss.Width(l) > w {
			l = lipgloss.NewStyle().MaxWidth(w).Render(l)
		}
		out = append(out, l)
	}
	return out
}

// terminalKey reenvía una tecla al terminal. Solo se atienden aquí las que
// cambian de agente o crean uno; todo lo demás (incluido Ctrl+C y Esc) es de
// la CLI.
func (m Panel) terminalKey(msg tea.KeyPressMsg) (Panel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.AgentNew):
		return m.newAgent()
	case key.Matches(msg, m.keys.AgentNext):
		m.selectView(m.sel + 1)
		return m, nil
	case key.Matches(msg, m.keys.AgentPrev):
		m.selectView(m.sel - 1)
		return m, nil
	}
	m.cur().pane.SendKey(msg)
	return m, nil
}

// terminalState es el estado de un agente en terminal para la lista.
func (v *agentView) terminalState() string {
	select {
	case <-v.pane.Done():
		return "EXITED"
	default:
		return "RUNNING"
	}
}

// terminalLine es la fila de la lista de un agente en terminal: su estado, la
// CLI y el título que la CLI puso al terminal.
func (m Panel) terminalLine(i int) string {
	v := m.views[i]
	marker := "  "
	if i == m.sel {
		marker = "▸ "
	}
	line := fmt.Sprintf("%s%-10s %-8s %s", marker, truncate(v.name, 10), v.terminalState(), v.cli)
	if t := strings.TrimSpace(v.pane.Title()); t != "" {
		line += " · " + t
	}
	st := lipgloss.NewStyle().MaxWidth(max(m.width, 1))
	if i == m.sel {
		st = st.Bold(true)
	}
	return st.Render(line)
}
