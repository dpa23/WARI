package dap

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// req es una petición recibida por el adaptador falso.
type req struct {
	Seq       int
	Command   string
	Arguments json.RawMessage
}

// fakeAdapter es un adaptador DAP mínimo en el propio test. handle se ejecuta
// en una goroutine por petición para poder bloquear una sin frenar a las
// demás.
type fakeAdapter struct {
	br  *bufio.Reader
	out io.Writer
	cl  io.Closer // cierra el transporte

	wmu  sync.Mutex
	seq  int
	mu   sync.Mutex
	got  []req
	resp []map[string]any // respuestas del cliente a peticiones inversas
	hold sync.WaitGroup

	handle func(f *fakeAdapter, r req)
}

func newFakeAdapter(in io.Reader, out io.Writer, cl io.Closer, handle func(f *fakeAdapter, r req)) *fakeAdapter {
	f := &fakeAdapter{br: bufio.NewReader(in), out: out, cl: cl, handle: handle}
	return f
}

// serve lee hasta EOF y después cierra el transporte.
func (f *fakeAdapter) serve() {
	defer f.hold.Wait()
	defer f.cl.Close()
	for {
		body, err := readFrame(f.br)
		if err != nil {
			if _, ok := err.(*protocolError); ok {
				continue
			}
			return
		}
		var m struct {
			Seq       int             `json:"seq"`
			Type      string          `json:"type"`
			Command   string          `json:"command"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		if m.Type == "response" {
			var r map[string]any
			json.Unmarshal(body, &r)
			f.mu.Lock()
			f.resp = append(f.resp, r)
			f.mu.Unlock()
			continue
		}
		if m.Type != "request" {
			continue
		}
		r := req{m.Seq, m.Command, m.Arguments}
		f.mu.Lock()
		f.got = append(f.got, r)
		f.mu.Unlock()
		f.hold.Add(1)
		go func() {
			defer f.hold.Done()
			f.handle(f, r)
		}()
	}
}

func (f *fakeAdapter) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.got {
		out = append(out, r.Command)
	}
	return out
}

func (f *fakeAdapter) count(cmd string) int {
	n := 0
	for _, c := range f.commands() {
		if c == cmd {
			n++
		}
	}
	return n
}

func (f *fakeAdapter) raw(s string) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	io.WriteString(f.out, s)
}

func (f *fakeAdapter) send(m map[string]any) {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	f.seq++
	m["seq"] = f.seq
	body, _ := json.Marshal(m)
	f.out.Write(encodeFrame(body))
}

func (f *fakeAdapter) respond(r req, body any) {
	m := map[string]any{"type": "response", "request_seq": r.Seq, "success": true, "command": r.Command}
	if body != nil {
		m["body"] = body
	}
	f.send(m)
}

func (f *fakeAdapter) fail(r req, msg string) {
	f.send(map[string]any{"type": "response", "request_seq": r.Seq, "success": false, "command": r.Command, "message": msg})
}

func (f *fakeAdapter) event(name string, body any) {
	m := map[string]any{"type": "event", "event": name}
	if body != nil {
		m["body"] = body
	}
	f.send(m)
}

// pipePair devuelve un cliente conectado por pipes a un adaptador falso.
func pipePair(t *testing.T, opts Options, handle func(f *fakeAdapter, r req)) (*Client, *fakeAdapter) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	f := newFakeAdapter(c2sR, s2cW, s2cW, handle)
	go f.serve()
	c := NewClient(s2cR, c2sW, opts)
	t.Cleanup(func() { c.Close() })
	return c, f
}

// tcpPair hace lo mismo sobre un socket local.
func tcpPair(t *testing.T, opts Options, handle func(f *fakeAdapter, r req)) (*Client, *fakeAdapter) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("sin TCP local: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	fch := make(chan *fakeAdapter, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			fch <- nil
			return
		}
		f := newFakeAdapter(conn, conn, conn, handle)
		fch <- f
		f.serve()
	}()
	c, err := ConnectTCP(t.Context(), ln.Addr().String(), ConnectOptions{Client: opts})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	f := <-fch
	if f == nil {
		t.Fatal("el adaptador no aceptó")
	}
	return c, f
}

// scenario es el adaptador de la sesión completa: launch no responde hasta
// configurationDone, como muchos adaptadores reales.
func scenario() func(f *fakeAdapter, r req) {
	var mu sync.Mutex
	var launch *req
	return func(f *fakeAdapter, r req) {
		switch r.Command {
		case "initialize":
			f.respond(r, map[string]any{"supportsConfigurationDoneRequest": true, "supportsCancelRequest": true})
			f.event("initialized", nil)
		case "launch", "attach":
			mu.Lock()
			launch = &r
			mu.Unlock()
		case "setBreakpoints":
			var a struct {
				Source      Source           `json:"source"`
				Breakpoints []map[string]int `json:"breakpoints"`
			}
			json.Unmarshal(r.Arguments, &a)
			bps := []map[string]any{}
			for i, b := range a.Breakpoints {
				// El adaptador mueve la línea 10 a la 11.
				line := b["line"]
				if line == 10 {
					line = 11
				}
				bps = append(bps, map[string]any{"id": 100 + i, "verified": true, "line": line})
			}
			f.respond(r, map[string]any{"breakpoints": bps})
		case "configurationDone":
			f.respond(r, nil)
			// Los manejadores corren en goroutines distintas: espera a que el
			// de launch haya anotado su petición.
			var l *req
			for i := 0; i < 400 && l == nil; i++ {
				mu.Lock()
				l = launch
				mu.Unlock()
				if l == nil {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if l != nil {
				f.respond(*l, nil)
			}
			f.event("stopped", map[string]any{"reason": "breakpoint", "threadId": 7, "hitBreakpointIds": []int{100}})
		case "threads":
			f.respond(r, map[string]any{"threads": []map[string]any{{"id": 7, "name": "main"}}})
		case "stackTrace":
			f.respond(r, map[string]any{"stackFrames": []map[string]any{
				{"id": 1, "name": "Main", "line": 11, "column": 1, "source": map[string]any{"path": "/p/Program.cs"}},
				{"id": 2, "name": "Run", "line": 4, "column": 1},
			}, "totalFrames": 2})
		case "scopes":
			f.respond(r, map[string]any{"scopes": []map[string]any{
				{"name": "Locals", "variablesReference": 1000},
				{"name": "Statics", "variablesReference": 2000, "expensive": true},
			}})
		case "variables":
			var a struct {
				Ref int `json:"variablesReference"`
			}
			json.Unmarshal(r.Arguments, &a)
			switch a.Ref {
			case 1000:
				f.respond(r, map[string]any{"variables": []map[string]any{
					{"name": "x", "value": "42", "type": "int"},
					{"name": "obj", "value": "{Foo}", "type": "Foo", "variablesReference": 1001},
				}})
			case 1001:
				f.respond(r, map[string]any{"variables": []map[string]any{
					{"name": "name", "value": "\"a\"", "type": "string"},
					{"name": "inner", "value": "{Bar}", "type": "Bar", "variablesReference": 1002},
				}})
			case 1002:
				f.respond(r, map[string]any{"variables": []map[string]any{
					{"name": "deep", "value": "true", "type": "bool"},
				}})
			default:
				f.fail(r, "referencia desconocida")
			}
		case "evaluate":
			f.respond(r, map[string]any{"result": "43", "type": "int"})
		case "next":
			f.respond(r, nil)
			f.event("stopped", map[string]any{"reason": "step", "threadId": 7})
		case "continue":
			f.respond(r, map[string]any{"allThreadsContinued": true})
			f.event("output", map[string]any{"category": "stdout", "output": "hola\n"})
			f.event("exited", map[string]any{"exitCode": 0})
			f.event("terminated", nil)
		case "disconnect":
			f.respond(r, nil)
			f.cl.Close()
		default:
			f.fail(r, "no soportado: "+r.Command)
		}
	}
}

func waitEvent[T Event](t *testing.T, c <-chan Event) T {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-c:
			if !ok {
				var zero T
				t.Fatalf("canal de eventos cerrado esperando %T", zero)
			}
			if v, ok := ev.(T); ok {
				return v
			}
		case <-timeout:
			var zero T
			t.Fatalf("timeout esperando %T", zero)
		}
	}
}
