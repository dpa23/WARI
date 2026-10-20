// Package codex es el adaptador de la CLI oficial `codex` (OpenAI Codex CLI).
// Cada turno es un proceso `codex exec --json`; los turnos siguientes
// reanudan el hilo con `codex exec resume`. La maquinaria de turnos vive en
// internal/agent/turn; aquí solo está lo propio de la CLI. El esquema de
// eventos está en docs/codex-events.md.
//
// # Permisos: el sandbox es el límite real
//
// codex exec no tiene superficie de aprobación: lo que el sandbox no permite
// falla. PermissionAsk y PermissionPlan usan `-s read-only` y
// PermissionAcceptEdits usa `-s workspace-write`. Nunca se pasa
// --dangerously-bypass-approvals-and-sandbox, --dangerously-bypass-hook-trust
// ni danger-full-access.
//
// `codex exec resume` NO admite -s en sus propios flags (verificado con
// `codex exec resume --help`, 0.163.0). Los flags de nivel `exec` van antes de
// `resume` y la CLI los acepta ahí (un valor inválido de -s se rechaza), pero
// no se pudo comprobar con un turno real que el sandbox de `exec` se aplique
// al reanudar. Por eso, al reanudar, el modo se fija además con
// `-c sandbox_mode="..."`, cuyo efecto sí se verificó: un valor inválido
// falla al cargar la configuración, antes de buscar el hilo.
//
// # Ediciones: sin FileEditProposed
//
// Un item file_change solo llega como item.completed: el archivo ya fue
// editado. Emitir FileEditProposed en ese momento haría que un checkpoint
// tomado entonces guardara el archivo ya modificado, así que el adaptador
// emite solo ToolCall (Name "apply_patch") y NO FileEditProposed. El
// checkpoint de una edición de codex debe salir del watcher de archivos.
//
// # Sesión inexistente
//
// Verificado con 0.163.0: `codex exec resume <id inexistente>` sale con
// código 1, sin nada en stdout y con "Error: thread/resume: ... no rollout
// found for thread id <id>" en stderr. El ejecutor de turnos lo traduce a
// agent.ErrSessionNotFound porque no hubo thread.started. NO verificado el
// caso de un id con forma válida pero de otro directorio de trabajo.
package codex

import (
	"os/exec"
	"sync"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/turn"
	"github.com/dpa23/wari/internal/proc"
)

// Driver es el turn.Driver de la CLI `codex`. Lleva el estado del turno en
// curso (hilo, texto final, errores ya emitidos) en un parser nuevo por turno.
type Driver struct {
	// Bin es el ejecutable; vacío busca `codex` en PATH.
	Bin string

	mu sync.Mutex
	p  *parser
}

var _ turn.Driver = (*Driver)(nil)

// New crea un agente que usa el ejecutable bin; vacío busca `codex` en PATH.
func New(bin string) *turn.Runner { return turn.New(&Driver{Bin: bin}) }

// Name implementa turn.Driver.
func (*Driver) Name() string { return "codex" }

// Spec implementa turn.Driver.
func (d *Driver) Spec(t agent.Task) (proc.Spec, error) {
	bin := d.Bin
	if bin == "" {
		bin = "codex"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return proc.Spec{}, err
	}
	d.mu.Lock()
	d.p = newParser()
	d.mu.Unlock()
	return proc.Spec{Path: path, Args: Args(t), Dir: t.Dir}, nil
}

// Parse implementa turn.Driver.
func (d *Driver) Parse(line []byte) ([]agent.Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.p == nil {
		d.p = newParser()
	}
	return d.p.parse(line)
}

// Args devuelve los argumentos de `codex` para un turno. El prompt (y el id
// de sesión al reanudar) van después de "--": nunca se interpretan como flag.
// El modelo y el directorio van pegados a su flag con "=" por lo mismo.
func Args(t agent.Task) []string {
	sandbox := "read-only"
	if t.Permissions == agent.PermissionAcceptEdits {
		sandbox = "workspace-write"
	}
	// PermissionAsk, PermissionPlan y cualquier modo desconocido: solo lectura.
	args := []string{"exec", "--json", "--skip-git-repo-check", "-s", sandbox}
	if t.Model != "" {
		args = append(args, "--model="+t.Model)
	}
	if t.Dir != "" {
		args = append(args, "--cd="+t.Dir)
	}
	if t.SessionID != "" {
		// resume no admite -s: ver la nota del paquete.
		return append(args, "-c", `sandbox_mode="`+sandbox+`"`, "resume", "--", t.SessionID, t.Prompt)
	}
	return append(args, "--", t.Prompt)
}

// Info es lo que el formulario de agente nuevo ofrece de codex. No hay lista de
// modelos ni nivel de esfuerzo verificados (la CLI no está instalada en la
// máquina de desarrollo): el modelo se escribe a mano y Task.Effort se ignora.
func Info() agent.Info {
	return agent.Info{Name: "codex", ModelHint: "nombre del modelo, como en `codex --model`",
		Launch:     Launch,
		LaunchNote: "sin verificar: codex no está instalado en la máquina de desarrollo",
		EditNote:   "las ediciones se aplican antes de que WARI las vea: sin checkpoint previo ni aviso de choque anticipado"}
}

// Launch arma la línea de `codex` interactivo con los mismos flags que usa el
// adaptador sin pantalla (--model, -s). SIN VERIFICAR contra la CLI real.
func Launch(t agent.Task, prompt string) (string, []string, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", nil, err
	}
	sandbox := "read-only"
	if t.Permissions == agent.PermissionAcceptEdits {
		sandbox = "workspace-write"
	}
	args := []string{"-s", sandbox}
	if t.Model != "" {
		args = append(args, "--model="+t.Model)
	}
	if prompt != "" {
		args = append(args, "--", prompt)
	}
	return path, args, nil
}
