// Package agy es el adaptador de la CLI oficial `agy` (Antigravity CLI). Cada
// turno es un proceso `agy -p` en modo stream-json; los turnos siguientes
// reanudan la conversación con --conversation. La maquinaria de turnos vive en
// internal/agent/turn; aquí solo está lo propio de la CLI.
//
// # Permisos: no hay un modo de solo lectura
//
// `--mode plan` NO impide escribir en el proyecto (verificado con agy 1.4.2,
// testdata/agy/write_plan_explicit.jsonl): es una orden al modelo (la CLI
// expande un comando /plan), así que con una petición de cambio normal el
// modelo planea y guarda el plan fuera del proyecto, pero si se le pide
// escribir con una herramienta por su nombre, escribe. Los comandos de shell
// sí se deniegan solos en modo print (llegan en result.denied_actions). Por
// eso PermissionAsk y PermissionPlan usan --mode plan como mejor esfuerzo y la
// protección real de los archivos del proyecto es el checkpoint de WARI.
package agy

import (
	"os/exec"
	"sync"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/turn"
	"github.com/dpa23/wari/internal/proc"
)

// Driver es el turn.Driver de la CLI `agy`. Recuerda la conversación que pidió
// el último turno para detectar que la CLI no la encontró.
type Driver struct {
	// Bin es el ejecutable; vacío busca `agy` en PATH.
	Bin string

	mu   sync.Mutex
	want string // Task.SessionID del turno en curso
}

var _ turn.Driver = (*Driver)(nil)

// New crea un agente que usa el ejecutable bin; vacío busca `agy` en PATH.
func New(bin string) *turn.Runner { return turn.New(&Driver{Bin: bin}) }

// Name implementa turn.Driver.
func (*Driver) Name() string { return "agy" }

// Spec implementa turn.Driver.
func (d *Driver) Spec(t agent.Task) (proc.Spec, error) {
	bin := d.Bin
	if bin == "" {
		bin = "agy"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return proc.Spec{}, err
	}
	d.mu.Lock()
	d.want = t.SessionID
	d.mu.Unlock()
	return proc.Spec{Path: path, Args: Args(t), Dir: t.Dir}, nil
}

// Parse implementa turn.Driver. Además de traducir la línea, avisa con
// agent.ErrSessionNotFound cuando se pidió reanudar una conversación y la CLI
// abrió otra: con una conversación inexistente agy no falla, avisa por stderr
// ("conversation ... not found") y empieza una nueva sin el contexto.
func (d *Driver) Parse(line []byte) ([]agent.Event, error) {
	events, err := ParseLine(line)
	d.mu.Lock()
	want := d.want
	d.mu.Unlock()
	if want == "" {
		return events, err
	}
	for _, ev := range events {
		if s, ok := ev.(agent.Started); ok && s.SessionID != want {
			err = joinErr(err, notFoundErr(want, s.SessionID))
		}
	}
	return events, err
}

// Args devuelve los argumentos de `agy` para un turno. El prompt y los valores
// van pegados a su flag con "=": así nunca se interpretan como otro flag.
func Args(t agent.Task) []string {
	args := []string{"-p=" + t.Prompt, "--output-format", "stream-json"}
	if t.SessionID != "" {
		args = append(args, "--conversation="+t.SessionID)
	}
	if t.Model != "" {
		args = append(args, "--model="+t.Model)
	}
	switch t.Permissions {
	case agent.PermissionAcceptEdits:
		args = append(args, "--mode=accept-edits")
	default:
		// PermissionAsk y PermissionPlan: ver la nota del paquete.
		args = append(args, "--mode=plan")
	}
	return args
}
