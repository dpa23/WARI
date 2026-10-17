//go:build unix

package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/turn"
)

const fixtureSession = "00000000-0000-0000-0000-000000000001"

// fakeCLI escribe un ejecutable que hace de `claude`: guarda sus argumentos
// (uno por línea) en <dir>/args y luego ejecuta body. En body, $FIXTURES
// apunta a testdata/claude.
func fakeCLI(t *testing.T, body string) (bin, argsFile string) {
	t.Helper()
	fixtures, err := filepath.Abs("../../../testdata/claude")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"FIXTURES='" + fixtures + "'\n" +
		"printf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		body + "\n"
	bin = filepath.Join(dir, "claude")
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

func isDone(ev agent.Event) bool { _, ok := ev.(agent.Done); return ok }

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
	c := New(bin)
	t.Cleanup(func() { c.Close() })
	if err := c.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	events := collectUntil(t, c, isDone)
	var text strings.Builder
	for _, ev := range events {
		if d, ok := ev.(agent.TextDelta); ok {
			text.WriteString(d.Text)
		}
	}
	if s, ok := events[0].(agent.Started); !ok || s.SessionID != fixtureSession {
		t.Errorf("primer evento = %v, quiero Started con la sesión %q", events[0], fixtureSession)
	}
	if text.String() != "hola" {
		t.Errorf("texto = %q, quiero \"hola\"", text.String())
	}
	if s := c.Status(); s != agent.StatusIdle {
		t.Errorf("Status al recibir Done = %s, quiero idle", s)
	}
	if args := readArgs(t, argsFile); args[0] != "-p" || args[len(args)-1] != "hola" {
		t.Errorf("args del primer turno = %q", args)
	}

	if err := c.Send("otra pregunta"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	collectUntil(t, c, isDone)
	args := readArgs(t, argsFile)
	if i := slices.Index(args, "--resume"); i < 0 || args[i+1] != fixtureSession {
		t.Errorf("Send no reanudó la sesión: args = %q", args)
	}
	if args[len(args)-1] != "otra pregunta" {
		t.Errorf("último argumento = %q, quiero el mensaje de Send", args[len(args)-1])
	}
}

func TestMissingBinary(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "no-existe"))
	t.Cleanup(func() { c.Close() })
	err := c.Start(context.Background(), agent.Task{Prompt: "hola"})
	if err == nil || !strings.HasPrefix(err.Error(), "claude: ") {
		t.Errorf("Start sin ejecutable = %v, quiero un error con prefijo \"claude: \"", err)
	}
	if s := c.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
}

func TestMalformedLineDoesNotStopTurn(t *testing.T) {
	bin, _ := fakeCLI(t, `echo 'no es json'; cat "$FIXTURES/simple.jsonl"`)
	c := New(bin)
	t.Cleanup(func() { c.Close() })
	if err := c.Start(context.Background(), agent.Task{Prompt: "hola"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectUntil(t, c, isDone)
	if e, ok := events[0].(agent.Error); !ok || !strings.Contains(e.Err.Error(), TestedVersion) {
		t.Errorf("primer evento = %v, quiero un agent.Error que nombre la versión", events[0])
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Start(context.Background(), agent.Task{Prompt: "x"}); !errors.Is(err, agent.ErrDone) {
		t.Errorf("Start tras Close = %v, quiero ErrDone", err)
	}
}
