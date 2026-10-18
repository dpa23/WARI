package lsp

import (
	"bufio"
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeServer es un servidor LSP mínimo en el propio test, conectado por
// pipes. handle se ejecuta en una goroutine por mensaje con id para poder
// bloquear una petición sin frenar a las demás.
type fakeServer struct {
	t      *testing.T
	br     *bufio.Reader
	out    io.WriteCloser
	wmu    sync.Mutex
	handle func(f *fakeServer, m message)

	mu  sync.Mutex
	got []message
	end chan struct{} // se cierra cuando el servidor deja de leer
}

// newPair devuelve cliente y servidor conectados. El servidor ya está
// leyendo; al cerrar el cliente (stdin) el servidor termina y cierra su
// salida.
func newPair(t *testing.T, opts Options, handle func(f *fakeServer, m message)) (*Client, *fakeServer) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	f := &fakeServer{t: t, br: bufio.NewReader(c2sR), out: s2cW, handle: handle, end: make(chan struct{})}
	go f.loop()
	c := NewClient(s2cR, c2sW, opts)
	t.Cleanup(func() { c.Close() })
	return c, f
}

func (f *fakeServer) loop() {
	defer close(f.end)
	defer f.out.Close()
	for {
		body, err := readFrame(f.br)
		if err != nil {
			var pe *protocolError
			if asProtocol(err, &pe) {
				continue
			}
			return
		}
		var m message
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		f.mu.Lock()
		f.got = append(f.got, m)
		f.mu.Unlock()
		if f.handle != nil && m.Method != "" {
			if m.ID != nil {
				go f.handle(f, m)
			} else {
				f.handle(f, m)
			}
		}
	}
}

func asProtocol(err error, pe **protocolError) bool {
	p, ok := err.(*protocolError)
	if ok {
		*pe = p
	}
	return ok
}

// write envía una trama cruda (para mensajes malformados).
func (f *fakeServer) write(raw string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.out.Write([]byte(raw))
}

func (f *fakeServer) send(v any) {
	body, err := json.Marshal(v)
	if err != nil {
		f.t.Error(err)
		return
	}
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.out.Write(encodeFrame(body))
}

func (f *fakeServer) reply(m message, result any) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
}

func (f *fakeServer) replyErr(m message, code int, msg string) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": code, "message": msg}})
}

func (f *fakeServer) notify(method string, params any) {
	f.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// waitFor espera el n-ésimo (desde 1) mensaje recibido con ese método.
func (f *fakeServer) waitFor(method string, n int) message {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		k := 0
		for _, m := range f.got {
			if m.Method == method {
				k++
				if k == n {
					f.mu.Unlock()
					return m
				}
			}
		}
		f.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("el servidor no recibió %s #%d", method, n)
	return message{}
}

// methods devuelve los métodos recibidos, en orden.
func (f *fakeServer) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.got {
		out = append(out, m.Method)
	}
	return out
}

// defaultHandle responde initialize y shutdown como un servidor real.
func defaultHandle(extra func(f *fakeServer, m message) bool) func(f *fakeServer, m message) {
	return func(f *fakeServer, m message) {
		if extra != nil && extra(f, m) {
			return
		}
		switch m.Method {
		case "initialize":
			f.reply(m, map[string]any{"capabilities": map[string]any{
				"completionProvider": map[string]any{"triggerCharacters": []string{"."}},
				"definitionProvider": true,
			}})
		case "shutdown":
			f.reply(m, nil)
		case "exit":
			f.out.Close()
		}
	}
}

func idNum(t *testing.T, m message) int64 {
	t.Helper()
	n, err := strconv.ParseInt(string(*m.ID), 10, 64)
	if err != nil {
		t.Fatalf("id no numérico: %s", *m.ID)
	}
	return n
}

// fakeProc es un Process falso para probar Close y el gestor sin lanzar nada.
type fakeProc struct {
	stdin  io.WriteCloser
	stdout io.Reader
	pgid   int
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	kills  int
	closes int
	onKill func()
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeProc) Stdout() io.Reader     { return p.stdout }
func (p *fakeProc) PGID() int             { return p.pgid }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Stderr() string        { return "" }
func (p *fakeProc) Close() error {
	p.mu.Lock()
	p.closes++
	p.mu.Unlock()
	if c, ok := p.stdout.(io.Closer); ok {
		c.Close() // como proc.Close: desbloquea al lector
	}
	return nil
}
func (p *fakeProc) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	if p.onKill != nil {
		p.onKill()
	}
	p.exit()
	return nil
}
func (p *fakeProc) exit() { p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) killCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kills
}

// newProcPair conecta un fakeServer a un fakeProc. El proceso "sale" cuando
// el servidor termina de leer.
func newProcPair(t *testing.T, handle func(f *fakeServer, m message)) (*fakeProc, *fakeServer) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	f := &fakeServer{t: t, br: bufio.NewReader(c2sR), out: s2cW, handle: handle, end: make(chan struct{})}
	p := &fakeProc{stdin: c2sW, stdout: s2cR, pgid: 0, done: make(chan struct{})}
	go func() {
		f.loop()
		p.exit()
	}()
	return p, f
}
