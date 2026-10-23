package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/proc"
	"github.com/dpa23/wari/internal/term"
	"github.com/dpa23/wari/internal/tui/icons"
	"github.com/dpa23/wari/internal/tui/theme"
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

// paneTickMsg repinta las pestañas: el estado de una CLI cambia por el paso del
// tiempo (deja de escribir), no solo cuando escribe.
type paneTickMsg struct{}

const paneTick = 700 * time.Millisecond

func tickPanes() tea.Cmd {
	return tea.Tick(paneTick, func(time.Time) tea.Msg { return paneTickMsg{} })
}

// anyPaneLive indica que algún terminal sigue corriendo.
func (m Panel) anyPaneLive() bool {
	for _, v := range m.views {
		if v.pane != nil && v.pane.Activity() != term.Exited {
			return true
		}
	}
	return false
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
		return tea.Batch(listenPane(m.paneCh), tickPanes()), nil
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

// terminalActivity traduce lo que el terminal sabe de la CLI al vocabulario del
// panel: la palabra y el estilo del chip.
func (v *agentView) terminalActivity() (word string, st lipgloss.Style) {
	switch v.pane.Activity() {
	case term.Working:
		return "TRABAJA", lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	case term.Exited:
		return "FIN", lipgloss.NewStyle().Faint(true)
	}
	return "ESPERA", lipgloss.NewStyle().Foreground(theme.AdaptiveColor{
		Light: lipgloss.Color("#1d4ed8"),
		Dark:  lipgloss.Color("#93c5fd"),
	})
}

// tabBar es la fila única que sustituye a la lista de agentes cuando el
// seleccionado es una CLI en terminal: un terminal necesita las filas para sí.
// Cada pestaña lleva su número, su estado y su nombre; la seleccionada va en
// negrita y subrayada. Si no caben todas, los nombres se acortan.
func (m Panel) tabBar() string {
	w := max(m.width, 1)
	n := len(m.views)
	// Cada pestaña gasta número, estado y dos separadores; "+" gasta 3.
	nameW := max((w-3-n*7)/max(n, 1), 3)
	var b strings.Builder
	used := 0
	for i, v := range m.views {
		mark, st := "·", lipgloss.NewStyle().Faint(true)
		if v.pane != nil {
			word, wst := v.terminalActivity()
			mark, st = stateMark(word), wst
		} else if sw, sst := stateBadge(v.state()); sw != "" {
			mark, st = stateMark(sw), sst
		}
		// Sin anidar estilos: lipgloss estropea las secuencias ANSI de un texto
		// ya coloreado cuando lo subraya.
		tab := lipgloss.NewStyle().Faint(i != m.sel).Bold(i == m.sel).Underline(i == m.sel)
		cell := tab.Render(fmt.Sprintf(" %d ", i+1)) + st.Render(mark) +
			tab.Render(" "+truncate(cmpOr(v.name, v.cli), nameW)+" ")
		if used+lipgloss.Width(cell) > w-3 {
			break
		}
		used += lipgloss.Width(cell)
		b.WriteString(cell)
	}
	b.WriteString(lipgloss.NewStyle().Faint(true).Render(" + "))
	return lipgloss.NewStyle().MaxWidth(w).Render(b.String())
}

// stateMark es el glifo de un estado, del juego de iconos elegido.
func stateMark(word string) string {
	g := icons.Ready
	switch word {
	case "TRABAJA", "RUN":
		g = icons.Working
	case "ESPERA", "IDLE":
		g = icons.Waiting
	case "FIN", "STOP":
		g = icons.Exited
	case "ERR":
		g = icons.Failed
	case "$!":
		g = icons.Budget
	case "PAU":
		g = icons.Paused
	case "Q":
		g = icons.Queued
	case "NEW":
		g = icons.New
	}
	if s := icons.State(string(g)); s != "" {
		return s
	}
	return "·"
}

// terminalLine es la fila de la lista de un agente en terminal cuando el
// seleccionado es un agente de chat: marca, nombre, estado, CLI y título.
func (m Panel) terminalLine(i int) string {
	v := m.views[i]
	marker := "  "
	if i == m.sel {
		marker = "▸ "
	}
	word, st := v.terminalActivity()
	line := fmt.Sprintf("%s%-10s %s %s", marker, truncate(v.name, 10), st.Render(fmt.Sprintf("%-7s", word)), v.cli)
	if t := strings.TrimSpace(v.pane.Title()); t != "" {
		line += " · " + t
	}
	return lipgloss.NewStyle().MaxWidth(max(m.width, 1)).Render(line)
}
