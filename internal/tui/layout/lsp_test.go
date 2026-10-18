package layout

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/index"
	"github.com/dpa23/wari/internal/lsp"
	"github.com/dpa23/wari/internal/tui/editorview"
	"github.com/dpa23/wari/internal/tui/filetree"
)

// --- Servidor LSP falso -----------------------------------------------------------
//
// fakeLSP es un servidor de lenguaje en el propio test, conectado por pipes
// como si fuera un proceso. Hace de Launcher del gestor real: el cliente, el
// gestor y el layout son los de verdad; solo falta el binario del servidor.

type fakeLSP struct {
	t *testing.T

	// Respuestas configurables (se leen bajo mu).
	complete func(uri, text string, pos lsp.Position) []map[string]any
	define   func(uri string, pos lsp.Position) any
	diag     func(uri, text string) []lsp.Diagnostic
	delay    time.Duration // retraso de las respuestas de completion
	pgid     int

	mu       sync.Mutex
	launches int
	procs    []*fakeLSPProc
	texts    map[string]string
	changes  int
	closed   []string
	lastPos  lsp.Position
}

func newFakeLSP(t *testing.T) *fakeLSP {
	return &fakeLSP{t: t, texts: map[string]string{}}
}

func (f *fakeLSP) launcher() lsp.Launcher {
	return func(ctx context.Context, path string, args []string, dir string) (lsp.Process, error) {
		c2sR, c2sW := io.Pipe()
		s2cR, s2cW := io.Pipe()
		p := &fakeLSPProc{stdin: c2sW, stdout: s2cR, pgid: f.pgid, done: make(chan struct{})}
		f.mu.Lock()
		f.launches++
		f.procs = append(f.procs, p)
		f.mu.Unlock()
		go func() {
			f.serve(bufio.NewReader(c2sR), s2cW)
			s2cW.Close()
			p.exit()
		}()
		return p, nil
	}
}

type fakeLSPProc struct {
	stdin  io.WriteCloser
	stdout io.Reader
	pgid   int
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	kills  int
}

func (p *fakeLSPProc) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeLSPProc) Stdout() io.Reader     { return p.stdout }
func (p *fakeLSPProc) PGID() int             { return p.pgid }
func (p *fakeLSPProc) Done() <-chan struct{} { return p.done }
func (p *fakeLSPProc) Stderr() string        { return "" }
func (p *fakeLSPProc) Close() error {
	if c, ok := p.stdout.(io.Closer); ok {
		c.Close()
	}
	return nil
}
func (p *fakeLSPProc) Kill() error {
	p.mu.Lock()
	p.kills++
	p.mu.Unlock()
	p.stdin.Close()
	p.exit()
	return nil
}
func (p *fakeLSPProc) exit() { p.once.Do(func() { close(p.done) }) }
func (p *fakeLSPProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

type rpcMsg struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

func readFrame(br *bufio.Reader) ([]byte, error) {
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

func (f *fakeLSP) serve(br *bufio.Reader, out io.Writer) {
	var wmu sync.Mutex
	send := func(v any) {
		body, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}
	reply := func(m rpcMsg, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}
	publish := func(uri, text string) {
		f.mu.Lock()
		fn := f.diag
		f.mu.Unlock()
		if fn == nil {
			return
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
			"params": map[string]any{"uri": uri, "diagnostics": fn(uri, text)}})
	}
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var m rpcMsg
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			reply(m, map[string]any{"capabilities": map[string]any{
				"completionProvider": map[string]any{"triggerCharacters": []string{"."}},
				"definitionProvider": true,
			}})
		case "shutdown":
			reply(m, nil)
		case "exit":
			return
		case "textDocument/didOpen":
			var p struct {
				TextDocument struct{ URI, Text string } `json:"textDocument"`
			}
			json.Unmarshal(m.Params, &p)
			f.mu.Lock()
			f.texts[p.TextDocument.URI] = p.TextDocument.Text
			f.mu.Unlock()
			publish(p.TextDocument.URI, p.TextDocument.Text)
		case "textDocument/didChange":
			var p struct {
				TextDocument   struct{ URI string }    `json:"textDocument"`
				ContentChanges []struct{ Text string } `json:"contentChanges"`
			}
			json.Unmarshal(m.Params, &p)
			f.mu.Lock()
			f.texts[p.TextDocument.URI] = p.ContentChanges[0].Text
			f.changes++
			f.mu.Unlock()
			publish(p.TextDocument.URI, p.ContentChanges[0].Text)
		case "textDocument/didClose":
			var p struct {
				TextDocument struct{ URI string } `json:"textDocument"`
			}
			json.Unmarshal(m.Params, &p)
			f.mu.Lock()
			f.closed = append(f.closed, p.TextDocument.URI)
			f.mu.Unlock()
		case "textDocument/completion", "textDocument/definition":
			var p struct {
				TextDocument struct{ URI string } `json:"textDocument"`
				Position     lsp.Position         `json:"position"`
			}
			json.Unmarshal(m.Params, &p)
			f.mu.Lock()
			f.lastPos = p.Position
			text, delay := f.texts[p.TextDocument.URI], f.delay
			compl, def := f.complete, f.define
			f.mu.Unlock()
			go func() {
				if m.Method == "textDocument/definition" {
					var res any
					if def != nil {
						res = def(p.TextDocument.URI, p.Position)
					}
					reply(m, res)
					return
				}
				time.Sleep(delay)
				var items []map[string]any
				if compl != nil {
					items = compl(p.TextDocument.URI, text, p.Position)
				}
				reply(m, items)
			}()
		}
	}
}

func (f *fakeLSP) snapshot() (launches, changes int, closed []string, text map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text = map[string]string{}
	for k, v := range f.texts {
		text[k] = v
	}
	return f.launches, f.changes, append([]string(nil), f.closed...), text
}

func (f *fakeLSP) position() lsp.Position {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPos
}

// --- Bucle de eventos mínimo -------------------------------------------------------

// sim hace de bucle de bubbletea: ejecuta cada Cmd en su goroutine y entrega
// el mensaje a update en la del test.
type sim struct {
	t    *testing.T
	m    Model
	msgs chan tea.Msg
}

func newSim(t *testing.T, m Model) *sim {
	return &sim{t: t, m: m, msgs: make(chan tea.Msg, 4096)}
}

func (s *sim) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case nil:
		case tea.BatchMsg:
			for _, c := range msg {
				s.run(c)
			}
		default:
			s.msgs <- msg
		}
	}()
}

// send aplica los mensajes (teclas) y lanza los Cmd que devuelvan.
func (s *sim) send(msgs ...tea.Msg) {
	for _, msg := range msgs {
		var cmd tea.Cmd
		s.m, cmd = s.m.update(msg)
		s.run(cmd)
	}
}

// until procesa mensajes hasta que cond sea cierta.
func (s *sim) until(desc string, cond func(Model) bool) {
	s.t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond(s.m) {
		select {
		case msg := <-s.msgs:
			var cmd tea.Cmd
			s.m, cmd = s.m.update(msg)
			s.run(cmd)
		case <-deadline:
			s.t.Fatalf("no se cumplió %q (msg=%q, lsp=%q)", desc, s.m.msg, s.m.lspInfo())
		}
	}
}

// quiet procesa mensajes durante d (para comprobar que algo NO ocurre).
func (s *sim) quiet(d time.Duration) {
	end := time.After(d)
	for {
		select {
		case msg := <-s.msgs:
			var cmd tea.Cmd
			s.m, cmd = s.m.update(msg)
			s.run(cmd)
		case <-end:
			return
		}
	}
}

func (s *sim) typeText(str string) {
	for _, r := range str {
		s.send(text(string(r)))
	}
}

// --- Entorno -------------------------------------------------------------------------

const demoJava = "class Demo {\n    void run(Obj obj) {\n        \n    }\n}\n"

// lspSetup instala (o no) un jdtls falso en el PATH, abre src/Demo.java con el
// cursor en la línea en blanco del método y devuelve el simulador.
func lspSetup(t *testing.T, installed bool, f *fakeLSP, lspOn bool) *sim {
	t.Helper()
	pathDir := t.TempDir()
	if installed {
		if err := os.WriteFile(filepath.Join(pathDir, "jdtls"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", pathDir)

	oldSync, oldReq, oldStat := lspSyncDelay, lspRequestTimeout, lspStatusEvery
	lspSyncDelay, lspRequestTimeout, lspStatusEvery = 30*time.Millisecond, 400*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { lspSyncDelay, lspRequestTimeout, lspStatusEvery = oldSync, oldReq, oldStat })

	root := project(t)
	file := write(t, root, "src/Demo.java", demoJava)
	ix := index.New(index.Options{Root: root, Ignore: filetree.IgnoreFunc(root)})
	ix.Refresh(context.Background())
	mgr := lsp.NewManager(context.Background(), f.launcher(), lsp.Options{ShutdownTimeout: 2 * time.Second})
	t.Cleanup(func() { mgr.Shutdown() })
	m := newLayout(t, Config{Root: root, File: file, Index: ix, LSP: mgr, LSPOn: lspOn}, 140, 30)
	m.ed.GoTo(2, 8)
	s := newSim(t, m)
	if lspOn {
		s.run(m.Init())
	}
	return s
}

func (s *sim) ready() {
	s.t.Helper()
	s.until("servidor listo", func(m Model) bool { return m.lsp.doc.client != nil })
}

func titles(m Model) []string { return paletteTitles(m) }

func methodsComplete(uri, text string, pos lsp.Position) []map[string]any {
	return []map[string]any{
		{"label": "setName(String)", "kind": 2, "detail": "void", "insertText": "setName"},
		{"label": "getName()", "kind": 2, "detail": "String", "insertText": "getName"},
		{"label": "length", "kind": 5, "detail": "int"},
	}
}

// --- Tests -----------------------------------------------------------------------------

func TestLSPOffByDefaultKeepsEditorUsable(t *testing.T) {
	f := newFakeLSP(t)
	s := lspSetup(t, true, f, false)
	if s.m.lsp.on || s.m.lspInfo() != "" {
		t.Fatalf("el LSP debe estar apagado: info=%q", s.m.lspInfo())
	}
	// Editar, completar con el índice y guardar funcionan igual.
	s.typeText("Dem")
	s.send(press('/', tea.ModAlt))
	if got := string(s.m.ed.Buffer().Line(2)); got != "        Demo" || s.m.pal.mode == palLSPComplete {
		t.Errorf("completar con el índice: línea %q msg=%q", got, s.m.msg)
	}
	s.send(press('s', tea.ModCtrl))
	s.until("guardado", func(m Model) bool { return strings.HasPrefix(m.msg, "guardado") })
	if b, _ := os.ReadFile(s.m.ed.Path()); !strings.Contains(string(b), "        Demo\n") {
		t.Errorf("no se guardó la edición: %q", b)
	}
	s.quiet(100 * time.Millisecond)
	if l, _, _, _ := f.snapshot(); l != 0 {
		t.Errorf("con el LSP apagado no se lanza nada (lanzamientos=%d)", l)
	}
	if bar := plain(lines(s.m)[s.m.h-1]); strings.Contains(bar, "LSP") {
		t.Errorf("la barra no debe mencionar el LSP apagado: %q", bar)
	}
	// Los diagnósticos sin LSP solo avisan.
	s.send(press('m', tea.ModAlt))
	if s.m.pal.open || !strings.Contains(s.m.msg, "LSP apagado") {
		t.Errorf("diagnostics sin LSP: msg=%q open=%v", s.m.msg, s.m.pal.open)
	}
}

func TestLSPNotInstalledWarnsAndFallsBackToIndex(t *testing.T) {
	f := newFakeLSP(t)
	s := lspSetup(t, false, f, false)
	s.send(press('l', tea.ModAlt))
	if !s.m.lsp.on {
		t.Fatal("alt+l debía encender el LSP")
	}
	if !strings.Contains(s.m.msg, "jdtls no está instalado") {
		t.Fatalf("aviso: %q", s.m.msg)
	}
	bar := plain(lines(s.m)[s.m.h-1])
	if !strings.Contains(bar, "jdtls no está instalado") {
		t.Errorf("barra: %q", bar)
	}
	// Pasado el aviso, la barra sigue diciendo el estado.
	s.send(press(tea.KeyRight, 0), press(tea.KeyLeft, 0))
	if bar := plain(lines(s.m)[s.m.h-1]); !strings.Contains(bar, "LSP jdtls · no instalado") {
		t.Errorf("barra: %q", bar)
	}
	// Completar cae al índice propio sin esperar a nada.
	s.typeText("Dem")
	s.send(press('/', tea.ModAlt))
	if got := string(s.m.ed.Buffer().Line(2)); got != "        Demo" || s.m.pal.mode == palLSPComplete {
		t.Errorf("sin servidor se completa con el índice: línea %q", got)
	}
	if l, _, _, _ := f.snapshot(); l != 0 {
		t.Errorf("lanzamientos=%d", l)
	}
}

func TestLSPCompletesObjectMethods(t *testing.T) {
	f := newFakeLSP(t)
	f.complete = methodsComplete
	s := lspSetup(t, true, f, false)

	s.send(press('l', tea.ModAlt))
	s.until("iniciando", func(m Model) bool { return strings.Contains(m.lspInfo(), "jdtls") })
	s.ready()
	s.until("RSS leído", func(m Model) bool { return len(m.lsp.servers) == 1 })
	if !strings.Contains(plain(lines(s.m)[s.m.h-1]), "LSP jdtls") {
		t.Errorf("la barra debe mostrar el servidor: %q", plain(lines(s.m)[s.m.h-1]))
	}

	s.typeText("obj.")
	s.send(press('/', tea.ModAlt))
	s.until("lista del LSP", func(m Model) bool { return m.pal.open && m.pal.mode == palLSPComplete })
	got := titles(s.m)
	// Orden por sortText o etiqueta.
	if len(got) != 3 || got[0] != "getName()" || got[1] != "length" || got[2] != "setName(String)" {
		t.Fatalf("sugerencias: %v", got)
	}
	// El servidor vio el texto con "obj." antes de la petición.
	_, _, _, texts := f.snapshot()
	if got := texts[lsp.FileURI(s.m.ed.Path())]; !strings.Contains(got, "obj.") {
		t.Errorf("el servidor no tenía el texto actual: %q", got)
	}
	if p := f.position(); p.Line != 2 || p.Character != 12 {
		t.Errorf("posición enviada: %+v (quería 2:12)", p)
	}
	checkDims(t, s.m, 140, 30, "completar LSP")

	// Elegir inserta el texto del servidor tras el punto, en un paso de deshacer.
	s.send(enter())
	if s.m.pal.open {
		t.Fatal("la lista debía cerrarse")
	}
	if got := string(s.m.ed.Buffer().Line(2)); got != "        obj.getName" {
		t.Errorf("línea tras completar: %q", got)
	}
	s.send(press('z', tea.ModCtrl))
	if got := string(s.m.ed.Buffer().Line(2)); got != "        obj." {
		t.Errorf("tras deshacer: %q", got)
	}
}

func TestLSPCompletionUsesPrefixAsFilter(t *testing.T) {
	f := newFakeLSP(t)
	f.complete = methodsComplete
	s := lspSetup(t, true, f, true)
	s.ready()
	s.typeText("obj.set")
	s.send(press('/', tea.ModAlt))
	s.until("lista", func(m Model) bool { return m.pal.open || strings.Contains(m.msg, "setName") })
	if s.m.pal.open {
		if got := titles(s.m); len(got) != 1 || got[0] != "setName(String)" {
			t.Fatalf("filtradas: %v", got)
		}
		s.send(enter())
	}
	if got := string(s.m.ed.Buffer().Line(2)); got != "        obj.setName" {
		t.Errorf("línea: %q", got)
	}
}

func TestLSPCompleteFallsBackToIndexWhenSlow(t *testing.T) {
	f := newFakeLSP(t)
	f.complete = methodsComplete
	f.delay = 3 * time.Second // más que lspRequestTimeout
	s := lspSetup(t, true, f, true)
	s.ready()
	s.typeText("Dem")
	s.send(press('/', tea.ModAlt))
	s.until("respaldo", func(m Model) bool { return strings.Contains(m.msg, "LSP no respondió") })
	if got := string(s.m.ed.Buffer().Line(2)); got != "        Demo" || s.m.pal.mode == palLSPComplete {
		t.Errorf("el servidor no respondió a tiempo: debía usarse el índice (línea %q)", got)
	}
}

func TestLSPCompleteEmptyAnswerFallsBack(t *testing.T) {
	f := newFakeLSP(t) // sin complete: devuelve lista vacía
	s := lspSetup(t, true, f, true)
	s.ready()
	s.typeText("obj.")
	s.send(press('/', tea.ModAlt))
	s.until("respaldo", func(m Model) bool { return strings.Contains(m.msg, "LSP sin sugerencias") })
}

func TestLSPSyncDebouncesAndConvertsUTF16(t *testing.T) {
	f := newFakeLSP(t)
	f.complete = methodsComplete
	s := lspSetup(t, true, f, true)
	s.ready()
	uri := lsp.FileURI(s.m.ed.Path())

	// Una línea con un carácter fuera del plano básico (2 unidades UTF-16).
	s.typeText("\"😀\"+obj.")
	s.until("texto sincronizado", func(Model) bool {
		_, _, _, texts := f.snapshot()
		return strings.Contains(texts[uri], "obj.")
	})
	s.quiet(120 * time.Millisecond)
	_, changes, _, texts := f.snapshot()
	if changes != 1 {
		t.Errorf("didChange=%d: las teclas seguidas deben fundirse en uno (debounce)", changes)
	}
	if texts[uri] != s.m.ed.Text() {
		t.Errorf("el servidor tiene %q, el editor %q", texts[uri], s.m.ed.Text())
	}

	s.send(press('/', tea.ModAlt))
	s.until("lista", func(m Model) bool { return m.pal.open })
	// "        " (8) + "😀" entre comillas: " + 😀(2) + " + "+" + "obj." = 8+1+2+1+1+4 = 17.
	if p := f.position(); p.Line != 2 || p.Character != 17 {
		t.Errorf("columna UTF-16 enviada: %+v (quería 2:17)", p)
	}
	// Y la inversa: la columna del servidor vuelve a runas.
	if line, col := s.m.bufPos(lsp.Position{Line: 2, Character: 17}); line != 2 || col != 16 {
		t.Errorf("bufPos=%d:%d, quería 2:16", line, col)
	}
}

func TestLSPClosesDocumentOnFileSwitch(t *testing.T) {
	f := newFakeLSP(t)
	s := lspSetup(t, true, f, true)
	s.ready()
	first := lsp.FileURI(s.m.ed.Path())
	other := write(t, s.m.root, "src/Other.java", "class Other {}\n")
	s.send(press('s', tea.ModCtrl)) // sin cambios que descartar
	s.until("guardado", func(m Model) bool { return strings.HasPrefix(m.msg, "guardado") })
	s.send(filetree.OpenFileMsg{Path: other})
	s.until("cerrado y abierto el otro", func(m Model) bool {
		_, _, closed, texts := f.snapshot()
		return len(closed) == 1 && closed[0] == first && texts[lsp.FileURI(other)] != "" && m.lsp.doc.client != nil
	})
	if l, _, _, _ := f.snapshot(); l != 1 {
		t.Errorf("los dos archivos comparten el servidor (lanzamientos=%d)", l)
	}
}

func TestLSPDiagnosticsMarkGutterAndBar(t *testing.T) {
	f := newFakeLSP(t)
	f.diag = func(uri, text string) []lsp.Diagnostic {
		return []lsp.Diagnostic{
			{Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 9}, End: lsp.Position{Line: 1, Character: 12}},
				Severity: lsp.SeverityError, Source: "jdtls", Message: "Obj cannot be resolved to a type\nsegunda línea"},
			{Range: lsp.Range{Start: lsp.Position{Line: 2}, End: lsp.Position{Line: 2, Character: 4}},
				Severity: lsp.SeverityWarning, Source: "jdtls", Message: "variable sin usar"},
			{Range: lsp.Range{Start: lsp.Position{Line: 4}, End: lsp.Position{Line: 4, Character: 1}},
				Severity: lsp.SeverityHint, Message: "pista"},
		}
	}
	s := lspSetup(t, true, f, true)
	s.ready()
	s.until("diagnósticos", func(m Model) bool { return len(m.lsp.diags) == 3 })

	// Margen: punto en las líneas 2 (error) y 3 (aviso); la pista no marca.
	view := s.m.ed.View()
	rows := strings.Split(view, "\n")
	if !strings.Contains(rows[1], "\x1b[38;2;244;71;71m●") {
		t.Errorf("línea 2 sin marca de error: %q", rows[1])
	}
	if !strings.Contains(rows[2], "\x1b[38;2;220;180;60m●") {
		t.Errorf("línea 3 sin marca de aviso: %q", rows[2])
	}
	if strings.Contains(rows[4], "●") || strings.Contains(rows[0], "●") {
		t.Errorf("líneas sin problemas marcadas:\n%q\n%q", rows[0], rows[4])
	}

	// El cursor está en la línea 3 (índice 2): su aviso en la barra.
	bar := plain(lines(s.m)[s.m.h-1])
	if !strings.Contains(bar, "⚠ variable sin usar") {
		t.Errorf("barra con el cursor en el aviso: %q", bar)
	}
	s.send(press(tea.KeyUp, 0))
	bar = plain(lines(s.m)[s.m.h-1])
	if !strings.Contains(bar, "✖ Obj cannot be resolved to a type") || strings.Contains(bar, "segunda") {
		t.Errorf("barra con el cursor en el error: %q", bar)
	}
	s.send(press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0))
	if strings.Contains(plain(lines(s.m)[s.m.h-1]), "pista") {
		t.Error("las pistas no se muestran en la barra")
	}

	// Lista navegable: ordenada por línea; elegir salta.
	s.send(press('m', tea.ModAlt))
	if !s.m.pal.open || s.m.pal.mode != palDiags {
		t.Fatalf("alt+m debía abrir la lista (msg=%q)", s.m.msg)
	}
	got := titles(s.m)
	if len(got) != 3 || !strings.Contains(got[0], "2: Obj cannot") || !strings.Contains(got[1], "3: variable") {
		t.Fatalf("lista: %v", got)
	}
	s.send(press(tea.KeyDown, 0), press(tea.KeyUp, 0), press(tea.KeyUp, 0), enter())
	if s.m.pal.open {
		t.Fatal("la lista debía cerrarse")
	}
	if l, c := cursorAt(s.m); l != 1 || c != 9 {
		t.Errorf("cursor tras elegir: %d:%d (quería 1:9)", l, c)
	}

	// Un diagnóstico nuevo (el servidor publica tras un cambio) reemplaza al viejo.
	f.mu.Lock()
	f.diag = func(uri, text string) []lsp.Diagnostic { return nil }
	f.mu.Unlock()
	s.send(press(tea.KeyEnd, 0), text("x"))
	s.until("marcas limpias", func(m Model) bool { return len(m.lsp.diags) == 0 })
	if strings.Contains(s.m.ed.View(), "●") {
		t.Error("sin diagnósticos no debe haber marcas")
	}
}

func TestLSPDefinitionServerFirstThenIndex(t *testing.T) {
	f := newFakeLSP(t)
	s := lspSetup(t, true, f, true)
	other := write(t, s.m.root, "src/Target.java", "package src;\n\npublic class Target {\n    void go() {}\n}\n")
	s.ready()
	f.mu.Lock()
	f.define = func(uri string, pos lsp.Position) any {
		return []map[string]any{{"uri": lsp.FileURI(other), "range": map[string]any{
			"start": map[string]any{"line": 3, "character": 9}, "end": map[string]any{"line": 3, "character": 11}}}}
	}
	f.mu.Unlock()
	s.m.ed.GoTo(1, 12) // sobre "Obj"
	s.send(press(tea.KeyF12, 0))
	s.until("salto", func(m Model) bool { return m.ed.Path() == other })
	if l, c := cursorAt(s.m); l != 3 || c != 9 {
		t.Errorf("cursor: %d:%d (quería 3:9)", l, c)
	}
	// El servidor tiene ahora abierto el archivo destino.
	s.until("destino abierto en el servidor", func(m Model) bool {
		_, _, _, texts := f.snapshot()
		return texts[lsp.FileURI(other)] != "" && m.lsp.doc.client != nil
	})

	// Respuesta vacía: respaldo del índice (aquí sin índice: avisa como siempre).
	f.mu.Lock()
	f.define = func(string, lsp.Position) any { return nil }
	f.mu.Unlock()
	s.m.ed.GoTo(3, 6)
	s.send(press(tea.KeyF12, 0))
	s.until("respaldo", func(m Model) bool { return strings.Contains(m.msg, "no se encontró la definición") })
}

func TestLSPToggleOffShutsServerDown(t *testing.T) {
	f := newFakeLSP(t)
	s := lspSetup(t, true, f, true)
	s.ready()
	f.mu.Lock()
	procs := append([]*fakeLSPProc(nil), f.procs...)
	f.mu.Unlock()
	if len(procs) != 1 || procs[0].exited() {
		t.Fatalf("debía haber un servidor vivo: %d", len(procs))
	}

	s.send(press('l', tea.ModAlt))
	if s.m.lsp.on || s.m.lspInfo() != "" {
		t.Fatalf("el LSP debía quedar apagado: %q", s.m.lspInfo())
	}
	s.until("servidor cerrado", func(Model) bool { return procs[0].exited() })
	// Tras apagar, completar usa el índice y nada vuelve a lanzarse.
	s.typeText("Dem")
	s.send(press('/', tea.ModAlt))
	if got := string(s.m.ed.Buffer().Line(2)); got != "        Demo" || s.m.pal.mode == palLSPComplete {
		t.Errorf("apagado se completa con el índice: línea %q", got)
	}
	s.quiet(100 * time.Millisecond)
	if l, _, _, _ := f.snapshot(); l != 1 {
		t.Errorf("lanzamientos=%d", l)
	}
	// Encender de nuevo arranca otro.
	s.send(press(tea.KeyEsc, 0), press('l', tea.ModAlt))
	s.ready()
	if l, _, _, _ := f.snapshot(); l != 2 {
		t.Errorf("lanzamientos tras reencender=%d, quería 2", l)
	}
}

func TestLSPPaletteActions(t *testing.T) {
	f := newFakeLSP(t)
	s := lspSetup(t, true, f, false)
	s.send(press('p', tea.ModCtrl))
	if got := strings.Join(titles(s.m), "|"); !strings.Contains(got, "Encender el LSP") || !strings.Contains(got, "Diagnósticos del LSP") {
		t.Fatalf("la paleta no ofrece el LSP: %s", got)
	}
	s.send(text("E"), text("n"), text("c"), text("e"), text("n"), text("d"), text("e"), text("r"), text(" "), text("e"), text("l"), text(" "), text("L"), text("S"), text("P"), enter())
	if !s.m.lsp.on {
		t.Fatalf("la paleta debía encender el LSP (msg=%q)", s.m.msg)
	}
	s.ready()
	s.send(press('p', tea.ModCtrl))
	if got := strings.Join(titles(s.m), "|"); !strings.Contains(got, "Apagar el LSP") {
		t.Errorf("encendido, la paleta debe ofrecer apagar: %s", got)
	}
}

func TestLSPWithoutManagerIsUnavailable(t *testing.T) {
	m := newLayout(t, Config{}, 100, 20)
	m, _ = send(m, press('l', tea.ModAlt))
	if m.lsp.on || !strings.Contains(m.msg, "no disponible") {
		t.Errorf("msg=%q on=%v", m.msg, m.lsp.on)
	}
}

func TestFormatMem(t *testing.T) {
	for _, c := range []struct {
		b    int64
		want string
	}{
		{512 << 20, "512 MB"}, {1 << 20, "1 MB"}, {(1 << 20) / 2, "1 MB"}, {3 << 29, "1.5 GB"},
	} {
		if got := formatMem(c.b); got != c.want {
			t.Errorf("formatMem(%d)=%q, quería %q", c.b, got, c.want)
		}
	}
}

func TestPlainSnippet(t *testing.T) {
	for in, want := range map[string]string{
		"foo($1)$0":             "foo()",
		"for (${1:i} = 0; $2;)": "for (i = 0; ;)",
		"x(${2})":               "x()",
		"sin snippet":           "sin snippet",
	} {
		if got := plainSnippet(in); got != want {
			t.Errorf("plainSnippet(%q)=%q, quería %q", in, got, want)
		}
	}
}

func TestLSPGutterMarksKeepWidth(t *testing.T) {
	root := project(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, "src", "Main.java")}, 60, 10)
	before := m.ed.View()
	m.ed.SetLineMarks(map[int]editorview.LineMark{0: editorview.MarkError})
	after := m.ed.View()
	if plain(before) != strings.Replace(plain(after), "●", " ", 1) {
		t.Errorf("la marca debe ocupar el hueco del margen sin mover el texto:\n%q\n%q", plain(before), plain(after))
	}
}
