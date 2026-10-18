package dap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestsConcurrentOutOfOrder(t *testing.T) {
	release := make(chan struct{})
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		var a struct {
			Expression string `json:"expression"`
		}
		json.Unmarshal(r.Arguments, &a)
		if a.Expression == "slow" {
			<-release
		}
		f.respond(r, map[string]any{"result": a.Expression})
	})
	ctx := ctx5(t)

	var wg sync.WaitGroup
	results := make(chan string, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res, err := c.Evaluate(ctx, "slow", 0, "")
		if err != nil {
			t.Error(err)
		}
		results <- res.Result
	}()
	// La rápida responde mientras la lenta sigue en vuelo.
	time.Sleep(50 * time.Millisecond)
	res, err := c.Evaluate(ctx, "fast", 0, "")
	if err != nil || res.Result != "fast" {
		t.Fatalf("fast = %+v, %v", res, err)
	}
	select {
	case r := <-results:
		t.Fatalf("la lenta terminó antes de tiempo: %s", r)
	default:
	}
	close(release)
	wg.Wait()
	if r := <-results; r != "slow" {
		t.Fatalf("slow = %s", r)
	}
}

func TestManyConcurrentRequests(t *testing.T) {
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		var a struct {
			Expression string `json:"expression"`
		}
		json.Unmarshal(r.Arguments, &a)
		f.respond(r, map[string]any{"result": a.Expression})
	})
	ctx := ctx5(t)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := strings.Repeat("x", i+1)
			res, err := c.Evaluate(ctx, e, 0, "")
			if err != nil || res.Result != e {
				t.Errorf("%d: %+v %v", i, res, err)
			}
		}()
	}
	wg.Wait()
}

func TestCancelByContext(t *testing.T) {
	c, f := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		switch r.Command {
		case "initialize":
			f.respond(r, map[string]any{"supportsCancelRequest": true})
		case "evaluate":
			// no responde nunca
		case "cancel":
			f.respond(r, nil)
		default:
			f.respond(r, nil)
		}
	})
	if _, err := c.Initialize(ctx5(t), InitializeArgs{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := c.Evaluate(ctx, "x", 0, "")
		errc <- err
	}()
	waitFor(t, func() bool { return f.count("evaluate") == 1 })
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Evaluate no volvió tras cancelar")
	}
	// Se le pidió cancelar al adaptador, con el seq de la petición.
	waitFor(t, func() bool { return f.count("cancel") == 1 })
	f.mu.Lock()
	var evalSeq int
	var cancelArgs string
	for _, r := range f.got {
		if r.Command == "evaluate" {
			evalSeq = r.Seq
		}
		if r.Command == "cancel" {
			cancelArgs = string(r.Arguments)
		}
	}
	f.mu.Unlock()
	want, _ := json.Marshal(map[string]int{"requestId": evalSeq})
	if cancelArgs != string(want) {
		t.Fatalf("cancel args %s, quería %s", cancelArgs, want)
	}
	// La conexión sigue sirviendo y una respuesta tardía se ignora.
	if _, err := c.Threads(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	// Contexto ya cancelado: ni siquiera se envía.
	before := f.count("evaluate")
	if _, err := c.Evaluate(ctx, "y", 0, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if f.count("evaluate") != before {
		t.Fatal("se envió con el contexto cancelado")
	}
}

func TestCancelWithoutCapabilityDoesNotSendCancel(t *testing.T) {
	c, f := pipePair(t, Options{}, func(f *fakeAdapter, r req) {})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Threads(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if f.count("cancel") != 0 {
		t.Fatal("cancel enviado sin soporte")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condición no cumplida a tiempo")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMalformedMessagesDoNotPanic(t *testing.T) {
	c, f := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		// Antes de responder, un surtido de basura.
		f.raw("Content-Length: 9\r\n\r\nnot json!")                   // JSON inválido
		f.raw("Content-Length: 2\r\n\r\n[]")                          // JSON de otro tipo
		f.raw("Content-Length: 14\r\n\r\n{\"type\":\"zzz\"}\n")       // tipo desconocido
		f.raw("Content-Length: 18\r\n\r\n{\"type\":\"event\"}  ")     // evento sin nombre
		f.raw("Content-Length: 0\r\n\r\n")                            // cuerpo vacío
		f.raw("X-Otra: cabecera\r\n\r\n")                             // sin Content-Length
		f.raw("basura sin dos puntos\r\nContent-Length: 2\r\n\r\n{}") // línea ilegible y luego válido
		f.send(map[string]any{"type": "response", "request_seq": 99999, "success": true, "command": "x"})
		f.send(map[string]any{"type": "event", "event": "stopped", "body": "no es un objeto"})
		f.send(map[string]any{"type": "response", "request_seq": r.Seq, "success": true, "command": r.Command,
			"body": map[string]any{"threads": []map[string]any{{"id": 1, "name": "t"}}}})
	})
	ctx := ctx5(t)
	th, err := c.Threads(ctx)
	if err != nil || len(th) != 1 {
		t.Fatalf("Threads = %+v, %v", th, err)
	}
	if c.Malformed() == 0 {
		t.Fatal("no se contaron mensajes malformados")
	}
	// El stopped con cuerpo ilegible llega como UnknownEvent, sin perderse.
	var unk UnknownEvent
	for ev := range c.Events() {
		if u, ok := ev.(UnknownEvent); ok && u.Name == "stopped" {
			unk = u
			break
		}
	}
	if unk.Name != "stopped" {
		t.Fatal("falta el evento stopped ilegible")
	}
	// Y la conexión sigue viva.
	if _, err := c.Threads(ctx); err != nil {
		t.Fatal(err)
	}
	_ = f
}

func TestInvalidArgumentsAndContentLength(t *testing.T) {
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) { f.respond(r, nil) })
	if err := c.Launch(ctx5(t), json.RawMessage(`{no`)); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("error = %v", err)
	}
	if _, err := c.Request(ctx5(t), "x", make(chan int)); err == nil {
		t.Fatal("quería error al serializar")
	}

	// Content-Length inválido y demasiado grande son fatales, sin pánico.
	for _, in := range []string{
		"Content-Length: abc\r\n\r\n",
		"Content-Length: -1\r\n\r\n",
		"Content-Length: 99999999999\r\n\r\n",
		"Content-Length: 10\r\n\r\ncorto",
	} {
		c2 := NewClient(strings.NewReader(in), io.Discard, Options{})
		select {
		case <-c2.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("%q: la lectura no terminó", in)
		}
		if c2.Err() == nil {
			t.Fatalf("%q: Err nil", in)
		}
		c2.Close()
	}
}

func TestEventQueueNeverBlocksReader(t *testing.T) {
	c, f := pipePair(t, Options{EventBuffer: 4}, func(f *fakeAdapter, r req) {
		for range 200 {
			f.event("output", map[string]any{"output": "x"})
		}
		f.event("stopped", map[string]any{"reason": "pause", "threadId": 1})
		for range 200 {
			f.event("output", map[string]any{"output": "y"})
		}
		f.respond(r, nil)
	})
	_ = f
	// Nadie lee Events, y aun así la petición completa.
	if _, err := c.Threads(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	if c.DroppedEvents() < 390 {
		t.Fatalf("descartados %d, esperaba ≈397", c.DroppedEvents())
	}
	// La parada sobrevive porque se sacrifica primero la salida.
	got := waitEvent[StoppedEvent](t, c.Events())
	if got.Reason != "pause" {
		t.Fatalf("%+v", got)
	}
}

func TestEventQueueDropsOldestWhenNoOutput(t *testing.T) {
	q := newEventQueue(3)
	defer q.stop()
	for i := 1; i <= 5; i++ {
		q.push(ThreadEvent{ThreadID: i})
	}
	// El pump puede haber sacado uno hacia el canal (bloqueado en el envío).
	q.finish()
	var ids []int
	for ev := range q.out {
		ids = append(ids, ev.(ThreadEvent).ThreadID)
	}
	if q.droppedCount() == 0 || ids[len(ids)-1] != 5 {
		t.Fatalf("ids %v, descartados %d", ids, q.droppedCount())
	}
}

func TestEventsClosedAfterConnectionEnds(t *testing.T) {
	c, f := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		f.event("exited", map[string]any{"exitCode": 3})
		f.event("terminated", nil)
		f.cl.Close() // el adaptador cae
	})
	go c.Threads(t.Context())
	var names []string
	for ev := range c.Events() { // se cierra tras entregar lo pendiente
		names = append(names, ev.EventName())
	}
	if len(names) != 2 || names[0] != "exited" || names[1] != "terminated" {
		t.Fatalf("eventos %v", names)
	}
	<-c.Done()
	if !errors.Is(c.Err(), ErrClosed) {
		t.Fatalf("Err = %v", c.Err())
	}
	if _, err := c.Threads(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("petición tras la caída: %v", err)
	}
	_ = f
}

func TestAdapterDeathFailsPendingRequests(t *testing.T) {
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		f.cl.Close()
	})
	errc := make(chan error, 3)
	for range 3 {
		go func() {
			_, err := c.Threads(ctx5(t))
			errc <- err
		}()
	}
	for range 3 {
		select {
		case err := <-errc:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("error = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("petición colgada tras la caída")
		}
	}
}

func TestReverseRequests(t *testing.T) {
	c, f := pipePair(t, Options{RunInTerminal: func(ctx context.Context, a RunInTerminalArgs) (RunInTerminalResult, error) {
		if a.Cwd != "/p" || len(a.Args) != 2 || a.Args[0] != "dotnet" {
			return RunInTerminalResult{}, errors.New("argumentos inesperados")
		}
		return RunInTerminalResult{ProcessID: 1234}, nil
	}}, func(f *fakeAdapter, r req) { f.respond(r, nil) })

	f.send(map[string]any{"type": "request", "command": "runInTerminal",
		"arguments": map[string]any{"kind": "integrated", "cwd": "/p", "args": []string{"dotnet", "x.dll"}}})
	f.send(map[string]any{"type": "request", "command": "startDebugging", "arguments": map[string]any{}})
	waitFor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.resp) == 2
	})
	f.mu.Lock()
	byCmd := map[string]map[string]any{}
	for _, r := range f.resp {
		byCmd[r["command"].(string)] = r
	}
	f.mu.Unlock()
	rt := byCmd["runInTerminal"]
	if rt["success"] != true || rt["body"].(map[string]any)["processId"] != float64(1234) {
		t.Fatalf("runInTerminal = %v", rt)
	}
	if sd := byCmd["startDebugging"]; sd["success"] != false {
		t.Fatalf("startDebugging = %v", sd)
	}
	if _, err := c.Threads(ctx5(t)); err != nil {
		t.Fatal(err)
	}
}

func TestReverseRunInTerminalWithoutHandlerFails(t *testing.T) {
	_, f := pipePair(t, Options{}, func(f *fakeAdapter, r req) { f.respond(r, nil) })
	f.send(map[string]any{"type": "request", "command": "runInTerminal", "arguments": map[string]any{"cwd": "/", "args": []string{}}})
	waitFor(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.resp) == 1
	})
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resp[0]["success"] != false {
		t.Fatalf("%v", f.resp[0])
	}
}

func TestInitializeSendsExpectedArguments(t *testing.T) {
	var got map[string]any
	var mu sync.Mutex
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		mu.Lock()
		json.Unmarshal(r.Arguments, &got)
		mu.Unlock()
		f.respond(r, map[string]any{"supportsConfigurationDoneRequest": true, "supportsTerminateRequest": true})
	})
	caps, err := c.Initialize(ctx5(t), InitializeArgs{AdapterID: "coreclr"})
	if err != nil {
		t.Fatal(err)
	}
	if !caps.SupportsConfigurationDoneRequest || !caps.SupportsTerminateRequest || caps.SupportsCancelRequest || len(caps.Raw) == 0 {
		t.Fatalf("caps = %+v", caps)
	}
	if c.Capabilities().SupportsTerminateRequest != true {
		t.Fatal("capacidades no guardadas")
	}
	mu.Lock()
	defer mu.Unlock()
	if got["adapterID"] != "coreclr" || got["clientID"] != "wari" || got["linesStartAt1"] != true ||
		got["pathFormat"] != "path" || got["supportsRunInTerminalRequest"] != false {
		t.Fatalf("args = %v", got)
	}
}

func TestContinueAbsentAllThreadsMeansTrue(t *testing.T) {
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) { f.respond(r, map[string]any{}) })
	all, err := c.Continue(ctx5(t), 1)
	if err != nil || !all {
		t.Fatalf("all = %v, %v", all, err)
	}
}

func TestSessionPauseUsesFirstThread(t *testing.T) {
	var mu sync.Mutex
	var pausedThread int
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		switch r.Command {
		case "threads":
			f.respond(r, map[string]any{"threads": []map[string]any{{"id": 9, "name": "a"}, {"id": 10, "name": "b"}}})
		case "pause":
			var a struct {
				ThreadID int `json:"threadId"`
			}
			json.Unmarshal(r.Arguments, &a)
			mu.Lock()
			pausedThread = a.ThreadID
			mu.Unlock()
			f.respond(r, nil)
		default:
			f.respond(r, nil)
		}
	})
	s := NewSession(c, SessionOptions{})
	if err := s.Pause(ctx5(t), 0); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if pausedThread != 9 {
		t.Fatalf("pausó el hilo %d", pausedThread)
	}
	if err := s.Continue(ctx5(t), 0); !errors.Is(err, ErrNotStopped) {
		t.Fatalf("Continue sin parada: %v", err)
	}
}

func TestDisconnectWithoutResponseCountsAsSuccess(t *testing.T) {
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		if r.Command == "disconnect" {
			f.cl.Close() // cierra sin responder
			return
		}
		f.respond(r, map[string]any{})
	})
	s := NewSession(c, SessionOptions{})
	if _, err := c.Initialize(ctx5(t), InitializeArgs{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(ctx5(t)); err != nil {
		t.Fatal(err)
	}
}

func TestStopBoundedWhenAdapterIgnoresDisconnect(t *testing.T) {
	c, _ := pipePair(t, Options{CloseTimeout: 200 * time.Millisecond}, func(f *fakeAdapter, r req) {
		if r.Command == "disconnect" {
			return // cuelga
		}
		f.respond(r, map[string]any{})
	})
	s := NewSession(c, SessionOptions{StopTimeout: 200 * time.Millisecond})
	if _, err := c.Initialize(ctx5(t), InitializeArgs{}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := s.Stop(ctx5(t))
	if err == nil {
		t.Fatal("quería error de disconnect")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Stop tardó %v", time.Since(start))
	}
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("la conexión no se cerró")
	}
}
