// Package opencode es el adaptador de la CLI oficial `opencode` (v2.0.18).
// Cada turno se ejecuta como un subproceso invocando `opencode run --standalone --format json`.
// La maquinaria de turnos vive en internal/agent/turn; aquí residen el driver, el parser y el
// sintetizador de cierre (Finisher).
//
// # Permisos y Modos
//
// Por defecto, sin el flag `--agent`, `opencode` tiene permisos para modificar y crear
// archivos directamente en el proyecto (verificado). Cuando se pasa `--agent plan`,
// la CLI queda restringida a modo de planeación y rechaza cualquier modificación de archivos
// fuera del directorio de plan (~/.opencode/plan).
//
// Mapeo obligatorio de permisos:
//   - agent.PermissionAsk y agent.PermissionPlan se mapean a `--agent plan`.
//   - agent.PermissionAcceptEdits se ejecuta sin `--agent`.
//
// NUNCA se utiliza el flag `--auto`.
//
// # Ediciones de archivo (FileEditProposed)
//
// Este adaptador NO emite eventos FileEditProposed. En opencode, el evento `tool_use`
// llega cuando la herramienta ya se ejecutó (`state.status == "completed"`). Si se tomara
// un checkpoint en ese momento, guardaría el archivo ya editado en vez del estado previo
// a la modificación. La detección y atribución de ediciones recae en el watcher del espacio de trabajo.
//
// # Consumo de tokens (Usage)
//
// En el evento `step_finish`, los tokens reportados incluyen input, output, reasoning,
// cache.read y cache.write. En agent.Usage, OutputTokens se calcula sumando output + reasoning,
// ya que los tokens de razonamiento forman parte de los tokens generados por el modelo.
package opencode

import (
	"os/exec"
	"sync"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/turn"
	"github.com/dpa23/wari/internal/proc"
)

// TestedVersion es la versión de la CLI con la que se verificó y probó el adaptador.
const TestedVersion = "2.0.18"

// MinVersion es la versión mínima soportada: la probada.
const MinVersion = TestedVersion

// Driver es el turn.Driver de la CLI `opencode`. Lleva el estado del turno en
// curso en un parser nuevo por turno (serializados). Implementa también turn.Finisher
// para sintetizar el evento Done al término del proceso.
type Driver struct {
	// Bin es el ejecutable; vacío busca `opencode` en PATH.
	Bin string

	mu sync.Mutex
	p  *parser
}

var _ turn.Driver = (*Driver)(nil)
var _ turn.Finisher = (*Driver)(nil)

// New crea un agente que usa el ejecutable bin; vacío busca `opencode` en PATH.
func New(bin string) *turn.Runner { return turn.New(&Driver{Bin: bin}) }

// Name implementa turn.Driver.
func (*Driver) Name() string { return "opencode" }

// Reset reinicia el estado interno del driver para un nuevo turno.
func (d *Driver) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.p = newParser()
}

// Spec implementa turn.Driver. Reinicia el estado del driver y devuelve la
// especificación del comando para el turno.
func (d *Driver) Spec(t agent.Task) (proc.Spec, error) {
	bin := d.Bin
	if bin == "" {
		bin = "opencode"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return proc.Spec{}, err
	}
	d.Reset()
	return proc.Spec{Path: path, Args: Args(t), Dir: t.Dir}, nil
}

// Parse implementa turn.Driver. Traduce una línea de la salida del turno a eventos.
func (d *Driver) Parse(line []byte) ([]agent.Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.p == nil {
		d.p = newParser()
	}
	return d.p.parse(line)
}

// Finish implementa turn.Finisher. Como opencode no emite un evento final de
// resultado, Finish sintetiza el evento Done al salir el proceso del turno.
func (d *Driver) Finish(exitErr error, stderr string) *agent.Done {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.p == nil {
		d.p = newParser()
	}
	return d.p.finish(exitErr, stderr)
}

// Args devuelve los argumentos de `opencode` para un turno:
// 'opencode run --standalone --format json [--agent plan] [--session ID] [--model M] -- <prompt>'
// El prompt siempre se coloca después de "--" para proteger prompts que comiencen con guión ("-").
// NUNCA se incluye el flag `--auto`.
func Args(t agent.Task) []string {
	args := []string{"run", "--standalone", "--format", "json"}
	switch t.Permissions {
	case agent.PermissionAcceptEdits:
		// sin --agent: opencode escribe archivos directamente
	case agent.PermissionPlan, agent.PermissionAsk:
		args = append(args, "--agent", "plan")
	default:
		// Por seguridad, cualquier otro modo cae en plan
		args = append(args, "--agent", "plan")
	}
	if t.SessionID != "" {
		args = append(args, "--session", t.SessionID)
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	args = append(args, "--", t.Prompt)
	return args
}

// Info es lo que el formulario de agente nuevo ofrece de opencode. El modelo
// lleva el formato de `opencode run --model` y Task.Effort se ignora (su
// equivalente es la variante, que va dentro del modelo tras "#").
func Info() agent.Info {
	return agent.Info{Name: "opencode", ModelHint: "proveedor/modelo o proveedor/modelo#variante",
		EditNote: "las ediciones se aplican antes de que WARI las vea: sin checkpoint previo ni aviso de choque anticipado"}
}
