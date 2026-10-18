package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

func bg(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func initialized(t *testing.T, extra func(f *fakeServer, m message) bool) (*Client, *fakeServer) {
	t.Helper()
	c, f := newPair(t, Options{ShutdownTimeout: time.Second}, defaultHandle(extra))
	if err := c.Initialize(bg(t), "file:///work/proj"); err != nil {
		t.Fatal(err)
	}
	return c, f
}

func params(t *testing.T, m message) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(m.Params, &p); err != nil {
		t.Fatalf("params: %v", err)
	}
	return p
}

func TestInitialize(t *testing.T) {
	c, f := initialized(t, nil)
	m := f.waitFor("initialize", 1)
	p := params(t, m)
	if p["rootUri"] != "file:///work/proj" {
		t.Errorf("rootUri=%v", p["rootUri"])
	}
	caps, _ := p["capabilities"].(map[string]any)
	td, _ := caps["textDocument"].(map[string]any)
	for _, k := range []string{"synchronization", "completion", "definition", "publishDiagnostics"} {
		if _, ok := td[k]; !ok {
			t.Errorf("falta capabilities.textDocument.%s", k)
		}
	}
	if p["processId"] == nil {
		t.Error("falta processId")
	}
	f.waitFor("initialized", 1)
	got := c.Capabilities()
	if !got.Completion || !got.Definition || len(got.Raw) == 0 {
		t.Errorf("capabilities=%+v", got)
	}
	// initialized llega después de initialize.
	if ms := f.methods(); !slices.Equal(ms[:2], []string{"initialize", "initialized"}) {
		t.Errorf("orden=%v", ms)
	}
}

func TestInitializeWithoutRoot(t *testing.T) {
	c, f := newPair(t, Options{}, defaultHandle(nil))
	if err := c.Initialize(bg(t), ""); err != nil {
		t.Fatal(err)
	}
	p := params(t, f.waitFor("initialize", 1))
	if v, ok := p["rootUri"]; !ok || v != nil {
		t.Errorf("rootUri=%v (debe ser null)", v)
	}
}

func TestInitializeServerError(t *testing.T) {
	c, _ := newPair(t, Options{}, func(f *fakeServer, m message) {
		if m.Method == "initialize" {
			f.replyErr(m, -32002, "no listo")
		}
	})
	err := c.Initialize(bg(t), "file:///x")
	var re *ResponseError
	if !errors.As(err, &re) || re.Code != -32002 {
		t.Fatalf("err=%v", err)
	}
}

func TestDocumentSync(t *testing.T) {
	c, f := initialized(t, nil)
	uri := "file:///work/proj/A.java"
	if err := c.DidOpen(uri, "java", "class A {}"); err != nil {
		t.Fatal(err)
	}
	open := params(t, f.waitFor("textDocument/didOpen", 1))["textDocument"].(map[string]any)
	if open["uri"] != uri || open["languageId"] != "java" || open["version"] != float64(1) || open["text"] != "class A {}" {
		t.Errorf("didOpen=%v", open)
	}
	for i, text := range []string{"class A { int x; }", "class A { int y; }"} {
		if err := c.DidChange(uri, text); err != nil {
			t.Fatal(err)
		}
		p := params(t, f.waitFor("textDocument/didChange", i+1))
		td := p["textDocument"].(map[string]any)
		if td["version"] != float64(i+2) {
			t.Errorf("version=%v, quería %d", td["version"], i+2)
		}
		ch := p["contentChanges"].([]any)
		if len(ch) != 1 || ch[0].(map[string]any)["text"] != text {
			t.Errorf("contentChanges=%v", ch)
		}
		if _, ranged := ch[0].(map[string]any)["range"]; ranged {
			t.Error("full sync no lleva range")
		}
	}
	if err := c.DidClose(uri); err != nil {
		t.Fatal(err)
	}
	cl := params(t, f.waitFor("textDocument/didClose", 1))["textDocument"].(map[string]any)
	if cl["uri"] != uri {
		t.Errorf("didClose=%v", cl)
	}
	if err := c.DidChange(uri, "x"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("didChange tras cerrar: %v", err)
	}
	if err := c.DidChange("file:///nunca", "x"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("didChange sin abrir: %v", err)
	}
}

func completionHandler(result any) func(f *fakeServer, m message) bool {
	return func(f *fakeServer, m message) bool {
		if m.Method == "textDocument/completion" {
			f.reply(m, result)
			return true
		}
		return false
	}
}

func TestCompletionShapes(t *testing.T) {
	item := func(m map[string]any) map[string]any { return m }
	tests := []struct {
		name   string
		result any
		want   []CompletionItem
	}{
		{"null", nil, nil},
		{"array", []any{item(map[string]any{"label": "size", "detail": "int size()", "kind": 2})},
			[]CompletionItem{{Label: "size", Detail: "int size()", Kind: 2, InsertText: "size"}}},
		{"lista", map[string]any{"isIncomplete": true, "items": []any{
			item(map[string]any{"label": "add", "insertText": "add(${1:e})", "insertTextFormat": 2}),
		}}, []CompletionItem{{Label: "add", InsertText: "add(${1:e})", Snippet: true}}},
		{"textEdit manda", []any{item(map[string]any{
			"label": "length", "insertText": "ignorado",
			"textEdit": map[string]any{"newText": "length()", "range": map[string]any{
				"start": map[string]any{"line": 1, "character": 2}, "end": map[string]any{"line": 1, "character": 4}}},
		})}, []CompletionItem{{Label: "length", InsertText: "length()",
			Replace: &Range{Position{1, 2}, Position{1, 4}}}}},
		{"insertReplaceEdit", []any{item(map[string]any{
			"label": "x",
			"textEdit": map[string]any{"newText": "xx",
				"insert":  map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}},
				"replace": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 3}}},
		})}, []CompletionItem{{Label: "x", InsertText: "xx", Replace: &Range{Position{0, 0}, Position{0, 3}}}}},
		{"descarta sin etiqueta", []any{item(map[string]any{"detail": "?"}), "basura", 7}, []CompletionItem{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, f := initialized(t, completionHandler(tt.result))
			got, err := c.Complete(bg(t), "file:///a", Position{3, 7})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
			p := params(t, f.waitFor("textDocument/completion", 1))
			if pos := p["position"].(map[string]any); pos["line"] != float64(3) || pos["character"] != float64(7) {
				t.Errorf("position=%v", pos)
			}
		})
	}
}

func rng(l1, c1, l2, c2 int) map[string]any {
	return map[string]any{
		"start": map[string]any{"line": l1, "character": c1},
		"end":   map[string]any{"line": l2, "character": c2},
	}
}

func TestDefinitionShapes(t *testing.T) {
	tests := []struct {
		name   string
		result any
		want   []Location
	}{
		{"null", nil, nil},
		{"Location", map[string]any{"uri": "file:///B.java", "range": rng(1, 2, 1, 5)},
			[]Location{{"file:///B.java", Range{Position{1, 2}, Position{1, 5}}}}},
		{"Location[]", []any{
			map[string]any{"uri": "file:///B.java", "range": rng(1, 2, 1, 5)},
			map[string]any{"uri": "file:///C.java", "range": rng(0, 0, 0, 1)},
		}, []Location{
			{"file:///B.java", Range{Position{1, 2}, Position{1, 5}}},
			{"file:///C.java", Range{Position{0, 0}, Position{0, 1}}},
		}},
		{"LocationLink usa selección", []any{map[string]any{
			"targetUri": "file:///D.java", "targetRange": rng(5, 0, 9, 1), "targetSelectionRange": rng(5, 6, 5, 9),
		}}, []Location{{"file:///D.java", Range{Position{5, 6}, Position{5, 9}}}}},
		{"LocationLink sin selección", []any{map[string]any{
			"targetUri": "file:///D.java", "targetRange": rng(5, 0, 9, 1),
		}}, []Location{{"file:///D.java", Range{Position{5, 0}, Position{9, 1}}}}},
		{"basura ignorada", []any{1, "x", map[string]any{"uri": "file:///solo-uri"}}, []Location{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := initialized(t, func(f *fakeServer, m message) bool {
				if m.Method == "textDocument/definition" {
					f.reply(m, tt.result)
					return true
				}
				return false
			})
			got, err := c.Definition(bg(t), "file:///A.java", Position{0, 0})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestRequestError(t *testing.T) {
	c, _ := initialized(t, func(f *fakeServer, m message) bool {
		if m.Method == "textDocument/completion" {
			f.replyErr(m, -32603, "boom")
			return true
		}
		return false
	})
	_, err := c.Complete(bg(t), "file:///a", Position{})
	var re *ResponseError
	if !errors.As(err, &re) || re.Message != "boom" {
		t.Fatalf("err=%v", err)
	}
}

func TestDiagnostics(t *testing.T) {
	c, f := initialized(t, nil)
	uri := "file:///A.java"
	other := "file:///B.java"
	ch, cancel := c.SubscribeDiagnostics(uri)
	defer cancel()
	och, ocancel := c.SubscribeDiagnostics(other)
	defer ocancel()

	f.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": []any{
		map[string]any{"range": rng(1, 0, 1, 4), "severity": 1, "source": "javac", "message": "no se puede resolver"},
	}})
	select {
	case d := <-ch:
		want := []Diagnostic{{Range: Range{Position{1, 0}, Position{1, 4}}, Severity: SeverityError, Source: "javac", Message: "no se puede resolver"}}
		if !reflect.DeepEqual(d, want) {
			t.Errorf("got %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sin diagnósticos")
	}
	select {
	case d := <-och:
		t.Errorf("otro documento recibió %v", d)
	case <-time.After(20 * time.Millisecond):
	}
	if got := c.Diagnostics(uri); len(got) != 1 {
		t.Errorf("Diagnostics=%v", got)
	}

	// Un consumidor lento solo ve lo último; vaciar con lista vacía también cuenta.
	for i := range 5 {
		f.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": []any{
			map[string]any{"range": rng(i, 0, i, 1), "message": fmt.Sprint("m", i)},
		}})
	}
	f.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": []any{}})
	waitDiag(t, c, uri, func(d []Diagnostic) bool { return d != nil && len(d) == 0 })
	select {
	case d := <-ch:
		if d == nil || len(d) != 0 {
			t.Errorf("quería el último valor (vacío), got %+v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("sin valor pendiente")
	}

	// Suscribirse tarde entrega lo último publicado.
	late, lcancel := c.SubscribeDiagnostics(uri)
	defer lcancel()
	select {
	case d := <-late:
		if len(d) != 0 {
			t.Errorf("late=%v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("el suscriptor tardío no recibió el último estado")
	}

	// Cancelar cierra el canal.
	cancel()
	cancel() // idempotente
	if _, ok := <-ch; ok {
		t.Error("el canal debía cerrarse")
	}
}

func waitDiag(t *testing.T, c *Client, uri string, ok func([]Diagnostic) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok(c.Diagnostics(uri)) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("los diagnósticos no llegaron")
}

func TestDiagnosticsChannelClosesWhenConnectionEnds(t *testing.T) {
	c, f := initialized(t, nil)
	ch, cancel := c.SubscribeDiagnostics("file:///A.java")
	defer cancel()
	f.out.Close()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("valor inesperado")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el canal no se cerró")
	}
	late, lcancel := c.SubscribeDiagnostics("file:///B.java")
	defer lcancel()
	if _, ok := <-late; ok {
		t.Error("suscripción tras la caída debe venir cerrada")
	}
}

func TestDidCloseClearsDiagnostics(t *testing.T) {
	c, f := initialized(t, nil)
	uri := "file:///A.java"
	c.DidOpen(uri, "java", "x")
	f.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": []any{
		map[string]any{"range": rng(0, 0, 0, 1), "message": "m"},
	}})
	waitDiag(t, c, uri, func(d []Diagnostic) bool { return len(d) == 1 })
	c.DidClose(uri)
	if got := c.Diagnostics(uri); got != nil {
		t.Errorf("Diagnostics tras didClose=%v", got)
	}
}

func TestCancellation(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	c, f := initialized(t, func(f *fakeServer, m message) bool {
		if m.Method == "textDocument/completion" {
			once.Do(func() { close(started) })
			return true // no responde nunca
		}
		return false
	})
	ctx, cancel := context.WithCancel(bg(t))
	errc := make(chan error, 1)
	go func() {
		_, err := c.Complete(ctx, "file:///a", Position{})
		errc <- err
	}()
	<-started
	req := f.waitFor("textDocument/completion", 1)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("la petición no volvió tras cancelar")
	}
	cr := f.waitFor("$/cancelRequest", 1)
	if got := params(t, cr)["id"]; got != float64(idNum(t, req)) {
		t.Errorf("cancelRequest id=%v, quería %d", got, idNum(t, req))
	}
	// La respuesta tardía se ignora y la conexión sigue sirviendo.
	f.reply(req, []any{})
	c2 := func() error {
		cctx, ccancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer ccancel()
		_, err := c.Complete(cctx, "file:///a", Position{})
		return err
	}
	if err := c2(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline: %v", err)
	}
}

func TestCancelledBeforeSend(t *testing.T) {
	c, f := initialized(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Complete(ctx, "file:///a", Position{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if slices.Contains(f.methods(), "textDocument/completion") {
		t.Error("no debió enviarse la petición")
	}
}

func TestConcurrentRequests(t *testing.T) {
	c, _ := initialized(t, func(f *fakeServer, m message) bool {
		if m.Method != "textDocument/completion" {
			return false
		}
		var p struct {
			Position Position `json:"position"`
		}
		json.Unmarshal(m.Params, &p)
		time.Sleep(time.Duration(rand.Intn(20)) * time.Millisecond) // respuestas desordenadas
		f.reply(m, []any{map[string]any{"label": fmt.Sprint("L", p.Position.Line)}})
		return true
	})
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := c.Complete(bg(t), "file:///a", Position{Line: i})
			if err != nil {
				errs <- err
				return
			}
			if len(items) != 1 || items[0].Label != fmt.Sprint("L", i) {
				errs <- fmt.Errorf("petición %d recibió %+v", i, items)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestMalformedMessagesDoNotPanic(t *testing.T) {
	c, f := initialized(t, completionHandler([]any{map[string]any{"label": "vivo"}}))
	junk := func(s string) string { return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(s), s) }
	f.write("Content-Type: x\r\n\r\n")                       // sin Content-Length
	f.write(junk("{no es json"))                             // cuerpo inválido
	f.write(junk("[]"))                                      // JSON de otra forma
	f.write(junk("null"))                                    // null
	f.write(junk(`{"jsonrpc":"2.0","id":9999,"result":1}`))  // id desconocido
	f.write(junk(`{"jsonrpc":"2.0","id":"abc","result":1}`)) // id string
	f.write(junk(`{"jsonrpc":"2.0","id":null,"result":1}`))  // id null
	f.write(junk(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":"nope"}`))
	f.write(junk(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":5}}`))
	f.write(junk(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics"}`))
	f.write(junk(`{"jsonrpc":"2.0","method":"window/logMessage"}`))
	f.write(junk(`{"jsonrpc":"2.0","id":3,"method":"workspace/configuration","params":7}`))
	f.write(junk(`{"jsonrpc":"2.0"}`))
	f.write("basura sin formato\r\n\r\n")

	items, err := c.Complete(bg(t), "file:///a", Position{})
	if err != nil || len(items) != 1 || items[0].Label != "vivo" {
		t.Fatalf("la conexión no sobrevivió: items=%v err=%v", items, err)
	}
}

func TestMalformedThenValidResponse(t *testing.T) {
	c, _ := initialized(t, func(f *fakeServer, m message) bool {
		if m.Method == "textDocument/completion" {
			f.write("X-Basura: 1\r\n\r\n")
			f.write("Content-Length: 4\r\n\r\n{xx}")
			f.reply(m, []any{map[string]any{"label": "ok"}})
			return true
		}
		return false
	})
	items, err := c.Complete(bg(t), "file:///a", Position{})
	if err != nil || len(items) != 1 || items[0].Label != "ok" {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestFatalFramingClosesConnection(t *testing.T) {
	c, f := initialized(t, func(f *fakeServer, m message) bool {
		if m.Method == "textDocument/completion" {
			f.write("Content-Length: 999999999999\r\n\r\n")
			return true
		}
		return false
	})
	_, err := c.Complete(bg(t), "file:///a", Position{})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done debía cerrarse")
	}
	if c.Err() == nil {
		t.Error("Err debía informar la causa")
	}
	if err := c.DidOpen("file:///a", "java", ""); !errors.Is(err, ErrClosed) {
		t.Errorf("DidOpen tras caída: %v", err)
	}
	_ = f
}

func TestServerCrashFailsPendingRequests(t *testing.T) {
	c, f := initialized(t, func(f *fakeServer, m message) bool {
		if m.Method == "textDocument/definition" {
			f.out.Close() // el servidor muere con la petición en vuelo
			return true
		}
		return false
	})
	_, err := c.Definition(bg(t), "file:///a", Position{})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("err=%v", err)
	}
	_ = f
}

func TestServerRequests(t *testing.T) {
	var mu sync.Mutex
	replies := map[string]message{}
	c, f := newPair(t, Options{}, nil)
	_ = c
	f.handle = func(f *fakeServer, m message) {}
	// El servidor falso lee respuestas del cliente en su propio bucle: las
	// respuestas no tienen Method, así que se recogen de f.got.
	f.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "workspace/configuration",
		"params": map[string]any{"items": []any{map[string]any{}, map[string]any{}}}})
	f.send(map[string]any{"jsonrpc": "2.0", "id": "s-2", "method": "window/workDoneProgress/create", "params": map[string]any{"token": "t"}})
	f.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "no/existe"})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, m := range f.got {
			if m.Method == "" && m.ID != nil {
				mu.Lock()
				replies[string(*m.ID)] = m
				mu.Unlock()
			}
		}
		f.mu.Unlock()
		mu.Lock()
		n := len(replies)
		mu.Unlock()
		if n == 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := string(replies["1"].Result); got != "[null,null]" {
		t.Errorf("workspace/configuration=%q", got)
	}
	if got := string(replies[`"s-2"`].Result); got != "null" || replies[`"s-2"`].Error != nil {
		t.Errorf("workDoneProgress/create=%q err=%v", got, replies[`"s-2"`].Error)
	}
	if e := replies["3"].Error; e == nil || e.Code != codeMethodNotFound {
		t.Errorf("método desconocido: %+v", replies["3"])
	}
}

func TestOnNotify(t *testing.T) {
	got := make(chan string, 4)
	c, f := newPair(t, Options{OnNotify: func(method string, _ json.RawMessage) { got <- method }}, defaultHandle(nil))
	if err := c.Initialize(bg(t), ""); err != nil {
		t.Fatal(err)
	}
	f.notify("window/logMessage", map[string]any{"type": 3, "message": "hola"})
	select {
	case m := <-got:
		if m != "window/logMessage" {
			t.Errorf("method=%s", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnNotify no se llamó")
	}
}

func TestOrderlyClose(t *testing.T) {
	c, f := initialized(t, nil)
	if err := c.DidOpen("file:///a", "java", "x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ms := f.methods()
	n := len(ms)
	if n < 2 || ms[n-2] != "shutdown" || ms[n-1] != "exit" {
		t.Errorf("secuencia=%v, debía terminar en shutdown, exit", ms)
	}
	select {
	case <-f.end:
	case <-time.After(5 * time.Second):
		t.Fatal("el servidor siguió leyendo")
	}
	select {
	case <-c.Done():
	default:
		t.Error("Done debía estar cerrado")
	}
	if err := c.Close(); err != nil { // idempotente
		t.Errorf("segundo Close: %v", err)
	}
	if err := c.DidOpen("file:///b", "java", ""); !errors.Is(err, ErrClosed) {
		t.Errorf("DidOpen tras Close: %v", err)
	}
	if _, err := c.Complete(bg(t), "file:///a", Position{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Complete tras Close: %v", err)
	}
}

func TestCloseWithoutInitializeSendsNoShutdown(t *testing.T) {
	c, f := newPair(t, Options{}, defaultHandle(nil))
	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Close sin initialize no debía esperar")
	}
	<-f.end
	if slices.Contains(f.methods(), "shutdown") {
		t.Error("no debía enviarse shutdown")
	}
}

func TestCloseKillsProcessGroupOnTimeout(t *testing.T) {
	// Servidor que ignora shutdown y no sale.
	p, f := newProcPair(t, func(f *fakeServer, m message) {
		if m.Method == "initialize" {
			f.reply(m, map[string]any{"capabilities": map[string]any{}})
		}
	})
	// El servidor falso termina al cerrar stdin; para simular un servidor
	// colgado, el cierre de stdin no debe provocar la salida.
	real := p.stdin
	p.stdin = nopCloser{real}
	p.onKill = func() { real.Close() }
	c := NewProcessClient(p, Options{ShutdownTimeout: 100 * time.Millisecond})
	if err := c.Initialize(bg(t), "file:///x"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := c.Close()
	if err == nil {
		t.Error("Close debía informar el problema de shutdown")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("Close tardó %v", el)
	}
	if p.killCount() != 1 || p.closes != 1 {
		t.Errorf("kills=%d closes=%d", p.killCount(), p.closes)
	}
	_ = f
}

type nopCloser struct{ io.WriteCloser }

func (nopCloser) Close() error { return nil }

func TestCloseCleanShutdownStillKillsGroup(t *testing.T) {
	// Salida limpia: el líder sale solo, pero Kill se llama igualmente para
	// no dejar descendientes (es idempotente en proc).
	p, f := newProcPair(t, defaultHandle(nil))
	c := NewProcessClient(p, Options{ShutdownTimeout: time.Second})
	if err := c.Initialize(bg(t), "file:///x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if p.killCount() != 1 {
		t.Errorf("kills=%d", p.killCount())
	}
	ms := f.methods()
	if ms[len(ms)-1] != "exit" {
		t.Errorf("secuencia=%v", ms)
	}
}

func TestMemoryRSSWithoutProcess(t *testing.T) {
	c, _ := newPair(t, Options{}, defaultHandle(nil))
	n, err := c.MemoryRSS()
	if n != 0 || err == nil {
		t.Errorf("n=%d err=%v", n, err)
	}
}
