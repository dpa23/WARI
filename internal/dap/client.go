// Package dap es un cliente mínimo del Debug Adapter Protocol: mensajes JSON
// con cabecera Content-Length sobre stdio o TCP, escrito a mano con la
// biblioteca estándar. Cubre initialize, launch/attach, breakpoints, control
// de ejecución, pila, variables navegables y evaluate.
//
// Hay tres capas:
//
//   - Client: la conexión. Peticiones concurrentes correlacionadas por seq,
//     cancelación por contexto y un canal de eventos que nunca bloquea al
//     lector (ver eventQueue para la política de desbordamiento).
//   - Session: el flujo de alto nivel de un depurador (Start, breakpoints,
//     Continue/Next/StepIn/StepOut/Pause, Stack, Variables, Stop).
//   - Adapter y Connect: adaptadores conocidos (netcoredbg, java-debug) y su
//     arranque bajo demanda con internal/proc.
//
// El transporte trabaja sobre io.Reader/io.Writer, de modo que se prueba con
// pipes y un adaptador falso.
package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Errores del cliente.
var (
	// ErrClosed indica que la conexión terminó o se está cerrando.
	ErrClosed = errors.New("dap: conexión cerrada")
)

// DefaultCloseTimeout es el plazo de Close para que el adaptador salga por
// sí solo tras un disconnect antes de matar su grupo.
const DefaultCloseTimeout = 2 * time.Second

// ResponseError es la respuesta de error (success=false) del adaptador.
type ResponseError struct {
	Command string
	Message string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("dap: %s falló: %s", e.Command, e.Message)
}

// Options configura un Client.
type Options struct {
	// EventBuffer es cuántos eventos sin leer se retienen; 0 usa
	// DefaultEventBuffer.
	EventBuffer int
	// CloseTimeout acota la espera de Close a que el adaptador salga tras un
	// disconnect; 0 usa DefaultCloseTimeout.
	CloseTimeout time.Duration
	// RunInTerminal atiende la petición inversa runInTerminal. Si es nil el
	// cliente no la anuncia y responde con error si el adaptador la envía.
	// Se ejecuta en su propia goroutine.
	RunInTerminal func(ctx context.Context, args RunInTerminalArgs) (RunInTerminalResult, error)
}

// Client es una conexión DAP. Todos los métodos son seguros para uso
// concurrente.
type Client struct {
	opts Options
	proc Process

	r  io.Reader
	br *bufio.Reader
	w  io.Writer

	writeMu sync.Mutex // serializa las escrituras de tramas completas

	mu           sync.Mutex
	nextSeq      int
	pending      map[int]*Call
	closing      bool
	readErr      error
	inited       bool
	disconnected bool
	caps         Capabilities
	hook         func(Event)

	events      *eventQueue
	initialized chan struct{}
	initOnce    sync.Once
	malformed   atomic.Int64

	ctx    context.Context // se cancela al terminar la conexión
	cancel context.CancelFunc

	done      chan struct{} // se cierra cuando termina la lectura
	closeOnce sync.Once
	closeErr  error
}

// NewClient crea un cliente sobre un transporte ya abierto e inicia la
// lectura. No envía initialize: llama a Initialize.
func NewClient(r io.Reader, w io.Writer, opts Options) *Client {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		opts:        opts,
		r:           r,
		br:          bufio.NewReaderSize(r, 64<<10),
		w:           w,
		pending:     map[int]*Call{},
		events:      newEventQueue(opts.EventBuffer),
		initialized: make(chan struct{}),
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// NewProcessClient crea un cliente sobre un proceso ya lanzado; Close
// terminará su grupo.
func NewProcessClient(p Process, opts Options) *Client {
	c := NewClient(p.Stdout(), p.Stdin(), opts)
	c.proc = p
	return c
}

// Events devuelve el canal de eventos. Se cierra cuando la conexión terminó
// y se entregaron los eventos pendientes. Nunca bloquea al lector: si el
// consumidor no da abasto se descartan eventos según la política de
// eventQueue (primero output, el más antiguo) y se cuentan en
// DroppedEvents. Tras Close se abandonan los eventos aún no leídos.
func (c *Client) Events() <-chan Event { return c.events.out }

// DroppedEvents es cuántos eventos se descartaron por desbordamiento.
func (c *Client) DroppedEvents() int { return c.events.droppedCount() }

// Malformed es cuántos mensajes ilegibles o inesperados se ignoraron.
func (c *Client) Malformed() int64 { return c.malformed.Load() }

// SetEventHook registra una función que recibe cada evento antes de
// encolarlo. Se llama desde la goroutine de lectura: no debe bloquear. Sirve
// a Session para seguir el estado sin consumir Events.
func (c *Client) SetEventHook(f func(Event)) {
	c.mu.Lock()
	c.hook = f
	c.mu.Unlock()
}

// Initialized se cierra cuando llega el evento initialized.
func (c *Client) Initialized() <-chan struct{} { return c.initialized }

// Done se cierra cuando la conexión terminó (cierre ordenado o caída del
// adaptador).
func (c *Client) Done() <-chan struct{} { return c.done }

// Err es la causa de la terminación; nil mientras la conexión siga viva.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.deadErr()
	default:
		return nil
	}
}

func (c *Client) deadErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr == nil || errors.Is(c.readErr, io.EOF) {
		return ErrClosed
	}
	return fmt.Errorf("%w: %v", ErrClosed, c.readErr)
}

// Capabilities devuelve las capacidades anunciadas por el adaptador (vacías
// antes de Initialize).
func (c *Client) Capabilities() Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// Call es una petición en vuelo.
type Call struct {
	c       *Client
	seq     int
	command string

	done chan struct{}
	body json.RawMessage
	err  error
}

// Done se cierra cuando la petición terminó (respuesta, error, cancelación o
// caída de la conexión).
func (call *Call) Done() <-chan struct{} { return call.done }

// Wait espera la respuesta. Si ctx se cancela antes, abandona la petición,
// pide su cancelación al adaptador (si lo soporta) y devuelve ctx.Err().
func (call *Call) Wait(ctx context.Context) (json.RawMessage, error) {
	select {
	case <-call.done:
		return call.body, call.err
	case <-ctx.Done():
	}
	if call.c.finish(call, nil, ctx.Err()) {
		call.c.cancelRemote(call)
	}
	<-call.done
	return call.body, call.err
}

// Go envía una petición sin esperar su respuesta. args puede ser nil, un
// json.RawMessage ya serializado o cualquier valor serializable.
func (c *Client) Go(command string, args any) *Call {
	call := &Call{c: c, command: command, done: make(chan struct{})}
	fail := func(err error) *Call {
		call.err = err
		close(call.done)
		return call
	}
	var raw json.RawMessage
	switch a := args.(type) {
	case nil:
	case json.RawMessage:
		raw = a
	default:
		b, err := json.Marshal(a)
		if err != nil {
			return fail(fmt.Errorf("dap: serializar argumentos de %s: %w", command, err))
		}
		raw = b
	}
	if len(raw) > 0 && !json.Valid(raw) {
		return fail(fmt.Errorf("dap: argumentos de %s no son JSON válido", command))
	}

	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return fail(ErrClosed)
	}
	select {
	case <-c.done:
		c.mu.Unlock()
		return fail(c.deadErr())
	default:
	}
	c.nextSeq++
	call.seq = c.nextSeq
	c.pending[call.seq] = call
	c.mu.Unlock()

	req := outRequest{Seq: call.seq, Type: "request", Command: command, Arguments: raw}
	if err := c.send(req); err != nil {
		c.finish(call, nil, err)
	}
	return call
}

// finish completa la petición si seguía pendiente y devuelve si lo hizo.
func (c *Client) finish(call *Call, body json.RawMessage, err error) bool {
	c.mu.Lock()
	_, ok := c.pending[call.seq]
	delete(c.pending, call.seq)
	c.mu.Unlock()
	if !ok {
		return false
	}
	call.body, call.err = body, err
	close(call.done)
	return true
}

// cancelRemote pide al adaptador cancelar una petición abandonada; es de
// mejor esfuerzo y no retiene al llamador.
func (c *Client) cancelRemote(call *Call) {
	if !c.Capabilities().SupportsCancelRequest {
		return
	}
	go func() {
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			return
		}
		c.nextSeq++
		seq := c.nextSeq
		c.mu.Unlock()
		args, _ := json.Marshal(map[string]any{"requestId": call.seq})
		_ = c.send(outRequest{Seq: seq, Type: "request", Command: "cancel", Arguments: args})
	}()
}

// Request envía una petición y espera su respuesta. Devuelve el cuerpo sin
// interpretar.
func (c *Client) Request(ctx context.Context, command string, args any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.Go(command, args).Wait(ctx)
}

// do es Request decodificando el cuerpo en result (si no es nil).
func (c *Client) do(ctx context.Context, command string, args, result any) error {
	body, err := c.Request(ctx, command, args)
	return decodeBody(command, body, err, result)
}

func decodeBody(command string, body json.RawMessage, err error, result any) error {
	if err != nil {
		return err
	}
	if result == nil || len(body) == 0 || string(body) == "null" {
		return nil
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("dap: respuesta de %s ilegible: %w", command, err)
	}
	return nil
}

// Mensajes salientes.
type outRequest struct {
	Seq       int             `json:"seq"`
	Type      string          `json:"type"`
	Command   string          `json:"command"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type outResponse struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	RequestSeq int             `json:"request_seq"`
	Success    bool            `json:"success"`
	Command    string          `json:"command"`
	Message    string          `json:"message,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// incoming es la unión de lo que puede llegar del adaptador.
type incoming struct {
	Seq        int             `json:"seq"`
	Type       string          `json:"type"`
	Command    string          `json:"command"`
	Arguments  json.RawMessage `json:"arguments"`
	RequestSeq int             `json:"request_seq"`
	Success    bool            `json:"success"`
	Message    string          `json:"message"`
	Body       json.RawMessage `json:"body"`
	Event      string          `json:"event"`
}

// send serializa y escribe una trama completa.
func (c *Client) send(m any) error {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("dap: serializar mensaje: %w", err)
	}
	frame := encodeFrame(body)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.w.Write(frame); err != nil {
		return fmt.Errorf("%w: escribir: %v", ErrClosed, err)
	}
	return nil
}

// readLoop lee mensajes hasta que el flujo termina; los mensajes mal
// formados se cuentan y se ignoran.
func (c *Client) readLoop() {
	var err error
	defer func() {
		c.mu.Lock()
		c.readErr = err
		pending := c.pending
		c.pending = map[int]*Call{}
		// Go consulta done bajo el mismo cerrojo: ninguna petición nueva
		// puede quedar registrada sin que nadie la complete.
		close(c.done)
		c.mu.Unlock()
		dead := c.deadErr()
		for _, call := range pending {
			call.err = dead
			close(call.done)
		}
		c.cancel()
		c.events.finish()
	}()
	for {
		var body []byte
		body, err = readFrame(c.br)
		if err != nil {
			var pe *protocolError
			if errors.As(err, &pe) {
				c.malformed.Add(1)
				continue
			}
			return
		}
		var m incoming
		if json.Unmarshal(body, &m) != nil {
			c.malformed.Add(1)
			continue
		}
		c.dispatch(&m)
	}
}

func (c *Client) dispatch(m *incoming) {
	switch m.Type {
	case "response":
		c.mu.Lock()
		call, ok := c.pending[m.RequestSeq]
		c.mu.Unlock()
		if !ok {
			return // respuesta a una petición ya cancelada o a un cancel
		}
		if m.Success {
			c.finish(call, m.Body, nil)
			return
		}
		c.finish(call, nil, &ResponseError{Command: call.command, Message: errorMessage(m)})
	case "event":
		c.handleEvent(m)
	case "request":
		go c.answerRequest(m)
	default:
		c.malformed.Add(1)
	}
}

// errorMessage elige el texto más informativo de una respuesta fallida: el
// message y, si existe, body.error.format.
func errorMessage(m *incoming) string {
	var b struct {
		Error struct {
			Format string `json:"format"`
		} `json:"error"`
	}
	if len(m.Body) > 0 {
		_ = json.Unmarshal(m.Body, &b)
	}
	switch {
	case m.Message != "" && b.Error.Format != "" && m.Message != b.Error.Format:
		return m.Message + ": " + b.Error.Format
	case m.Message != "":
		return m.Message
	case b.Error.Format != "":
		return b.Error.Format
	}
	return "sin detalle"
}

func (c *Client) handleEvent(m *incoming) {
	if m.Event == "" {
		c.malformed.Add(1)
		return
	}
	ev := parseEvent(m.Event, m.Body)
	if _, ok := ev.(InitializedEvent); ok {
		c.initOnce.Do(func() { close(c.initialized) })
	}
	c.mu.Lock()
	hook := c.hook
	c.mu.Unlock()
	if hook != nil {
		hook(ev)
	}
	c.events.push(ev)
}

// answerRequest responde a las peticiones inversas del adaptador.
func (c *Client) answerRequest(m *incoming) {
	resp := outResponse{Type: "response", RequestSeq: m.Seq, Command: m.Command}
	c.mu.Lock()
	c.nextSeq++
	resp.Seq = c.nextSeq
	c.mu.Unlock()

	switch {
	case m.Command == "runInTerminal" && c.opts.RunInTerminal != nil:
		var args RunInTerminalArgs
		if err := json.Unmarshal(m.Arguments, &args); err != nil {
			resp.Message = "argumentos ilegibles"
			break
		}
		res, err := c.opts.RunInTerminal(c.ctx, args)
		if err != nil {
			resp.Message = err.Error()
			break
		}
		resp.Success = true
		resp.Body, _ = json.Marshal(res)
	default:
		resp.Message = "petición no soportada: " + m.Command
	}
	_ = c.send(resp) // si falla, la conexión ya está cayendo
}

// Close cierra la conexión: cierra la escritura hacia el adaptador y, si hay
// proceso, espera su salida (solo si ya hubo un disconnect correcto) y mata
// el grupo completo si sigue vivo. Es idempotente; devuelve el primer
// problema encontrado (el cierre se completa igualmente).
func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.shutdown() })
	return c.closeErr
}

func (c *Client) shutdown() error {
	timeout := c.opts.CloseTimeout
	if timeout <= 0 {
		timeout = DefaultCloseTimeout
	}
	c.mu.Lock()
	c.closing = true
	graceful := c.disconnected
	c.mu.Unlock()

	var first error
	keep := func(err error) {
		if first == nil && err != nil {
			first = err
		}
	}
	if cl, ok := c.w.(io.Closer); ok {
		_ = cl.Close() // EOF en stdin: el adaptador debería salir
	}
	wait := time.Duration(0)
	if graceful {
		wait = timeout
	}
	exited := c.waitExit(wait)
	if c.proc != nil {
		if !exited && graceful {
			keep(errors.New("dap: el adaptador no salió a tiempo; se mata el grupo"))
		}
		keep(c.proc.Kill())
		_ = c.proc.Close() // desbloquea la lectura si un descendiente retiene el pipe
	} else if !exited {
		if cl, ok := c.r.(io.Closer); ok {
			_ = cl.Close()
		}
	}
	select {
	case <-c.done:
	case <-time.After(time.Second):
		keep(errors.New("dap: la lectura no terminó"))
	}
	c.events.stop()
	return first
}

// waitExit espera hasta d a que el adaptador termine: su proceso si lo hay
// y, si no, el fin del flujo de lectura.
func (c *Client) waitExit(d time.Duration) bool {
	var ch <-chan struct{} = c.done
	if c.proc != nil {
		ch = c.proc.Done()
	}
	select {
	case <-ch:
		return true
	default:
	}
	if d <= 0 {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	}
}
