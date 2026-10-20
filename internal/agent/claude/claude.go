// Package claude es el adaptador de la CLI oficial `claude` (Claude Code).
// Cada turno es un proceso `claude -p` en modo stream-json; los turnos
// siguientes reanudan la sesión con --resume. La maquinaria de turnos vive en
// internal/agent/turn; aquí solo está lo propio de la CLI.
package claude

import (
	"os/exec"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/turn"
	"github.com/dpa23/wari/internal/proc"
)

// Driver es el turn.Driver de la CLI `claude`.
type Driver struct {
	// Bin es el ejecutable; vacío busca `claude` en PATH.
	Bin string
}

var _ turn.Driver = Driver{}

// New crea un agente que usa el ejecutable bin; vacío busca `claude` en PATH.
func New(bin string) *turn.Runner { return turn.New(Driver{Bin: bin}) }

// Name implementa turn.Driver.
func (Driver) Name() string { return "claude" }

// Spec implementa turn.Driver.
func (d Driver) Spec(t agent.Task) (proc.Spec, error) {
	bin := d.Bin
	if bin == "" {
		bin = "claude"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return proc.Spec{}, err
	}
	return proc.Spec{Path: path, Args: Args(t), Dir: t.Dir}, nil
}

// Parse implementa turn.Driver.
func (Driver) Parse(line []byte) ([]agent.Event, error) { return ParseLine(line) }

// Args devuelve los argumentos de `claude` para un turno. El prompt va
// después de "--" para que nunca se interprete como un flag.
func Args(t agent.Task) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	if t.SessionID != "" {
		args = append(args, "--resume", t.SessionID)
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Effort != "" {
		args = append(args, "--effort", t.Effort)
	}
	switch t.Permissions {
	case agent.PermissionAcceptEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case agent.PermissionPlan:
		args = append(args, "--permission-mode", "plan")
	default:
		args = append(args, "--permission-mode", "manual")
	}
	// Sin superficie de aprobación, lo que requiera permiso se deniega y
	// llega como NeedsApproval en lugar de bloquear el turno.
	args = append(args, "--permission-prompts", "none")
	return append(args, "--", t.Prompt)
}

// Info es lo que el formulario de agente nuevo ofrece de claude. Los alias
// salen de `claude --help` (--model acepta un alias o el nombre completo).
func Info() agent.Info {
	return agent.Info{
		Name: "claude",
		Models: []agent.ModelOption{
			{ID: "sonnet", Label: "Sonnet", Note: "equilibrado"},
			{ID: "opus", Label: "Opus", Note: "potente, gasta más cuota"},
			{ID: "fable", Label: "Fable", Note: "el más potente, gasta mucha cuota"},
		},
		ModelHint: "alias (sonnet, opus, fable) o nombre completo",
		Efforts:   []string{"low", "medium", "high", "xhigh", "max"},
		Launch:    Launch,
	}
}

// Launch arma la línea de `claude` interactivo (su modo por omisión): con
// "Preguntar" la propia CLI muestra sus diálogos de permiso. Los flags son los
// de `claude --help`; el mensaje inicial va como argumento posicional.
func Launch(t agent.Task, prompt string) (string, []string, error) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", nil, err
	}
	var args []string
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Effort != "" {
		args = append(args, "--effort", t.Effort)
	}
	switch t.Permissions {
	case agent.PermissionAcceptEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case agent.PermissionPlan:
		args = append(args, "--permission-mode", "plan")
	}
	if prompt != "" {
		args = append(args, "--", prompt)
	}
	return path, args, nil
}
