//go:build unix

package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/turn"
)

// fakeCLI escribe un ejecutable que hace de `codex`: guarda sus argumentos
// (uno por línea) en <dir>/args y luego ejecuta body. En body, $FIXTURES
// apunta a testdata/codex.
func fakeCLI(t *testing.T, body string) (bin, argsFile string) {
	t.Helper()
	fixtures, err := filepath.Abs("../../../testdata/codex")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"FIXTURES='" + fixtures + "'\n" +
		"printf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		body + "\n"
	bin = filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

// collectUntil lee eventos hasta que stop devuelve true o se cierra el canal.
func collectUntil(t *testing.T, r *turn.Runner, stop func(agent.Event) bool) []agent.Event {
	t.Helper()
	var got []agent.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-r.Events():
			if !ok {
				return got
			}
			got = append(got, ev)
			if stop(ev) {
				return got
			}
		case <-deadline:
			t.Fatalf("tiempo agotado; eventos recibidos: %v", got)
		}
	}
}

func isDone(ev agent.Event) bool    { _, ok := ev.(agent.Done); return ok }
func isStarted(ev agent.Event) bool { _, ok := ev.(agent.Started); return ok }

func readArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestTurnAndSend(t *testing.T) {
	bin, argsFile := fakeCLI(t, `cat "$FIXTURES/synthetic_simple.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	task := agent.Task{Prompt: "hola", Permissions: agent.PermissionAcceptEdits}
	if err := a.Start(context.Background(), task); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectUntil(t, a, isDone)
	if got := sequence(events); !slices.Equal(got, []string{"Started", "TextDelta", "Usage", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	if s := a.Status(); s != agent.StatusIdle {
		t.Errorf("Status al recibir Done = %s, quiero idle", s)
	}
	if args := readArgs(t, argsFile); !slices.Equal(args, Args(task)) {
		t.Errorf("args = %q, quiero %q", args, Args(task))
	}

	// Send hereda el modo del Start y reanuda el hilo.
	if err := a.Send("otra pregunta"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	collectUntil(t, a, isDone)
	want := Args(agent.Task{Prompt: "otra pregunta", SessionID: tid, Permissions: agent.PermissionAcceptEdits})
	if args := readArgs(t, argsFile); !slices.Equal(args, want) {
		t.Errorf("args de Send = %q, quiero %q", args, want)
	}
}

func TestMissingBinary(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "no-existe"))
	t.Cleanup(func() { a.Close() })
	err := a.Start(context.Background(), agent.Task{Prompt: "hola"})
	if err == nil || !strings.HasPrefix(err.Error(), "codex: ") {
		t.Errorf("Start sin ejecutable = %v, quiero un error con prefijo \"codex: \"", err)
	}
	if s := a.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
}

func TestMalformedLineDoesNotStopTurn(t *testing.T) {
	bin, _ := fakeCLI(t, `echo 'no es json'; cat "$FIXTURES/synthetic_simple.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectUntil(t, a, isDone)
	if e, ok := events[0].(agent.Error); !ok || !strings.Contains(e.Err.Error(), TestedVersion) {
		t.Errorf("primer evento = %v, quiero un agent.Error que nombre la versión", events[0])
	}
	if d := find[agent.Done](t, events); d.IsError {
		t.Errorf("Done = %+v", d)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := a.Start(context.Background(), agent.Task{Prompt: "x"}); !errors.Is(err, agent.ErrDone) {
		t.Errorf("Start tras Close = %v, quiero ErrDone", err)
	}
}

func TestUnauthenticatedSetsErrorStatus(t *testing.T) {
	// Grabación real: el turno falla con turn.failed y la CLI sale con 1.
	bin, _ := fakeCLI(t, `cat "$FIXTURES/error_unauthenticated.jsonl"; exit 1`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatal(err)
	}
	events := collectUntil(t, a, isDone)
	if got := sequence(events); !slices.Equal(got, []string{"Started", "Error", "Error", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	if s := a.Status(); s != agent.StatusError {
		t.Errorf("Status = %s, quiero error", s)
	}
	// Sin sesión pedida, un fallo no es ErrSessionNotFound.
	for _, ev := range events {
		if e, ok := ev.(agent.Error); ok && errors.Is(e.Err, agent.ErrSessionNotFound) {
			t.Errorf("Error = %v, no debe ser ErrSessionNotFound", e.Err)
		}
	}
}

func TestResumeMissingThreadReportsErrSessionNotFound(t *testing.T) {
	// Salida verificada con codex 0.163.0: nada en stdout, el error en
	// stderr y código 1 (no hay thread.started).
	missing := "11111111-1111-4111-8111-111111111111"
	bin, argsFile := fakeCLI(t, `echo "Error: thread/resume: thread/resume failed: no rollout found for thread id `+missing+` (code -32600)" >&2; exit 1`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola", SessionID: missing}); err != nil {
		t.Fatal(err)
	}
	events := collectUntil(t, a, isDone)
	if args := readArgs(t, argsFile); !slices.Contains(args, missing) || !slices.Contains(args, "resume") {
		t.Errorf("args = %q", args)
	}
	var notFound, stderrShown bool
	for _, ev := range events {
		if e, ok := ev.(agent.Error); ok {
			notFound = notFound || errors.Is(e.Err, agent.ErrSessionNotFound)
			stderrShown = stderrShown || strings.Contains(e.Err.Error(), "no rollout found")
		}
	}
	if !notFound || !stderrShown {
		t.Errorf("ErrSessionNotFound = %v, stderr en el error = %v; eventos %v", notFound, stderrShown, sequence(events))
	}
	if d := find[agent.Done](t, events); !d.IsError {
		t.Errorf("Done = %+v", d)
	}
}

func TestParserStateIsPerTurn(t *testing.T) {
	// El segundo turno no hereda el texto ni los items del primero: su
	// primer mensaje no lleva separador y su ToolCall se vuelve a emitir.
	bin, _ := fakeCLI(t, `cat "$FIXTURES/synthetic_command.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	for i := range 2 {
		var err error
		if i == 0 {
			err = a.Start(context.Background(), agent.Task{Prompt: "a"})
		} else {
			err = a.Send("b")
		}
		if err != nil {
			t.Fatalf("turno %d: %v", i, err)
		}
		events := collectUntil(t, a, isDone)
		if got := joinText(events); got != "Leí nota.txt.\n\ncontenido de prueba 42" {
			t.Errorf("turno %d: texto = %q", i, got)
		}
		find[agent.ToolCall](t, events)
	}
}

func TestCloseMidTurnLeavesNoProcesses(t *testing.T) {
	// El turno anuncia el hilo y lanza dos nietos, como haría la CLI al
	// ejecutar comandos.
	bin, _ := fakeCLI(t, `head -1 "$FIXTURES/synthetic_simple.jsonl"; sleep 60 & sleep 60 & wait`)
	a := New(bin)
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatal(err)
	}
	collectUntil(t, a, isStarted)
	pgid := a.PGID()
	if pgid == 0 {
		t.Fatal("PGID = 0 con un turno en curso")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("quedan procesos en el grupo %d", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCancelKeepsThread(t *testing.T) {
	bin, argsFile := fakeCLI(t, `head -1 "$FIXTURES/synthetic_simple.jsonl"; sleep 60`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatal(err)
	}
	collectUntil(t, a, isStarted)
	pgid := a.PGID()
	if err := a.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if s := a.Status(); s != agent.StatusIdle {
		t.Errorf("Status tras Cancel = %s, quiero idle", s)
	}
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("el grupo %d sigue vivo tras Cancel (%v)", pgid, err)
	}
	// El hilo anunciado por Started sigue disponible para Send.
	if err := a.Send("sigue"); err != nil {
		t.Fatalf("Send tras Cancel: %v", err)
	}
	collectUntil(t, a, isStarted)
	if args := readArgs(t, argsFile); !slices.Contains(args, tid) || !slices.Contains(args, "resume") {
		t.Errorf("Send tras Cancel no reanudó el hilo: args = %q", args)
	}
}
