//go:build unix

package agy

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

// fakeCLI escribe un ejecutable que hace de `agy`: guarda sus argumentos
// (uno por línea) en <dir>/args y luego ejecuta body. En body, $FIXTURES
// apunta a testdata/agy.
func fakeCLI(t *testing.T, body string) (bin, argsFile string) {
	t.Helper()
	fixtures, err := filepath.Abs("../../../testdata/agy")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"FIXTURES='" + fixtures + "'\n" +
		"printf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		body + "\n"
	bin = filepath.Join(dir, "agy")
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
	bin, argsFile := fakeCLI(t, `cat "$FIXTURES/simple.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola", Permissions: agent.PermissionAcceptEdits}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := collectUntil(t, a, isDone)
	if s, ok := events[0].(agent.Started); !ok || s.SessionID != sid {
		t.Errorf("primer evento = %v, quiero Started con la conversación %q", events[0], sid)
	}
	if text := joinText(events); text != "hola\n" {
		t.Errorf("texto = %q, quiero %q", text, "hola\n")
	}
	if s := a.Status(); s != agent.StatusIdle {
		t.Errorf("Status al recibir Done = %s, quiero idle", s)
	}
	args := readArgs(t, argsFile)
	if args[0] != "-p=hola" || !slices.Contains(args, "--mode=accept-edits") {
		t.Errorf("args del primer turno = %q", args)
	}

	// El Send hereda el modo del Start y reanuda la conversación.
	if err := a.Send("otra pregunta"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	collectUntil(t, a, isDone)
	args = readArgs(t, argsFile)
	if !slices.Contains(args, "--conversation="+sid) {
		t.Errorf("Send no reanudó la conversación: args = %q", args)
	}
	if args[0] != "-p=otra pregunta" || !slices.Contains(args, "--mode=accept-edits") {
		t.Errorf("args de Send = %q", args)
	}
}

func TestMissingBinary(t *testing.T) {
	a := New(filepath.Join(t.TempDir(), "no-existe"))
	t.Cleanup(func() { a.Close() })
	err := a.Start(context.Background(), agent.Task{Prompt: "hola"})
	if err == nil || !strings.HasPrefix(err.Error(), "agy: ") {
		t.Errorf("Start sin ejecutable = %v, quiero un error con prefijo \"agy: \"", err)
	}
	if s := a.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
}

func TestMalformedLineDoesNotStopTurn(t *testing.T) {
	bin, _ := fakeCLI(t, `echo 'no es json'; cat "$FIXTURES/simple.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectUntil(t, a, isDone)
	if e, ok := events[0].(agent.Error); !ok || !strings.Contains(e.Err.Error(), TestedVersion) {
		t.Errorf("primer evento = %v, quiero un agent.Error que nombre la versión", events[0])
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := a.Start(context.Background(), agent.Task{Prompt: "x"}); !errors.Is(err, agent.ErrDone) {
		t.Errorf("Start tras Close = %v, quiero ErrDone", err)
	}
}

func TestEditProposed(t *testing.T) {
	bin, _ := fakeCLI(t, `cat "$FIXTURES/write_accept.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "crea hola.txt"}); err != nil {
		t.Fatal(err)
	}
	events := collectUntil(t, a, isDone)
	edit := find[agent.FileEditProposed](t, events)
	if edit.Tool != "write_to_file" || edit.Path != "/tmp/wari-agy-fixture/hola.txt" {
		t.Errorf("FileEditProposed = %+v", edit)
	}
}

func TestErrorModelSetsErrorStatus(t *testing.T) {
	// La CLI real escribe el result de error y sale con código distinto de 0.
	bin, _ := fakeCLI(t, `cat "$FIXTURES/error_model.jsonl"; exit 1`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola", Model: "modelo-inexistente"}); err != nil {
		t.Fatal(err)
	}
	events := collectUntil(t, a, isDone)
	if got := sequence(events); !slices.Equal(got, []string{"Usage", "Error", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	if s := a.Status(); s != agent.StatusError {
		t.Errorf("Status = %s, quiero error", s)
	}
	// Sin sesión no hay Started y no se pidió reanudar: no es ErrSessionNotFound.
	if e := find[agent.Error](t, events); errors.Is(e.Err, agent.ErrSessionNotFound) {
		t.Errorf("Error = %v, no debe ser ErrSessionNotFound", e.Err)
	}
}

func TestResumeMissingConversationReportsErrSessionNotFound(t *testing.T) {
	// Con una conversación inexistente agy abre otra (fixture resume_missing).
	bin, argsFile := fakeCLI(t, `cat "$FIXTURES/resume_missing.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })
	missing := "00000000-0000-4000-8000-000000000099"
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola", SessionID: missing}); err != nil {
		t.Fatal(err)
	}
	events := collectUntil(t, a, isDone)
	if args := readArgs(t, argsFile); !slices.Contains(args, "--conversation="+missing) {
		t.Errorf("args = %q", args)
	}
	var found bool
	for _, ev := range events {
		if e, ok := ev.(agent.Error); ok && errors.Is(e.Err, agent.ErrSessionNotFound) {
			found = true
		}
	}
	if !found {
		t.Errorf("sin Error ErrSessionNotFound en %v", sequence(events))
	}

	// Un resume que sí encuentra la conversación no da error.
	bin2, _ := fakeCLI(t, `cat "$FIXTURES/resume.jsonl"`)
	b := New(bin2)
	t.Cleanup(func() { b.Close() })
	if err := b.Start(context.Background(), agent.Task{Prompt: "hola", SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range collectUntil(t, b, isDone) {
		if e, ok := ev.(agent.Error); ok {
			t.Errorf("Error inesperado al reanudar bien: %v", e.Err)
		}
	}
}

func TestCloseMidTurnLeavesNoProcesses(t *testing.T) {
	// El turno anuncia la conversación y lanza dos nietos, como haría la CLI
	// al ejecutar comandos.
	bin, _ := fakeCLI(t, `head -1 "$FIXTURES/simple.jsonl"; sleep 60 & sleep 60 & wait`)
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

func TestCancelKeepsConversation(t *testing.T) {
	bin, argsFile := fakeCLI(t, `head -1 "$FIXTURES/simple.jsonl"; sleep 60`)
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
	// La conversación anunciada por Started sigue disponible para Send.
	if err := a.Send("sigue"); err != nil {
		t.Fatalf("Send tras Cancel: %v", err)
	}
	collectUntil(t, a, isStarted)
	if args := readArgs(t, argsFile); !slices.Contains(args, "--conversation="+sid) {
		t.Errorf("Send tras Cancel no reanudó la conversación: args = %q", args)
	}
}
