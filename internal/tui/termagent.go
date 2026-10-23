package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/proc"
	"github.com/dpa23/wari/internal/term"
	"github.com/dpa23/wari/internal/tui/icons"
	"github.com/dpa23/wari/internal/tui/theme"
	"github.com/dpa23/wari/internal/workspace/worktree"
)

// Un agente en terminal es la CLI interactiva de verdad dentro de un terminal
// virtual (internal/term). WARI no interpreta su salida: los comandos "/", los
// permisos, las skills y el modelo son de la CLI. A diferencia del panel de
// chat, un TermAgent no vive dentro de ningún panel: el layout lo coloca en el
// tile que quiera y le presta la pantalla, las teclas y el ratón.

// LaunchTerminalMsg pide al layout lanzar una CLI en un terminal nuevo. La
// emite el formulario del panel cuando el modo elegido es "terminal".
type LaunchTerminalMsg struct {
	// CLI, Model, Effort y Role son lo que eligió el formulario; Path y Args,
	// la línea de lanzamiento que armó el adaptador.
	CLI, Model, Effort, Role string
	Path                     string
	Args                     []string
	Dir                      string
	// Isolated pide un worktree propio para el agente; el layout lo crea.
	Isolated bool
}

// TermAgent es una CLI corriendo en un terminal.
type TermAgent struct {
	Name, CLI, Model, Effort, Role string
	// WT es el worktree propio del agente; nil si edita el proyecto.
	WT   *worktree.Info
	pane *term.Pane
}

// StartTermAgent lanza la CLI de req con el tamaño dado. notify se llama,
// agrupado, cada vez que la pantalla cambia.
func StartTermAgent(req LaunchTerminalMsg, name string, cols, rows int, notify func()) (*TermAgent, error) {
	pane, err := term.Start(context.Background(), proc.Spec{Path: req.Path, Args: req.Args, Dir: req.Dir, Env: terminalLaunchEnv()}, cols, rows, notify)
	if err != nil {
		return nil, err
	}
	return &TermAgent{Name: name, CLI: req.CLI, Model: req.Model, Effort: req.Effort, Role: req.Role, pane: pane}, nil
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

// Close mata a la CLI con su grupo de procesos. Es idempotente.
func (a *TermAgent) Close() error { return a.pane.Close() }

// Done se cierra cuando la CLI terminó.
func (a *TermAgent) Done() <-chan struct{} { return a.pane.Done() }

// Resize cambia el tamaño del terminal; la CLI redibuja sola.
func (a *TermAgent) Resize(cols, rows int) { a.pane.Resize(cols, rows) }

// SendKey reenvía una tecla a la CLI. Todo es suyo, incluido Ctrl+C y Esc.
func (a *TermAgent) SendKey(k tea.KeyPressMsg) { a.pane.SendKey(k) }

// Paste reenvía un pegado a la CLI.
func (a *TermAgent) Paste(text string) { a.pane.Paste(text) }

// SendText escribe texto en la CLI como si se tecleara.
func (a *TermAgent) SendText(text string) { a.pane.SendText(text) }

// Title es el título que la CLI puso al terminal.
func (a *TermAgent) Title() string { return strings.TrimSpace(a.pane.Title()) }

// Activity estima qué hace la CLI (ver term.Pane.Activity).
func (a *TermAgent) Activity() term.Activity { return a.pane.Activity() }

// Process expone el proceso de la CLI (grupo, pausa, parada).
func (a *TermAgent) Process() *proc.Process { return a.pane.Process() }

// Cursor es la posición del cursor dentro del terminal.
func (a *TermAgent) Cursor() (x, y int, visible bool) { return a.pane.Cursor() }

// Rows devuelve exactamente h filas de ancho w con la pantalla de la CLI.
func (a *TermAgent) Rows(w, h int) []string {
	lines := a.pane.Lines()
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

// Chip es la palabra y el estilo del estado del agente.
func (a *TermAgent) Chip() (word string, st lipgloss.Style) {
	switch a.pane.Activity() {
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

// Glyph es el icono del estado en el juego de iconos elegido.
func (a *TermAgent) Glyph() string {
	word, _ := a.Chip()
	return stateMark(word)
}

// Label es "CLI · modelo" para el título de su tile.
func (a *TermAgent) Label() string {
	s := a.CLI
	if a.Model != "" {
		s += "·" + a.Model
	}
	if a.Effort != "" {
		s += "·" + a.Effort
	}
	return s
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

// PaneTickMsg repinta los estados: el de una CLI cambia por el paso del tiempo
// (deja de escribir), no solo cuando escribe.
type PaneTickMsg struct{}

// PaneTick es el periodo del repintado de estados.
const PaneTick = 700 * time.Millisecond

// TickPanes arma el siguiente PaneTickMsg.
func TickPanes() tea.Cmd {
	return tea.Tick(PaneTick, func(time.Time) tea.Msg { return PaneTickMsg{} })
}

// launchRequest arma la línea de lanzamiento del agente configurado en v.
func (m Panel) launchRequest(v *agentView, prompt string) (LaunchTerminalMsg, error) {
	info, ok := m.cfg.Infos[v.cli]
	if !ok || info.Launch == nil {
		return LaunchTerminalMsg{}, fmt.Errorf("%s no tiene modo terminal", v.cli)
	}
	task := agent.Task{Dir: m.cfg.Task.Dir, Model: v.taskModel, Effort: v.effort, Permissions: m.effectivePermissions(v)}
	if v.role == roleOrchestrator {
		prompt = orchestratorPreamble + prompt
	}
	path, args, err := info.Launch(task, prompt)
	if err != nil {
		return LaunchTerminalMsg{}, err
	}
	return LaunchTerminalMsg{CLI: v.cli, Model: v.taskModel, Effort: v.effort, Role: v.role, Path: path, Args: args, Dir: m.cfg.Task.Dir}, nil
}

// Wheel entrega la rueda del ratón a la CLI (solo si ella pidió el ratón).
func (a *TermAgent) Wheel(x, y int, up bool) { a.pane.Wheel(x, y, up) }
