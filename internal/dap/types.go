package dap

import "encoding/json"

// Capabilities son las capacidades del adaptador que el cliente usa; Raw
// conserva la respuesta completa de initialize.
type Capabilities struct {
	SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest"`
	SupportsCancelRequest            bool `json:"supportsCancelRequest"`
	SupportsTerminateRequest         bool `json:"supportsTerminateRequest"`
	SupportsConditionalBreakpoints   bool `json:"supportsConditionalBreakpoints"`
	SupportsEvaluateForHovers        bool `json:"supportsEvaluateForHovers"`

	Raw json.RawMessage `json:"-"`
}

// InitializeArgs son los argumentos de initialize. Los campos vacíos toman
// valores por defecto: ClientID "wari", líneas y columnas desde 1 y rutas de
// archivo como path.
type InitializeArgs struct {
	ClientID   string
	AdapterID  string
	ClientName string
}

// Source identifica un archivo fuente.
type Source struct {
	Name            string `json:"name,omitempty"`
	Path            string `json:"path,omitempty"`
	SourceReference int    `json:"sourceReference,omitempty"`
}

// Breakpoint es el estado de un breakpoint según el adaptador.
type Breakpoint struct {
	ID       int     `json:"id,omitempty"`
	Verified bool    `json:"verified"`
	Message  string  `json:"message,omitempty"`
	Source   *Source `json:"source,omitempty"`
	Line     int     `json:"line,omitempty"`
}

// Thread es un hilo del depurado.
type Thread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// StackFrame es un marco de la pila.
type StackFrame struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Source *Source `json:"source,omitempty"`
	Line   int     `json:"line"`
	Column int     `json:"column"`
}

// Scope es un ámbito de variables de un marco (locales, argumentos...).
type Scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	Expensive          bool   `json:"expensive"`
}

// Variable es una variable; si VariablesReference > 0 tiene hijos que se
// piden con Variables. Las referencias solo valen mientras el depurado siga
// detenido en la misma parada.
type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
	NamedVariables     int    `json:"namedVariables,omitempty"`
	IndexedVariables   int    `json:"indexedVariables,omitempty"`
	EvaluateName       string `json:"evaluateName,omitempty"`
}

// EvaluateResult es el resultado de evaluate.
type EvaluateResult struct {
	Result             string `json:"result"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

// RunInTerminalArgs es la petición inversa runInTerminal del adaptador.
type RunInTerminalArgs struct {
	Kind  string         `json:"kind,omitempty"`
	Title string         `json:"title,omitempty"`
	Cwd   string         `json:"cwd"`
	Args  []string       `json:"args"`
	Env   map[string]any `json:"env,omitempty"`
}

// RunInTerminalResult es la respuesta a runInTerminal.
type RunInTerminalResult struct {
	ProcessID      int `json:"processId,omitempty"`
	ShellProcessID int `json:"shellProcessId,omitempty"`
}

// Event es un evento del adaptador; los tipos concretos son
// InitializedEvent, StoppedEvent, ContinuedEvent, OutputEvent,
// TerminatedEvent, ExitedEvent, BreakpointEvent, ThreadEvent y
// UnknownEvent.
type Event interface {
	EventName() string
}

// InitializedEvent: el adaptador acepta configuración (breakpoints).
type InitializedEvent struct{}

// StoppedEvent: la ejecución se detuvo.
type StoppedEvent struct {
	Reason            string `json:"reason"`
	Description       string `json:"description,omitempty"`
	ThreadID          int    `json:"threadId"`
	AllThreadsStopped bool   `json:"allThreadsStopped,omitempty"`
	Text              string `json:"text,omitempty"`
	HitBreakpointIDs  []int  `json:"hitBreakpointIds,omitempty"`
}

// ContinuedEvent: la ejecución se reanudó.
type ContinuedEvent struct {
	ThreadID            int  `json:"threadId"`
	AllThreadsContinued bool `json:"allThreadsContinued,omitempty"`
}

// OutputEvent: salida del depurado o del adaptador.
type OutputEvent struct {
	Category           string  `json:"category,omitempty"`
	Output             string  `json:"output"`
	Source             *Source `json:"source,omitempty"`
	Line               int     `json:"line,omitempty"`
	VariablesReference int     `json:"variablesReference,omitempty"`
}

// TerminatedEvent: la sesión de depuración terminó.
type TerminatedEvent struct{}

// ExitedEvent: el proceso depurado salió.
type ExitedEvent struct {
	ExitCode int `json:"exitCode"`
}

// BreakpointEvent: un breakpoint cambió (por ejemplo, quedó verificado).
type BreakpointEvent struct {
	Reason     string     `json:"reason"`
	Breakpoint Breakpoint `json:"breakpoint"`
}

// ThreadEvent: un hilo empezó o terminó.
type ThreadEvent struct {
	Reason   string `json:"reason"`
	ThreadID int    `json:"threadId"`
}

// UnknownEvent es un evento que el cliente no interpreta.
type UnknownEvent struct {
	Name string
	Body json.RawMessage
}

// EventName implementa Event.
func (InitializedEvent) EventName() string { return "initialized" }

// EventName implementa Event.
func (StoppedEvent) EventName() string { return "stopped" }

// EventName implementa Event.
func (ContinuedEvent) EventName() string { return "continued" }

// EventName implementa Event.
func (OutputEvent) EventName() string { return "output" }

// EventName implementa Event.
func (TerminatedEvent) EventName() string { return "terminated" }

// EventName implementa Event.
func (ExitedEvent) EventName() string { return "exited" }

// EventName implementa Event.
func (BreakpointEvent) EventName() string { return "breakpoint" }

// EventName implementa Event.
func (ThreadEvent) EventName() string { return "thread" }

// EventName implementa Event.
func (e UnknownEvent) EventName() string { return e.Name }

// parseEvent decodifica el cuerpo de un evento; un cuerpo ilegible de un
// evento conocido se entrega como UnknownEvent para no perderlo.
func parseEvent(name string, body json.RawMessage) Event {
	var (
		ev  Event
		err error
	)
	decode := func(v any) {
		if len(body) > 0 {
			err = json.Unmarshal(body, v)
		}
	}
	switch name {
	case "initialized":
		ev = InitializedEvent{}
	case "stopped":
		var e StoppedEvent
		decode(&e)
		ev = e
	case "continued":
		var e ContinuedEvent
		decode(&e)
		ev = e
	case "output":
		var e OutputEvent
		decode(&e)
		ev = e
	case "terminated":
		ev = TerminatedEvent{}
	case "exited":
		var e ExitedEvent
		decode(&e)
		ev = e
	case "breakpoint":
		var e BreakpointEvent
		decode(&e)
		ev = e
	case "thread":
		var e ThreadEvent
		decode(&e)
		ev = e
	default:
		return UnknownEvent{Name: name, Body: body}
	}
	if err != nil {
		return UnknownEvent{Name: name, Body: body}
	}
	return ev
}
