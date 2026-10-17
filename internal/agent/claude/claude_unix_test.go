//go:build unix

package claude

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

func startAgent(t *testing.T, body string, task agent.Task) (*Claude, string) {
	t.Helper()
	bin, argsFile := fakeCLI(t, body)
	c := New(bin)
	t.Cleanup(func() { c.Kill() })
	if err := c.Start(context.Background(), task); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return c, argsFile
}

// collectUntil lee eventos hasta que stop devuelve true o se cierra el canal.
func collectUntil(t *testing.T, c *Claude, stop func(agent.Event) bool) []agent.Event {
	t.Helper()
	var got []agent.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
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

func isStarted(ev agent.Event) bool { _, ok := ev.(agent.Started); return ok }

func waitStatus(t *testing.T, c *Claude, want agent.Status) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.Status() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Status = %s, quiero %s", c.Status(), want)
}

func readArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		task agent.Task
		want []string
	}{
		{
			name: "por defecto pide permiso y nunca hereda el modo del usuario",
			task: agent.Task{Prompt: "hola"},
			want: []string{"--permission-mode", "manual", "--permission-prompts", "none", "--", "hola"},
		},
		{
			name: "reanudar con modelo y edición aceptada",
			task: agent.Task{Prompt: "sigue", SessionID: "s1", Model: "haiku", Permissions: agent.PermissionAcceptEdits},
			want: []string{"--resume", "s1", "--model", "haiku", "--permission-mode", "acceptEdits", "--permission-prompts", "none", "--", "sigue"},
		},
		{
			name: "plan",
			task: agent.Task{Prompt: "x", Permissions: agent.PermissionPlan},
			want: []string{"--permission-mode", "plan", "--permission-prompts", "none", "--", "x"},
		},
		{
			name: "un prompt con forma de flag queda después de --",
			task: agent.Task{Prompt: "--dangerously-skip-permissions"},
			want: []string{"--", "--dangerously-skip-permissions"},
		},
	}
	base := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Args(tt.task)
			if !slices.Equal(got[:len(base)], base) {
				t.Errorf("Args = %q, quiero que empiece con %q", got, base)
			}
			if !slices.Equal(got[len(got)-len(tt.want):], tt.want) {
				t.Errorf("Args = %q, quiero que termine con %q", got, tt.want)
			}
		})
	}
}

func TestTurnAndSend(t *testing.T) {
	c, argsFile := startAgent(t, `cat "$FIXTURES/simple.jsonl"`, agent.Task{Prompt: "hola"})

	events := collectUntil(t, c, isDone)
	var text strings.Builder
	for _, ev := range events {
		if d, ok := ev.(agent.TextDelta); ok {
			text.WriteString(d.Text)
		}
	}
	if _, ok := events[0].(agent.Started); !ok {
		t.Errorf("primer evento = %T, quiero agent.Started", events[0])
	}
	if text.String() != "hola" {
		t.Errorf("texto = %q, quiero \"hola\"", text.String())
	}
	// Done llega con el proceso ya terminado: el estado ya es Idle y Send
	// funciona en el acto, sin esperar.
	if s := c.Status(); s != agent.StatusIdle {
		t.Errorf("Status al recibir Done = %s, quiero idle", s)
	}
	if got := c.SessionID(); got != fixtureSession {
		t.Errorf("SessionID = %q, quiero %q", got, fixtureSession)
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

func TestSendWithoutSession(t *testing.T) {
	if err := New("claude").Send("hola"); !errors.Is(err, agent.ErrNoSession) {
		t.Errorf("Send sin sesión = %v, quiero ErrNoSession", err)
	}
}

func TestStartWhileBusy(t *testing.T) {
	c, _ := startAgent(t, `head -1 "$FIXTURES/simple.jsonl"; sleep 60`, agent.Task{Prompt: "hola"})
	if err := c.Start(context.Background(), agent.Task{Prompt: "otra"}); !errors.Is(err, agent.ErrBusy) {
		t.Errorf("Start con un turno en curso = %v, quiero ErrBusy", err)
	}
}

func TestKillMidTurnLeavesNoProcesses(t *testing.T) {
	// El turno lanza dos nietos, como haría la CLI al usar herramientas.
	c, _ := startAgent(t, `head -3 "$FIXTURES/simple.jsonl"; sleep 60 & sleep 60 & wait`, agent.Task{Prompt: "hola"})
	collectUntil(t, c, isStarted)
	pgid := c.PGID()
	if pgid == 0 {
		t.Fatal("PGID = 0 con un turno en curso")
	}

	if err := c.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	collectUntil(t, c, func(agent.Event) bool { return false }) // hasta que se cierre el canal
	if s := c.Status(); s != agent.StatusDone {
		t.Errorf("Status tras Kill = %s, quiero done", s)
	}

	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("quedan procesos en el grupo %d", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := c.Start(context.Background(), agent.Task{Prompt: "x"}); !errors.Is(err, agent.ErrDone) {
		t.Errorf("Start tras Kill = %v, quiero ErrDone", err)
	}
	if err := c.Kill(); err != nil {
		t.Errorf("segundo Kill = %v, quiero nil", err)
	}
}

func TestConcurrentKillWaitsForFirst(t *testing.T) {
	c, _ := startAgent(t, `head -1 "$FIXTURES/simple.jsonl"; sleep 60 & sleep 60 & wait`, agent.Task{Prompt: "hola"})
	collectUntil(t, c, isStarted)

	// closed vacía lo pendiente sin bloquear y dice si el canal ya se cerró.
	closed := func() bool {
		for {
			select {
			case _, ok := <-c.Events():
				if !ok {
					return true
				}
			default:
				return false
			}
		}
	}
	results := make(chan bool, 2)
	for range 2 {
		go func() {
			c.Kill()
			results <- closed()
		}()
	}
	for range 2 {
		if !<-results {
			t.Error("un Kill volvió antes de que el primero cerrara el canal")
		}
	}
}

func TestExitWithoutResult(t *testing.T) {
	c, _ := startAgent(t, `echo boom >&2; exit 1`, agent.Task{Prompt: "hola"})
	events := collectUntil(t, c, isDone)
	if len(events) != 2 {
		t.Fatalf("eventos = %v, quiero Error y Done", events)
	}
	if e, ok := events[0].(agent.Error); !ok || !strings.Contains(e.Err.Error(), "boom") {
		t.Errorf("primer evento = %v, quiero un Error con el stderr", events[0])
	}
	if d := events[1].(agent.Done); !d.IsError {
		t.Errorf("Done = %+v, quiero IsError", d)
	}
	if s := c.Status(); s != agent.StatusError {
		t.Errorf("Status = %s, quiero error", s)
	}
}

func TestLeftoverProcessDoesNotBlockTurn(t *testing.T) {
	// La CLI deja un proceso en segundo plano que conserva stdout: el turno
	// igual debe terminar y el resto del grupo debe morir.
	// El sleep 0.5 da tiempo a leer el PGID antes de que el líder salga.
	c, _ := startAgent(t, `cat "$FIXTURES/simple.jsonl"; sleep 60 & sleep 0.5; exit 0`, agent.Task{Prompt: "hola"})
	collectUntil(t, c, isStarted)
	pgid := c.PGID()
	collectUntil(t, c, isDone)
	if s := c.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("quedan procesos en el grupo %d", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestErrorResultSetsErrorStatus(t *testing.T) {
	c, _ := startAgent(t, `cat "$FIXTURES/error_model.jsonl"; exit 1`, agent.Task{Prompt: "hola"})
	collectUntil(t, c, isDone)
	waitStatus(t, c, agent.StatusError)
}

func TestMalformedLineDoesNotStopTurn(t *testing.T) {
	c, _ := startAgent(t, `echo 'no es json'; cat "$FIXTURES/simple.jsonl"`, agent.Task{Prompt: "hola"})
	events := collectUntil(t, c, isDone)
	if e, ok := events[0].(agent.Error); !ok || !strings.Contains(e.Err.Error(), TestedVersion) {
		t.Errorf("primer evento = %v, quiero un agent.Error que nombre la versión", events[0])
	}
	waitStatus(t, c, agent.StatusIdle)
}

func TestPauseResume(t *testing.T) {
	c, _ := startAgent(t, `head -1 "$FIXTURES/simple.jsonl"; sleep 60`, agent.Task{Prompt: "hola"})
	collectUntil(t, c, isStarted)

	if err := c.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if s := c.Status(); s != agent.StatusPaused {
		t.Errorf("Status = %s, quiero paused", s)
	}
	if err := c.Pause(); err == nil {
		t.Error("Pause de un turno ya pausado no devolvió error")
	}
	if err := c.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if s := c.Status(); s != agent.StatusRunning {
		t.Errorf("Status = %s, quiero running", s)
	}
}
