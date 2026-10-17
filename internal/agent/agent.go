// Package agent define el contrato común de los agentes de WARI. Cada
// adaptador (internal/agent/<cli>) lanza una CLI oficial como subproceso y
// traduce su salida a los eventos normalizados de este paquete; el resto del
// sistema solo conoce estos tipos.
package agent

import (
	"context"
	"encoding/json"
	"errors"
)

// Agent es un agente controlado por WARI. WARI es el proceso padre: decide
// cuándo el agente corre, se pausa o muere.
type Agent interface {
	// Start lanza un turno con la tarea. Devuelve ErrBusy si ya hay uno en curso.
	Start(ctx context.Context, task Task) error
	// Send lanza un turno nuevo dentro de la misma sesión.
	Send(msg string) error
	// Events entrega los eventos de todos los turnos del agente, sin mezclar
	// turnos. Cada turno termina con exactamente un Done, que se emite cuando
	// el proceso del turno ya salió y Status ya no es StatusRunning, así que
	// se puede llamar a Send en cuanto llega. Un turno cancelado con Cancel
	// no emite Done, y después de que Cancel vuelve no se entrega ningún
	// evento suyo. El canal se cierra cuando el agente termina
	// definitivamente (Close); un turno interrumpido por Close tampoco emite
	// Done. Hay que leer el canal: si se llena, el turno espera.
	Events() <-chan Event
	// Pause congela el turno en curso con todos sus subprocesos.
	Pause() error
	// Resume reanuda un turno pausado.
	Resume() error
	// Cancel termina el turno en curso con todos sus descendientes. El agente
	// sigue usable: queda en StatusIdle y acepta otro turno. Sin turno en
	// curso no hace nada.
	Cancel() error
	// Close termina el agente: mata el turno en curso con todos sus
	// descendientes, cierra Events y deja el estado en StatusDone. Las
	// llamadas concurrentes esperan a la primera.
	Close() error
	// Status devuelve el estado actual.
	Status() Status
}

// Task describe un turno.
type Task struct {
	// Prompt es el mensaje para el agente.
	Prompt string
	// Dir es el directorio de trabajo; vacío usa el actual.
	Dir string
	// SessionID, si no está vacío, reanuda esa sesión de la CLI.
	SessionID string
	// Model es el modelo a usar; vacío deja el predeterminado de la CLI.
	Model string
	// Permissions fija el modo de permisos. El valor cero (PermissionAsk) es
	// el más seguro: nunca se hereda el modo configurado por el usuario.
	Permissions PermissionMode
}

// PermissionMode es el modo de permisos normalizado. Cada adaptador lo
// traduce a los flags de su CLI.
type PermissionMode int

const (
	// PermissionAsk pide aprobación para todo lo que no sea de solo lectura.
	// Sin una superficie de aprobación, la CLI deniega y se emite NeedsApproval.
	PermissionAsk PermissionMode = iota
	// PermissionAcceptEdits permite editar archivos sin preguntar.
	PermissionAcceptEdits
	// PermissionPlan solo permite leer y planear.
	PermissionPlan
)

// Status es el estado de un agente.
type Status int

const (
	// StatusIdle: sin turno en curso (antes del primero o entre turnos).
	StatusIdle Status = iota
	// StatusRunning: hay un turno en curso.
	StatusRunning
	// StatusPaused: el turno en curso está congelado.
	StatusPaused
	// StatusWaitingApproval: el agente espera una aprobación del usuario.
	StatusWaitingApproval
	// StatusError: el último turno falló; se puede intentar otro.
	StatusError
	// StatusDone: el agente terminó y no acepta más turnos.
	StatusDone
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusRunning:
		return "running"
	case StatusPaused:
		return "paused"
	case StatusWaitingApproval:
		return "waiting-approval"
	case StatusError:
		return "error"
	case StatusDone:
		return "done"
	}
	return "unknown"
}

var (
	// ErrBusy indica que ya hay un turno en curso.
	ErrBusy = errors.New("agent: ya hay un turno en curso")
	// ErrDone indica que el agente ya terminó.
	ErrDone = errors.New("agent: el agente ya terminó")
	// ErrNoSession indica que Send no tiene una sesión que continuar.
	ErrNoSession = errors.New("agent: no hay sesión que continuar")
	// ErrSessionNotFound llega envuelto en un evento Error cuando la CLI no
	// pudo reanudar Task.SessionID (sesión inexistente o borrada). Cada
	// adaptador decide cómo reconocerlo en su CLI.
	ErrSessionNotFound = errors.New("agent: no se pudo reanudar la sesión")
)

// Event es un evento normalizado. Los tipos concretos son los de este
// paquete; se distinguen con un type switch.
type Event interface{ isEvent() }

// Started indica que la CLI abrió o reanudó una sesión. Llega antes que
// cualquier otro evento del turno, así que el ID sirve para reanudar aunque
// el turno se cancele.
type Started struct {
	SessionID string
	Model     string
}

// TextDelta es un fragmento de la respuesta del agente principal.
type TextDelta struct {
	Text string
}

// ToolCall es una llamada a herramienta completa.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// FileEditProposed indica que una llamada a herramienta edita un archivo.
// Acompaña al ToolCall correspondiente; las ediciones hechas por comandos de
// shell no se detectan aquí (las atribuye el watcher).
type FileEditProposed struct {
	ToolCallID string
	Tool       string
	Path       string
}

// Usage es el consumo reportado por la CLI al final de un turno.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	// CostUSD es el costo estimado por la CLI; 0 si no lo reporta.
	CostUSD float64
}

// NeedsApproval indica una acción que requiere aprobación. Si Denied es
// true, la CLI ya la denegó automáticamente (modo headless sin superficie de
// aprobación) y la acción no se realizó.
type NeedsApproval struct {
	ToolCallID string
	Tool       string
	Input      json.RawMessage
	Denied     bool
}

// Done cierra un turno, salvo que se cancele con Cancel o se cierre el agente
// con Close. Si la CLI terminó sin reportar un resultado, el adaptador lo
// sintetiza con IsError, precedido por un Error con la causa.
type Done struct {
	SessionID string
	// Result es el texto final del turno.
	Result string
	// IsError indica que el turno terminó con error.
	IsError bool
	// Turns es el número de turnos internos que reportó la CLI.
	Turns int
}

// Error es un error del agente o del adaptador.
type Error struct {
	Err error
}

func (Started) isEvent()          {}
func (TextDelta) isEvent()        {}
func (ToolCall) isEvent()         {}
func (FileEditProposed) isEvent() {}
func (Usage) isEvent()            {}
func (NeedsApproval) isEvent()    {}
func (Done) isEvent()             {}
func (Error) isEvent()            {}
