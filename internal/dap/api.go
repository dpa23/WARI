package dap

import (
	"context"
	"encoding/json"
	"errors"
)

// Initialize envía initialize y guarda las capacidades del adaptador.
func (c *Client) Initialize(ctx context.Context, a InitializeArgs) (Capabilities, error) {
	if a.ClientID == "" {
		a.ClientID = "wari"
	}
	if a.ClientName == "" {
		a.ClientName = "WARI"
	}
	args := map[string]any{
		"clientID":                     a.ClientID,
		"clientName":                   a.ClientName,
		"adapterID":                    a.AdapterID,
		"linesStartAt1":                true,
		"columnsStartAt1":              true,
		"pathFormat":                   "path",
		"supportsVariableType":         true,
		"supportsVariablePaging":       false,
		"supportsRunInTerminalRequest": c.opts.RunInTerminal != nil,
		"supportsProgressReporting":    false,
	}
	body, err := c.Request(ctx, "initialize", args)
	if err != nil {
		return Capabilities{}, err
	}
	var caps Capabilities
	if err := decodeBody("initialize", body, nil, &caps); err != nil {
		return Capabilities{}, err
	}
	caps.Raw = body
	c.mu.Lock()
	c.caps = caps
	c.inited = true
	c.mu.Unlock()
	return caps, nil
}

// Launch envía launch con argumentos propios del adaptador. Algunos
// adaptadores no responden hasta recibir configurationDone: usa Go para no
// bloquear (Session.Start lo hace).
func (c *Client) Launch(ctx context.Context, args json.RawMessage) error {
	return c.do(ctx, "launch", args, nil)
}

// Attach envía attach con argumentos propios del adaptador (misma salvedad
// que Launch).
func (c *Client) Attach(ctx context.Context, args json.RawMessage) error {
	return c.do(ctx, "attach", args, nil)
}

// SetBreakpoints reemplaza los breakpoints de path por los de lines. Una
// lista vacía los borra. Devuelve el estado de cada uno, en el mismo orden.
func (c *Client) SetBreakpoints(ctx context.Context, path string, lines []int) ([]Breakpoint, error) {
	bps := make([]map[string]int, len(lines))
	for i, l := range lines {
		bps[i] = map[string]int{"line": l}
	}
	if lines == nil {
		lines = []int{}
	}
	var res struct {
		Breakpoints []Breakpoint `json:"breakpoints"`
	}
	err := c.do(ctx, "setBreakpoints", map[string]any{
		"source":         Source{Path: path},
		"breakpoints":    bps,
		"lines":          lines, // obsoleto, pero lo usan adaptadores antiguos
		"sourceModified": false,
	}, &res)
	return res.Breakpoints, err
}

// ConfigurationDone indica que terminó la configuración inicial.
func (c *Client) ConfigurationDone(ctx context.Context) error {
	return c.do(ctx, "configurationDone", nil, nil)
}

// Continue reanuda el hilo; devuelve si el adaptador reanudó todos los hilos.
func (c *Client) Continue(ctx context.Context, thread int) (allThreads bool, err error) {
	res := struct {
		AllThreadsContinued *bool `json:"allThreadsContinued"`
	}{}
	err = c.do(ctx, "continue", map[string]int{"threadId": thread}, &res)
	// Por especificación, ausente significa true.
	return res.AllThreadsContinued == nil || *res.AllThreadsContinued, err
}

// Next avanza una línea sin entrar en llamadas.
func (c *Client) Next(ctx context.Context, thread int) error {
	return c.do(ctx, "next", map[string]int{"threadId": thread}, nil)
}

// StepIn avanza entrando en la llamada.
func (c *Client) StepIn(ctx context.Context, thread int) error {
	return c.do(ctx, "stepIn", map[string]int{"threadId": thread}, nil)
}

// StepOut avanza hasta salir de la función actual.
func (c *Client) StepOut(ctx context.Context, thread int) error {
	return c.do(ctx, "stepOut", map[string]int{"threadId": thread}, nil)
}

// Pause detiene el hilo.
func (c *Client) Pause(ctx context.Context, thread int) error {
	return c.do(ctx, "pause", map[string]int{"threadId": thread}, nil)
}

// Threads lista los hilos.
func (c *Client) Threads(ctx context.Context) ([]Thread, error) {
	var res struct {
		Threads []Thread `json:"threads"`
	}
	err := c.do(ctx, "threads", nil, &res)
	return res.Threads, err
}

// StackTrace pide los marcos del hilo desde start; levels 0 pide todos.
// Devuelve también el total que conoce el adaptador (0 si no lo informa).
func (c *Client) StackTrace(ctx context.Context, thread, start, levels int) ([]StackFrame, int, error) {
	args := map[string]int{"threadId": thread}
	if start > 0 {
		args["startFrame"] = start
	}
	if levels > 0 {
		args["levels"] = levels
	}
	var res struct {
		StackFrames []StackFrame `json:"stackFrames"`
		TotalFrames int          `json:"totalFrames"`
	}
	err := c.do(ctx, "stackTrace", args, &res)
	return res.StackFrames, res.TotalFrames, err
}

// Scopes pide los ámbitos de un marco.
func (c *Client) Scopes(ctx context.Context, frameID int) ([]Scope, error) {
	var res struct {
		Scopes []Scope `json:"scopes"`
	}
	err := c.do(ctx, "scopes", map[string]int{"frameId": frameID}, &res)
	return res.Scopes, err
}

// Variables pide los hijos de una variablesReference (un ámbito o una
// variable estructurada).
func (c *Client) Variables(ctx context.Context, ref int) ([]Variable, error) {
	var res struct {
		Variables []Variable `json:"variables"`
	}
	err := c.do(ctx, "variables", map[string]int{"variablesReference": ref}, &res)
	return res.Variables, err
}

// Evaluate evalúa una expresión en el contexto de un marco (frameID 0 usa el
// contexto global del adaptador). context puede ser "repl", "watch" o "hover".
func (c *Client) Evaluate(ctx context.Context, expr string, frameID int, evalContext string) (EvaluateResult, error) {
	args := map[string]any{"expression": expr}
	if frameID != 0 {
		args["frameId"] = frameID
	}
	if evalContext != "" {
		args["context"] = evalContext
	}
	var res EvaluateResult
	err := c.do(ctx, "evaluate", args, &res)
	return res, err
}

// Disconnect cierra la sesión de depuración; terminateDebuggee pide además
// matar el proceso depurado. Algunos adaptadores cierran la conexión sin
// responder: eso cuenta como éxito.
func (c *Client) Disconnect(ctx context.Context, terminateDebuggee bool) error {
	err := c.do(ctx, "disconnect", map[string]any{"terminateDebuggee": terminateDebuggee}, nil)
	if err == nil || isClosed(err) {
		c.mu.Lock()
		c.disconnected = true
		c.mu.Unlock()
		return nil
	}
	return err
}

func isClosed(err error) bool { return errors.Is(err, ErrClosed) }
