//go:build unix

package opencode

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

// fakeCLI escribe un ejecutable que hace de `opencode`: guarda sus argumentos
// (uno por línea) en <dir>/args y luego ejecuta body. En body, $FIXTURES
// apunta a testdata/opencode.
func fakeCLI(t *testing.T, body string) (bin, argsFile string) {
	t.Helper()
	fixtures, err := filepath.Abs("../../../testdata/opencode")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"FIXTURES='" + fixtures + "'\n" +
		"printf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		body + "\n"
	bin = filepath.Join(dir, "opencode")
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

func sequence(events []agent.Event) []string {
	var seq []string
	for _, ev := range events {
		switch ev.(type) {
		case agent.Started:
			seq = append(seq, "Started")
		case agent.TextDelta:
			seq = append(seq, "TextDelta")
		case agent.ToolCall:
			seq = append(seq, "ToolCall")
		case agent.FileEditProposed:
			seq = append(seq, "FileEditProposed")
		case agent.Usage:
			seq = append(seq, "Usage")
		case agent.NeedsApproval:
			seq = append(seq, "NeedsApproval")
		case agent.Done:
			seq = append(seq, "Done")
		case agent.Error:
			seq = append(seq, "Error")
		}
	}
	return seq
}

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

	task := agent.Task{Prompt: "hola", Permissions: agent.PermissionAcceptEdits}
	if err := a.Start(context.Background(), task); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := collectUntil(t, a, isDone)
	wantSeq := []string{"Started", "TextDelta", "Usage", "Done"}
	if got := sequence(events); !slices.Equal(got, wantSeq) {
		t.Fatalf("secuencia = %v, want %v", got, wantSeq)
	}
	if s := a.Status(); s != agent.StatusIdle {
		t.Errorf("Status al recibir Done = %s, quiero idle", s)
	}
	args := readArgs(t, argsFile)
	if !slices.Equal(args, Args(task)) {
		t.Errorf("args primer turno = %q, want %q", args, Args(task))
	}
	// Con PermissionAcceptEdits no debe haber --agent
	if slices.Contains(args, "--agent") {
		t.Errorf("primer turno no debía incluir --agent: %q", args)
	}

	// Segundo turno con Send reanuda la sesión anunciada ("ses_000000000002")
	if err := a.Send("segundo turno"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	collectUntil(t, a, isDone)
	wantArgs := Args(agent.Task{
		Prompt:      "segundo turno",
		SessionID:   "ses_000000000002",
		Permissions: agent.PermissionAcceptEdits,
	})
	if gotArgs := readArgs(t, argsFile); !slices.Equal(gotArgs, wantArgs) {
		t.Errorf("args Send = %q, want %q", gotArgs, wantArgs)
	}
}

func TestTurnPlanPermissions(t *testing.T) {
	bin, argsFile := fakeCLI(t, `cat "$FIXTURES/write_plan.jsonl"`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })

	task := agent.Task{Prompt: "crea archivo", Permissions: agent.PermissionAsk}
	if err := a.Start(context.Background(), task); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectUntil(t, a, isDone)
	wantSeq := []string{"Started", "Usage", "TextDelta", "Usage", "Done"}
	if got := sequence(events); !slices.Equal(got, wantSeq) {
		t.Fatalf("secuencia = %v, want %v", got, wantSeq)
	}

	args := readArgs(t, argsFile)
	if !slices.Contains(args, "--agent") || !slices.Contains(args, "plan") {
		t.Errorf("args deben contener --agent plan: %q", args)
	}
}

func TestResumeMissingSessionReportsErrSessionNotFound(t *testing.T) {
	bin, argsFile := fakeCLI(t, `cat "$FIXTURES/resume_missing.jsonl"; exit 1`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })

	missing := "ses_no_existe"
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola", SessionID: missing}); err != nil {
		t.Fatal(err)
	}
	events := collectUntil(t, a, isDone)

	if args := readArgs(t, argsFile); !slices.Contains(args, missing) || !slices.Contains(args, "--session") {
		t.Errorf("args = %q, deben incluir --session %s", args, missing)
	}

	// No debe haber evento Started
	for _, ev := range events {
		if _, ok := ev.(agent.Started); ok {
			t.Errorf("no debía haber evento Started en resume missing: %+v", ev)
		}
	}

	var foundSessionNotFoundErr bool
	for _, ev := range events {
		if e, ok := ev.(agent.Error); ok {
			if errors.Is(e.Err, agent.ErrSessionNotFound) {
				foundSessionNotFoundErr = true
			}
		}
	}
	if !foundSessionNotFoundErr {
		t.Errorf("se esperaba un agent.Error con ErrSessionNotFound; eventos: %v", sequence(events))
	}

	// Done debe tener IsError: true
	var done *agent.Done
	for _, ev := range events {
		if d, ok := ev.(agent.Done); ok {
			done = &d
		}
	}
	if done == nil || !done.IsError {
		t.Errorf("Done = %+v, want IsError: true", done)
	}
	if s := a.Status(); s != agent.StatusError {
		t.Errorf("Status = %s, want error", s)
	}
}

func TestModelError(t *testing.T) {
	bin, _ := fakeCLI(t, `cat "$FIXTURES/error_model.jsonl"; exit 1`)
	a := New(bin)
	t.Cleanup(func() { a.Close() })

	if err := a.Start(context.Background(), agent.Task{Prompt: "hola", Model: "inexistente"}); err != nil {
		t.Fatal(err)
	}
	events := collectUntil(t, a, isDone)

	// error_model sí tiene sessionID en su línea JSON ("ses_000000000001")
	wantSeq := []string{"Started", "Error", "Done"}
	if got := sequence(events); !slices.Equal(got, wantSeq) {
		t.Fatalf("secuencia = %v, want %v", got, wantSeq)
	}

	var done *agent.Done
	for _, ev := range events {
		if d, ok := ev.(agent.Done); ok {
			done = &d
		}
	}
	if done == nil || !done.IsError || done.SessionID != "ses_000000000001" {
		t.Errorf("Done = %+v, want IsError: true y SessionID ses_000000000001", done)
	}
	if s := a.Status(); s != agent.StatusError {
		t.Errorf("Status = %s, want error", s)
	}
}

func TestCloseMidTurnLeavesNoProcesses(t *testing.T) {
	bin, _ := fakeCLI(t, `cat "$FIXTURES/simple.jsonl" | head -1; sleep 60 & sleep 60 & wait`)
	a := New(bin)
	if err := a.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatal(err)
	}
	collectUntil(t, a, isStarted)
	pgid := a.PGID()
	if pgid == 0 {
		t.Fatal("PGID = 0 con turno en curso")
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

func TestCancelKeepsSession(t *testing.T) {
	bin, argsFile := fakeCLI(t, `cat "$FIXTURES/simple.jsonl" | head -1; sleep 60`)
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
		t.Errorf("Status tras Cancel = %s, want idle", s)
	}
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("el grupo %d sigue vivo tras Cancel (%v)", pgid, err)
	}
	// La sesión anunciada por Started sigue disponible para Send
	if err := a.Send("sigue"); err != nil {
		t.Fatalf("Send tras Cancel: %v", err)
	}
	collectUntil(t, a, isStarted)
	args := readArgs(t, argsFile)
	if !slices.Contains(args, "ses_000000000002") || !slices.Contains(args, "--session") {
		t.Errorf("Send tras Cancel no reanudó la sesión: args = %q", args)
	}
}
