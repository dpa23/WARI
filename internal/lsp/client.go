package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errores del cliente.
var (
	// ErrClosed indica que la conexión terminó o se está cerrando.
	ErrClosed = errors.New("lsp: conexión cerrada")
	// ErrNotOpen indica un didChange sobre un documento sin didOpen.
	ErrNotOpen = errors.New("lsp: documento no abierto")
	// ErrNoProcess indica que el cliente no está asociado a un proceso.
	ErrNoProcess = errors.New("lsp: sin proceso asociado")
)

// DefaultShutdownTimeout es el plazo de Close para shutdown y salida del
// proceso antes de forzar el Kill del grupo.
const DefaultShutdownTimeout = 3 * time.Second

// Options configura un Client.
type Options struct {
	// ShutdownTimeout acota la espera de Close; 0 usa DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
	// ClientName y ClientVersion se envían en clientInfo; por defecto "wari".
	ClientName    string
	ClientVersion string
	// OnNotify recibe las notificaciones del servidor que el cliente no
	// procesa (window/logMessage, $/progress...). Se llama desde la goroutine
	// de lectura: no debe bloquear.
	OnNotify func(method string, params json.RawMessage)
}

// Client es una conexión LSP. Todos los métodos son seguros para uso
// concurrente.
type Client struct {
	opts Options
	proc Process

	r  io.Reader
	br *bufio.Reader
	w  io.Writer

	writeMu sync.Mutex // serializa las escrituras de tramas completas
	docMu   sync.Mutex // mantiene ordenadas las versiones de didChange

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]chan *message
	docs     map[string]int
	diagLast map[string][]Diagnostic
	diagSubs map[string]map[*diagSub]struct{}
	closing  bool
	readErr  error
	inited   bool
	caps     ServerCapabilities

	done      chan struct{} // se cierra cuando termina la lectura
	closeOnce sync.Once
	closeErr  error
}

// NewClient crea un cliente sobre un transporte ya abierto e inicia la
// lectura. No envía initialize: llama a Initialize.
func NewClient(r io.Reader, w io.Writer, opts Options) *Client {
	c := &Client{
		opts:     opts,
		r:        r,
		br:       bufio.NewReaderSize(r, 64<<10),
		w:        w,
		pending:  map[int64]chan *message{},
		docs:     map[string]int{},
		diagLast: map[string][]Diagnostic{},
		diagSubs: map[string]map[*diagSub]struct{}{},
		done:     make(chan struct{}),
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

// Done se cierra cuando la conexión terminó (cierre ordenado o caída del
// servidor).
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

// Capabilities devuelve las capacidades anunciadas por el servidor (vacías
// antes de Initialize).
func (c *Client) Capabilities() ServerCapabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// Initialize ejecuta initialize e initialized. rootURI puede ser "" para un
// servidor sin carpeta de trabajo. ctx acota la espera de la respuesta
// (jdtls puede tardar decenas de segundos en el primer arranque).
func (c *Client) Initialize(ctx context.Context, rootURI string) error {
	name, version := c.opts.ClientName, c.opts.ClientVersion
	if name == "" {
		name = "wari"
	}
	params := map[string]any{
		"processId":  os.Getpid(),
		"clientInfo": map[string]any{"name": name, "version": version},
		"rootUri":    nilIfEmpty(rootURI),
		"capabilities": map[string]any{
			"general": map[string]any{"positionEncodings": []string{"utf-16"}},
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": false, "dynamicRegistration": false},
				"completion": map[string]any{
					"completionItem": map[string]any{
						"snippetSupport":      false,
						"documentationFormat": []string{"plaintext"},
					},
				},
				"definition":         map[string]any{"linkSupport": true},
				"publishDiagnostics": map[string]any{},
			},
		},
	}
	if rootURI != "" {
		params["workspaceFolders"] = []map[string]string{{"uri": rootURI, "name": baseName(rootURI)}}
	} else {
		params["workspaceFolders"] = nil
	}
	var res struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := c.rpc(ctx, "initialize", params, &res); err != nil {
		return err
	}
	caps := ServerCapabilities{Raw: res.Capabilities}
	var flags struct {
		Completion json.RawMessage `json:"completionProvider"`
		Definition json.RawMessage `json:"definitionProvider"`
	}
	if json.Unmarshal(res.Capabilities, &flags) == nil {
		caps.Completion = providerOn(flags.Completion)
		caps.Definition = providerOn(flags.Definition)
	}
	c.mu.Lock()
	c.caps = caps
	c.inited = true
	c.mu.Unlock()
	return c.notify("initialized", struct{}{})
}

// providerOn interpreta un "xProvider": ausente, null o false es apagado.
func providerOn(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "false"
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func baseName(uri string) string {
	uri = strings.TrimRight(uri, "/")
	if i := strings.LastIndex(uri, "/"); i >= 0 {
		return uri[i+1:]
	}
	return uri
}

// DidOpen abre un documento con la versión 1.
func (c *Client) DidOpen(uri, languageID, text string) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	c.docMu.Lock()
	defer c.docMu.Unlock()
	c.mu.Lock()
	c.docs[uri] = 1
	c.mu.Unlock()
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": languageID, "version": 1, "text": text,
		},
	})
}

// DidChange reemplaza el contenido completo (sincronización full) y sube la
// versión del documento.
func (c *Client) DidChange(uri, text string) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	c.docMu.Lock()
	defer c.docMu.Unlock()
	c.mu.Lock()
	v, ok := c.docs[uri]
	if ok {
		v++
		c.docs[uri] = v
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotOpen, uri)
	}
	return c.notify("textDocument/didChange", map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": v},
		"contentChanges": []map[string]any{{"text": text}},
	})
}

// DidClose cierra el documento y descarta sus diagnósticos guardados.
func (c *Client) DidClose(uri string) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	c.docMu.Lock()
	defer c.docMu.Unlock()
	c.mu.Lock()
	delete(c.docs, uri)
	delete(c.diagLast, uri)
	c.mu.Unlock()
	return c.notify("textDocument/didClose", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
}

// Complete pide sugerencias de autocompletado en pos.
func (c *Client) Complete(ctx context.Context, uri string, pos Position) ([]CompletionItem, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err := c.rpc(ctx, "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	}, &raw)
	if err != nil {
		return nil, err
	}
	return parseCompletion(raw), nil
}

// Definition pide la definición del símbolo en pos. Normaliza Location,
// []Location y []LocationLink a una lista de ubicaciones.
func (c *Client) Definition(ctx context.Context, uri string, pos Position) ([]Location, error) {
	if err := c.checkOpen(); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	err := c.rpc(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
	}, &raw)
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

func (c *Client) checkOpen() error {
	c.mu.Lock()
	closing := c.closing
	c.mu.Unlock()
	if closing {
		return ErrClosed
	}
	select {
	case <-c.done:
		return c.deadErr()
	default:
		return nil
	}
}

// rpc envía una petición y espera su respuesta. Si ctx se cancela antes,
// envía $/cancelRequest y devuelve ctx.Err().
func (c *Client) rpc(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ch := make(chan *message, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return c.deadErr()
	default:
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	idRaw := json.RawMessage(strconv.FormatInt(id, 10))
	req := &message{ID: &idRaw, Method: method}
	if err := c.setParams(req, params); err != nil {
		c.forget(id)
		return err
	}
	if err := c.send(req); err != nil {
		c.forget(id)
		return err
	}

	select {
	case m := <-ch:
		return decodeResponse(m, result)
	case <-ctx.Done():
		c.forget(id)
		// La cancelación es de mejor esfuerzo y no debe retener al llamador.
		go c.notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	case <-c.done:
		select { // una respuesta ya recibida gana a la caída
		case m := <-ch:
			return decodeResponse(m, result)
		default:
		}
		c.forget(id)
		return c.deadErr()
	}
}

func decodeResponse(m *message, result any) error {
	if m.Error != nil {
		return m.Error
	}
	if result == nil || len(m.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Result, result); err != nil {
		return fmt.Errorf("lsp: respuesta ilegible: %w", err)
	}
	return nil
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) notify(method string, params any) error {
	m := &message{Method: method}
	if err := c.setParams(m, params); err != nil {
		return err
	}
	return c.send(m)
}

func (c *Client) setParams(m *message, params any) error {
	if params == nil {
		return nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("lsp: serializar parámetros de %s: %w", m.Method, err)
	}
	m.Params = raw
	return nil
}

// send serializa y escribe una trama completa.
func (c *Client) send(m *message) error {
	m.JSONRPC = "2.0"
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("lsp: serializar mensaje: %w", err)
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
// formados se ignoran.
func (c *Client) readLoop() {
	var err error
	defer func() {
		c.mu.Lock()
		c.readErr = err
		for uri, subs := range c.diagSubs {
			for s := range subs {
				close(s.ch)
			}
			delete(c.diagSubs, uri)
		}
		c.mu.Unlock()
		close(c.done)
	}()
	for {
		var body []byte
		body, err = readFrame(c.br)
		if err != nil {
			var pe *protocolError
			if errors.As(err, &pe) {
				continue
			}
			return
		}
		var m message
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		c.dispatch(&m)
	}
}

func (c *Client) dispatch(m *message) {
	hasID := m.ID != nil && string(*m.ID) != "null"
	switch {
	case m.Method != "" && hasID:
		go c.answerServerRequest(m)
	case m.Method != "":
		c.handleNotification(m)
	case hasID:
		var id int64
		if json.Unmarshal(*m.ID, &id) != nil {
			return
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ok {
			ch <- m // el canal tiene capacidad 1 y solo se envía una vez
		}
	}
}

func (c *Client) handleNotification(m *message) {
	if m.Method == "textDocument/publishDiagnostics" {
		var p struct {
			URI         string       `json:"uri"`
			Diagnostics []Diagnostic `json:"diagnostics"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.URI != "" {
			c.publish(p.URI, p.Diagnostics)
		}
		return
	}
	if c.opts.OnNotify != nil {
		c.opts.OnNotify(m.Method, m.Params)
	}
}

// answerServerRequest responde a las peticiones del servidor. Un servidor
// real (jdtls) las espera antes de seguir; sin soporte devolvemos el valor
// neutro o MethodNotFound.
func (c *Client) answerServerRequest(m *message) {
	resp := &message{ID: m.ID}
	switch m.Method {
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(m.Params, &p)
		nulls := make([]any, len(p.Items))
		raw, _ := json.Marshal(nulls)
		resp.Result = raw
	case "client/registerCapability", "client/unregisterCapability",
		"window/workDoneProgress/create":
		resp.Result = json.RawMessage("null")
	case "workspace/workspaceFolders":
		resp.Result = json.RawMessage("null")
	default:
		resp.Error = &ResponseError{Code: codeMethodNotFound, Message: "método no soportado: " + m.Method}
	}
	_ = c.send(resp) // si falla, la conexión ya está cayendo
}

// Close cierra la sesión: shutdown y exit con plazo, cierra stdin y, si hay
// proceso, espera su salida y mata el grupo completo si sigue vivo. Es
// idempotente; devuelve el primer problema encontrado (el cierre se completa
// igualmente).
func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.shutdown() })
	return c.closeErr
}

func (c *Client) shutdown() error {
	timeout := c.opts.ShutdownTimeout
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	c.mu.Lock()
	inited := c.inited
	c.closing = true
	c.mu.Unlock()

	var first error
	keep := func(err error) {
		if first == nil && err != nil {
			first = err
		}
	}
	graceful := inited && c.Err() == nil
	if graceful {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := c.rpc(ctx, "shutdown", nil, nil)
		cancel()
		if err != nil {
			keep(fmt.Errorf("lsp: shutdown: %w", err))
			graceful = false
		} else {
			keep(c.notify("exit", nil))
		}
	}
	if cl, ok := c.w.(io.Closer); ok {
		_ = cl.Close() // EOF en stdin: el servidor debería salir
	}

	// Espera de salida solo tras un shutdown limpio; si no, se fuerza ya.
	wait := time.Duration(0)
	if graceful {
		wait = timeout
	}
	exited := c.waitExit(wait)
	if c.proc != nil {
		if !exited {
			keep(errors.New("lsp: el servidor no salió a tiempo; se mata el grupo"))
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
		keep(errors.New("lsp: la lectura no terminó"))
	}
	return first
}

// waitExit espera hasta d a que el servidor termine: su proceso si lo hay y,
// si no, el fin del flujo de lectura.
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
