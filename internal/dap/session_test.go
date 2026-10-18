package dap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSessionFullFlow(t *testing.T) {
	for name, pair := range map[string]func(*testing.T, Options, func(*fakeAdapter, req)) (*Client, *fakeAdapter){
		"pipes": pipePair,
		"tcp":   tcpPair,
	} {
		t.Run(name, func(t *testing.T) {
			c, f := pair(t, Options{}, scenario())
			s := NewSession(c, SessionOptions{AdapterID: "coreclr"})
			ctx := ctx5(t)

			// Breakpoint antes de Start: solo se registra.
			if err := s.SetBreakpoint(ctx, "/p/Program.cs", 10); err != nil {
				t.Fatal(err)
			}
			if f.count("setBreakpoints") != 0 {
				t.Fatal("setBreakpoints enviado antes de Start")
			}
			if s.State() != StateIdle {
				t.Fatalf("estado %v, quería idle", s.State())
			}

			if err := s.Start(ctx, []byte(`{"program":"/p/bin/app.dll"}`)); err != nil {
				t.Fatal(err)
			}
			want := []string{"initialize", "launch", "setBreakpoints", "configurationDone"}
			if got := f.commands(); !slices.Equal(got, want) {
				t.Fatalf("secuencia %v, quería %v", got, want)
			}
			if err := s.Start(ctx, nil); !errors.Is(err, ErrStarted) {
				t.Fatalf("segundo Start: %v", err)
			}

			ev := waitEvent[StoppedEvent](t, s.Events())
			if ev.Reason != "breakpoint" || ev.ThreadID != 7 {
				t.Fatalf("stopped = %+v", ev)
			}
			if th, ok := s.StoppedThread(); !ok || th != 7 || s.State() != StateStopped {
				t.Fatalf("estado tras stopped: %v thread %d", s.State(), th)
			}
			bps := s.Breakpoints("")
			if len(bps) != 1 || bps[0].Line != 10 || bps[0].ActualLine != 11 || !bps[0].Verified || bps[0].ID != 100 {
				t.Fatalf("breakpoints = %+v", bps)
			}

			frames, err := s.Stack(ctx, 0)
			if err != nil || len(frames) != 2 || frames[0].Name != "Main" || frames[0].Source.Path != "/p/Program.cs" {
				t.Fatalf("stack = %+v, %v", frames, err)
			}

			tree, err := s.Tree(ctx, frames[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			rows := tree.Rows()
			// Locals expandido; Statics (costoso) sin expandir.
			if len(rows) != 4 || rows[0].Node.Name != "Locals" || rows[1].Node.Name != "x" || rows[3].Node.Name != "Statics" {
				t.Fatalf("filas iniciales: %s", dump(rows))
			}
			obj := rows[2].Node
			if err := tree.Expand(ctx, obj); err != nil {
				t.Fatal(err)
			}
			inner := obj.Children[1]
			if err := tree.Toggle(ctx, inner); err != nil {
				t.Fatal(err)
			}
			rows = tree.Rows()
			if len(rows) != 7 || rows[5].Node.Name != "deep" || rows[5].Depth != 3 {
				t.Fatalf("filas anidadas: %s", dump(rows))
			}
			tree.Collapse(obj)
			if n := len(tree.Rows()); n != 4 {
				t.Fatalf("tras plegar: %d filas", n)
			}
			if err := tree.Expand(ctx, rows[1].Node); err != nil { // hoja: no hace nada
				t.Fatal(err)
			}

			vars, err := s.Variables(ctx, 1000)
			if err != nil || len(vars) != 2 {
				t.Fatalf("Variables = %+v, %v", vars, err)
			}
			if _, err := s.Variables(ctx, 9999); err == nil {
				t.Fatal("quería error para referencia desconocida")
			} else if re := (*ResponseError)(nil); !errors.As(err, &re) || re.Message != "referencia desconocida" {
				t.Fatalf("error = %v", err)
			}
			res, err := s.Evaluate(ctx, "x+1", frames[0].ID)
			if err != nil || res.Result != "43" {
				t.Fatalf("Evaluate = %+v, %v", res, err)
			}

			if err := s.Next(ctx, 0); err != nil {
				t.Fatal(err)
			}
			if ev := waitEvent[StoppedEvent](t, s.Events()); ev.Reason != "step" {
				t.Fatalf("stopped = %+v", ev)
			}
			if s.State() != StateStopped {
				t.Fatalf("estado tras step: %v (la respuesta no debe pisar la parada)", s.State())
			}

			// Con el depurado en marcha, las operaciones sobre la parada fallan.
			if err := s.Continue(ctx, 0); err != nil {
				t.Fatal(err)
			}
			if out := waitEvent[OutputEvent](t, s.Events()); out.Output != "hola\n" {
				t.Fatalf("output = %+v", out)
			}
			if ex := waitEvent[ExitedEvent](t, s.Events()); ex.ExitCode != 0 {
				t.Fatalf("exited = %+v", ex)
			}
			waitEvent[TerminatedEvent](t, s.Events())
			if s.State() != StateTerminated {
				t.Fatalf("estado final %v", s.State())
			}
			if _, err := s.Stack(ctx, 0); !errors.Is(err, ErrNotStopped) {
				t.Fatalf("Stack tras terminar: %v", err)
			}

			if err := s.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-s.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("Done no se cerró tras Stop")
			}
			if f.count("disconnect") != 1 {
				t.Fatalf("disconnect enviado %d veces", f.count("disconnect"))
			}
			if err := s.Stop(ctx); err != nil { // idempotente
				t.Fatal(err)
			}
		})
	}
}

func dump(rows []Row) string {
	var s string
	for _, r := range rows {
		s += "\n"
		for i := 0; i < r.Depth; i++ {
			s += "  "
		}
		s += r.Node.Name
	}
	return s
}

func TestSessionBreakpointsAfterStart(t *testing.T) {
	c, f := pipePair(t, Options{}, scenario())
	s := NewSession(c, SessionOptions{})
	ctx := ctx5(t)
	if err := s.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if f.count("setBreakpoints") != 0 {
		t.Fatal("sin breakpoints no hay setBreakpoints")
	}
	for _, l := range []int{5, 3, 5} {
		if err := s.SetBreakpoint(ctx, "/p/A.cs", l); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Breakpoints("/p/A.cs"); len(got) != 2 || got[0].Line != 3 || got[1].Line != 5 {
		t.Fatalf("breakpoints = %+v", got)
	}
	on, err := s.ToggleBreakpoint(ctx, "/p/A.cs", 3)
	if err != nil || on {
		t.Fatalf("Toggle = %v, %v", on, err)
	}
	if err := s.ClearBreakpoint(ctx, "/p/A.cs", 5); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearBreakpoint(ctx, "/p/A.cs", 99); err != nil { // inexistente
		t.Fatal(err)
	}
	if got := s.Breakpoints(""); len(got) != 0 {
		t.Fatalf("quedan %+v", got)
	}
	// El último setBreakpoints enviado debe ser la lista vacía.
	f.mu.Lock()
	last := f.got[len(f.got)-1]
	f.mu.Unlock()
	if last.Command != "setBreakpoints" || !strings.Contains(string(last.Arguments), `"breakpoints":[]`) {
		t.Fatalf("último mensaje: %s %s", last.Command, last.Arguments)
	}
	if err := s.SetBreakpoint(ctx, "/p/A.cs", 0); err == nil {
		t.Fatal("línea 0 debería fallar")
	}
}

func TestSessionBreakpointEventUpdatesVerified(t *testing.T) {
	sc := scenario()
	c, f := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		if r.Command == "setBreakpoints" {
			f.respond(r, map[string]any{"breakpoints": []map[string]any{{"id": 5, "verified": false, "message": "pendiente"}}})
			return
		}
		sc(f, r)
	})
	s := NewSession(c, SessionOptions{})
	ctx := ctx5(t)
	s.SetBreakpoint(ctx, "/p/A.cs", 3)
	if err := s.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if b := s.Breakpoints("")[0]; b.Verified || b.Message != "pendiente" {
		t.Fatalf("%+v", b)
	}
	f.event("breakpoint", map[string]any{"reason": "changed", "breakpoint": map[string]any{"id": 5, "verified": true, "line": 4}})
	waitEvent[BreakpointEvent](t, s.Events())
	if b := s.Breakpoints("")[0]; !b.Verified || b.ActualLine != 4 {
		t.Fatalf("%+v", b)
	}
}

func TestSessionStartLaunchError(t *testing.T) {
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		switch r.Command {
		case "initialize":
			f.respond(r, map[string]any{})
			f.event("initialized", nil)
		case "launch":
			f.fail(r, "no existe el programa")
		case "disconnect":
			f.respond(r, nil)
			f.cl.Close()
		default:
			f.respond(r, nil)
		}
	})
	s := NewSession(c, SessionOptions{})
	err := s.Start(ctx5(t), nil)
	var re *ResponseError
	if !errors.As(err, &re) || re.Message != "no existe el programa" {
		t.Fatalf("error = %v", err)
	}
	if s.State() != StateTerminated {
		t.Fatalf("estado %v tras fallar Start", s.State())
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Start fallido debe cerrar la conexión")
	}
}

func TestSessionLaunchRespondsBeforeInitialized(t *testing.T) {
	// Orden alternativo: launch se responde primero y initialized llega después.
	c, f := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		switch r.Command {
		case "initialize":
			f.respond(r, map[string]any{})
		case "launch":
			f.respond(r, nil)
			f.event("initialized", nil)
		default:
			f.respond(r, nil)
		}
	})
	s := NewSession(c, SessionOptions{})
	if err := s.Start(ctx5(t), nil); err != nil {
		t.Fatal(err)
	}
	// Sin supportsConfigurationDoneRequest no se envía.
	if f.count("configurationDone") != 0 {
		t.Fatal("configurationDone no soportado pero enviado")
	}
}

func TestSessionStartContextCancel(t *testing.T) {
	// El adaptador nunca emite initialized.
	c, _ := pipePair(t, Options{}, func(f *fakeAdapter, r req) {
		if r.Command == "initialize" {
			f.respond(r, map[string]any{})
		}
	})
	s := NewSession(c, SessionOptions{StopTimeout: 100 * time.Millisecond})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := s.Start(ctx, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
