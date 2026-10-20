package layout

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/dap"
	"github.com/dpa23/wari/internal/proc"
	"github.com/dpa23/wari/internal/tui/theme"
)

// TestMain convierte el binario de test en un adaptador DAP falso cuando se
// lanza con WARI_LAYOUT_DAP=1: así se prueba el arranque real, con grupo de
// procesos propio, a través de internal/proc.
func TestMain(m *testing.M) {
	if os.Getenv("WARI_LAYOUT_DAP") != "1" {
		theme.SetDark(true) // sin consulta de color de fondo en Init
		os.Exit(m.Run())
	}
	if os.Getenv("WARI_LAYOUT_DAP_CHILD") == "1" {
		// Un descendiente en el mismo grupo: Stop debe llevárselo también.
		if err := exec.Command("sleep", "120").Start(); err != nil {
			os.Exit(3)
		}
	}
	f := newFakeDAP(os.Getenv("WARI_LAYOUT_DAP_MAIN"), os.Getenv("WARI_LAYOUT_DAP_HELPER"))
	f.serve(os.Stdin, os.Stdout, func() { os.Stdout.Close() })
	os.Exit(0)
}

// fakeDAP es un adaptador DAP en el propio test. Escenario: launch no
// responde hasta configurationDone; después se detiene en el primer
// breakpoint (o en la línea 1) con dos marcos: Main en main y Run en helper.
// next avanza una línea y vuelve a detenerse; continue termina el depurado.
//
// Variables del marco 1: Locals { x=42, obj {name, inner {deep}} } y Statics
// (costoso, sin expandir). El marco 2 tiene Locals { y=7 }.
type fakeDAP struct {
	main, helper string

	mu         sync.Mutex
	cmds       []string
	launchArgs json.RawMessage
	bps        map[string][]int
	line       int
}

func newFakeDAP(main, helper string) *fakeDAP {
	return &fakeDAP{main: main, helper: helper, bps: map[string][]int{}}
}

func (f *fakeDAP) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeDAP) count(cmd string) int {
	n := 0
	for _, c := range f.commands() {
		if c == cmd {
			n++
		}
	}
	return n
}

func (f *fakeDAP) breakpoints(path string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.bps[path]...)
}

func dapReadFrame(br *bufio.Reader) ([]byte, error) {
	n := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			n, _ = strconv.Atoi(v)
		}
	}
	if n < 0 {
		return nil, fmt.Errorf("falta Content-Length")
	}
	body := make([]byte, n)
	_, err := io.ReadFull(br, body)
	return body, err
}

func (f *fakeDAP) serve(r io.Reader, w io.Writer, closeOut func()) {
	br := bufio.NewReader(r)
	var wmu sync.Mutex
	seq := 0
	send := func(m map[string]any) {
		wmu.Lock()
		defer wmu.Unlock()
		seq++
		m["seq"] = seq
		body, _ := json.Marshal(m)
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}
	respond := func(req int, cmd string, body any) {
		m := map[string]any{"type": "response", "request_seq": req, "success": true, "command": cmd}
		if body != nil {
			m["body"] = body
		}
		send(m)
	}
	event := func(name string, body any) {
		m := map[string]any{"type": "event", "event": name}
		if body != nil {
			m["body"] = body
		}
		send(m)
	}
	stopped := func(reason string) {
		event("stopped", map[string]any{"reason": reason, "threadId": 7})
	}
	vars := func(vs ...map[string]any) map[string]any { return map[string]any{"variables": vs} }

	launchSeq := 0
	for {
		body, err := dapReadFrame(br)
		if err != nil {
			break
		}
		var m struct {
			Seq       int             `json:"seq"`
			Type      string          `json:"type"`
			Command   string          `json:"command"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(body, &m) != nil || m.Type != "request" {
			continue
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, m.Command)
		f.mu.Unlock()
		switch m.Command {
		case "initialize":
			respond(m.Seq, m.Command, map[string]any{"supportsConfigurationDoneRequest": true})
			event("initialized", nil)
		case "launch", "attach":
			f.mu.Lock()
			f.launchArgs = m.Arguments
			f.mu.Unlock()
			launchSeq = m.Seq
		case "setBreakpoints":
			var a struct {
				Source      dap.Source       `json:"source"`
				Breakpoints []map[string]int `json:"breakpoints"`
			}
			json.Unmarshal(m.Arguments, &a)
			var lines []int
			res := []map[string]any{}
			for i, b := range a.Breakpoints {
				lines = append(lines, b["line"])
				res = append(res, map[string]any{"id": 100 + i, "verified": true, "line": b["line"]})
			}
			f.mu.Lock()
			f.bps[a.Source.Path] = lines
			f.mu.Unlock()
			respond(m.Seq, m.Command, map[string]any{"breakpoints": res})
		case "configurationDone":
			respond(m.Seq, m.Command, nil)
			if launchSeq != 0 {
				respond(launchSeq, "launch", nil)
			}
			f.mu.Lock()
			f.line = 1
			if l := f.bps[f.main]; len(l) > 0 {
				f.line = l[0]
			}
			f.mu.Unlock()
			stopped("breakpoint")
		case "threads":
			respond(m.Seq, m.Command, map[string]any{"threads": []map[string]any{{"id": 7, "name": "main"}}})
		case "stackTrace":
			f.mu.Lock()
			line := f.line
			f.mu.Unlock()
			respond(m.Seq, m.Command, map[string]any{"stackFrames": []map[string]any{
				{"id": 1, "name": "Main", "line": line, "column": 1, "source": map[string]any{"path": f.main}},
				{"id": 2, "name": "Run", "line": 2, "column": 1, "source": map[string]any{"path": f.helper}},
			}, "totalFrames": 2})
		case "scopes":
			var a struct {
				FrameID int `json:"frameId"`
			}
			json.Unmarshal(m.Arguments, &a)
			if a.FrameID == 2 {
				respond(m.Seq, m.Command, map[string]any{"scopes": []map[string]any{{"name": "Locals", "variablesReference": 3000}}})
				break
			}
			respond(m.Seq, m.Command, map[string]any{"scopes": []map[string]any{
				{"name": "Locals", "variablesReference": 1000},
				{"name": "Statics", "variablesReference": 2000, "expensive": true},
			}})
		case "variables":
			var a struct {
				Ref int `json:"variablesReference"`
			}
			json.Unmarshal(m.Arguments, &a)
			switch a.Ref {
			case 1000:
				respond(m.Seq, m.Command, vars(
					map[string]any{"name": "x", "value": "42", "type": "int"},
					map[string]any{"name": "obj", "value": "{Foo}", "type": "Foo", "variablesReference": 1001}))
			case 1001:
				respond(m.Seq, m.Command, vars(
					map[string]any{"name": "name", "value": "\"a\"", "type": "string"},
					map[string]any{"name": "inner", "value": "{Bar}", "type": "Bar", "variablesReference": 1002}))
			case 1002:
				respond(m.Seq, m.Command, vars(map[string]any{"name": "deep", "value": "true", "type": "bool"}))
			case 2000:
				respond(m.Seq, m.Command, vars(map[string]any{"name": "Count", "value": "3", "type": "int"}))
			case 3000:
				respond(m.Seq, m.Command, vars(map[string]any{"name": "y", "value": "7", "type": "int"}))
			default:
				send(map[string]any{"type": "response", "request_seq": m.Seq, "success": false, "command": m.Command, "message": "referencia desconocida"})
			}
		case "next", "stepIn", "stepOut":
			f.mu.Lock()
			f.line++
			f.mu.Unlock()
			respond(m.Seq, m.Command, nil)
			stopped("step")
		case "continue":
			respond(m.Seq, m.Command, map[string]any{"allThreadsContinued": true})
			event("output", map[string]any{"category": "stdout", "output": "hola desde el depurado\n"})
			event("exited", map[string]any{"exitCode": 0})
			event("terminated", nil)
		case "disconnect":
			respond(m.Seq, m.Command, nil)
			closeOut()
			return
		default:
			send(map[string]any{"type": "response", "request_seq": m.Seq, "success": false, "command": m.Command, "message": "no soportado"})
		}
	}
	closeOut()
}

// --- Lanzadores --------------------------------------------------------------------

// pipeProc es el "proceso" en memoria del adaptador falso.
type pipeProc struct {
	in    io.WriteCloser
	out   io.Reader
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	kills int
}

func (p *pipeProc) Stdin() io.WriteCloser { return p.in }
func (p *pipeProc) Stdout() io.Reader     { return p.out }
func (p *pipeProc) PGID() int             { return 0 }
func (p *pipeProc) Done() <-chan struct{} { return p.done }
func (p *pipeProc) Stderr() string        { return "" }
func (p *pipeProc) exit()                 { p.once.Do(func() { close(p.done) }) }
func (p *pipeProc) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	p.in.Close()
	p.exit()
	return nil
}
func (p *pipeProc) Close() error {
	if c, ok := p.out.(io.Closer); ok {
		c.Close()
	}
	return nil
}
func (p *pipeProc) killCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.kills
}

// fakeLauncher entrega el adaptador falso conectado por pipes y anota cómo se
// lanzó.
type fakeLauncher struct {
	f *fakeDAP

	mu       sync.Mutex
	launches int
	path     string
	args     []string
	dir      string
	proc     *pipeProc
}

func (l *fakeLauncher) launch(ctx context.Context, path string, args []string, dir string) (dap.Process, error) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	p := &pipeProc{in: c2sW, out: s2cR, done: make(chan struct{})}
	l.mu.Lock()
	l.launches++
	l.path, l.args, l.dir, l.proc = path, args, dir, p
	l.mu.Unlock()
	go func() {
		l.f.serve(c2sR, s2cW, func() { s2cW.Close() })
		p.exit()
	}()
	return p, nil
}

func (l *fakeLauncher) process() *pipeProc {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.proc
}

// realLauncher lanza el binario de test como adaptador de verdad (grupo de
// procesos propio, con un descendiente) a través de internal/proc.
type realLauncher struct {
	main, helper string
	mu           sync.Mutex
	p            *proc.Process
}

func (l *realLauncher) launch(ctx context.Context, path string, args []string, dir string) (dap.Process, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	env := append(os.Environ(), "WARI_LAYOUT_DAP=1", "WARI_LAYOUT_DAP_CHILD=1",
		"WARI_LAYOUT_DAP_MAIN="+l.main, "WARI_LAYOUT_DAP_HELPER="+l.helper)
	p, err := proc.Start(ctx, proc.Spec{Path: exe, Dir: dir, Env: env, Stdin: true, KillGrace: 300 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.p = p
	l.mu.Unlock()
	return p, nil
}

// installAdapter pone en el PATH un ejecutable "netcoredbg" (no se ejecuta:
// el Launcher inyectado lo sustituye; basta con que dap.Adapter.Detect lo halle).
func installAdapter(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("el adaptador falso es un guion de sh")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "netcoredbg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return p
}
