//go:build unix

package proc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startSh lanza un script de sh y entrega sus líneas de stdout en un canal
// que se cierra al llegar a EOF.
func startSh(t *testing.T, ctx context.Context, script string, grace time.Duration) (*Process, <-chan string) {
	t.Helper()
	p, err := Start(ctx, Spec{Path: "/bin/sh", Args: []string{"-c", script}, KillGrace: grace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(p.Stdout())
		for sc.Scan() {
			lines <- sc.Text()
		}
		// Un error de lectura tras Kill o Close es esperable; los tests
		// verifican las líneas recibidas.
		_ = sc.Err()
	}()
	t.Cleanup(func() {
		p.Kill()
		p.Close()
	})
	return p, lines
}

func waitLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("EOF antes de la línea %q", want)
			}
			if l == want {
				return
			}
		case <-deadline:
			t.Fatalf("no llegó la línea %q", want)
		}
	}
}

// waitGroupGone espera a que no quede ningún proceso (ni zombi) en el grupo.
func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	out, _ := exec.Command("ps", "-e", "-o", "pid=", "-o", "pgid=", "-o", "state=", "-o", "comm=").Output()
	t.Fatalf("quedan procesos en el grupo %d:\n%s", pgid, out)
}

func waitEOF(t *testing.T, lines <-chan string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stdout no llegó a EOF")
		}
	}
}

func TestStdoutAndExitCode(t *testing.T) {
	p, lines := startSh(t, context.Background(), "echo uno; echo dos; exit 3", 0)
	var got []string
	for l := range lines {
		got = append(got, l)
	}
	if strings.Join(got, ",") != "uno,dos" {
		t.Errorf("líneas = %q, quiero [uno dos]", got)
	}
	var exitErr *exec.ExitError
	if err := p.Wait(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("Wait = %v, quiero código de salida 3", err)
	}
}

func TestStderrTail(t *testing.T) {
	p, lines := startSh(t, context.Background(), "echo fallo >&2", 0)
	waitEOF(t, lines)
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := p.Stderr(); !strings.Contains(got, "fallo") {
		t.Errorf("Stderr = %q, quiero que contenga \"fallo\"", got)
	}
}

func TestTailBufferKeepsEnd(t *testing.T) {
	tb := &tailBuffer{max: 4}
	tb.Write([]byte("abc"))
	tb.Write([]byte("defg"))
	if got := tb.String(); got != "defg" {
		t.Errorf("String = %q, quiero \"defg\"", got)
	}
}

func TestKillTerminatesWholeGroup(t *testing.T) {
	// El líder lanza dos nietos que heredan stdout.
	p, lines := startSh(t, context.Background(), "sleep 60 & sleep 60 & echo listo; wait", 0)
	waitLine(t, lines, "listo")

	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitGroupGone(t, p.PGID())
	waitEOF(t, lines)
}

func TestLeaderExitKillsLeftovers(t *testing.T) {
	// El líder sale enseguida y deja un proceso en segundo plano que
	// conserva stdout: sin limpieza, quedaría huérfano y stdout no llegaría
	// nunca a EOF.
	p, lines := startSh(t, context.Background(), "sleep 60 & echo listo", 0)
	waitLine(t, lines, "listo")
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	waitGroupGone(t, p.PGID())
	waitEOF(t, lines)
}

func TestLeftoverIgnoringTermIsKilledAfterGrace(t *testing.T) {
	// El resto hereda el SIGTERM ignorado: la limpieza debe esperar el plazo
	// y luego forzar.
	const grace = 300 * time.Millisecond
	p, lines := startSh(t, context.Background(), `trap "" TERM; sleep 60 & echo listo`, grace)
	waitLine(t, lines, "listo")
	start := time.Now()
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed < grace {
		t.Errorf("la limpieza forzó a los %v, antes del plazo de gracia (%v)", elapsed, grace)
	}
	waitGroupGone(t, p.PGID())
}

func TestKillEscalatesWhenTermIgnored(t *testing.T) {
	// trap "" hace que el líder y sus hijos ignoren SIGTERM.
	const grace = 300 * time.Millisecond
	p, lines := startSh(t, context.Background(),
		`trap "" TERM; echo listo; while :; do sleep 0.1; done`, grace)
	waitLine(t, lines, "listo")

	start := time.Now()
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if elapsed := time.Since(start); elapsed < grace {
		t.Errorf("Kill tardó %v; debió esperar el plazo de gracia (%v) antes de forzar", elapsed, grace)
	}
	waitGroupGone(t, p.PGID())
}

func TestPauseResume(t *testing.T) {
	p, lines := startSh(t, context.Background(),
		`i=0; while :; do echo $i; i=$((i+1)); sleep 0.05; done`, 0)
	waitLine(t, lines, "0")

	if err := p.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// Descarta lo que se escribió antes de la pausa.
	drain := time.After(200 * time.Millisecond)
draining:
	for {
		select {
		case <-lines:
		case <-drain:
			break draining
		}
	}
	select {
	case l := <-lines:
		t.Fatalf("llegó %q con el grupo pausado", l)
	case <-time.After(400 * time.Millisecond):
	}

	if err := p.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	select {
	case <-lines:
	case <-time.After(5 * time.Second):
		t.Fatal("no llegaron líneas después de Resume")
	}
}

func TestKillWhilePaused(t *testing.T) {
	p, lines := startSh(t, context.Background(), "echo listo; sleep 60", 0)
	waitLine(t, lines, "listo")
	if err := p.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	start := time.Now()
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// SIGTERM + SIGCONT debe bastar: sin ellos, Kill esperaría todo el plazo.
	if elapsed := time.Since(start); elapsed >= DefaultKillGrace {
		t.Errorf("Kill de un grupo pausado tardó %v; la terminación ordenada no llegó", elapsed)
	}
	waitGroupGone(t, p.PGID())
}

func TestContextCancelKills(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, lines := startSh(t, ctx, "sleep 60 & echo listo; wait", 0)
	waitLine(t, lines, "listo")

	cancel()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("el proceso sigue vivo después de cancelar el contexto")
	}
	waitGroupGone(t, p.PGID())
}

func TestPauseAfterExit(t *testing.T) {
	p, lines := startSh(t, context.Background(), "exit 0", 0)
	waitEOF(t, lines)
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	waitGroupGone(t, p.PGID())
	if err := p.Pause(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Pause tras salir = %v, quiero ErrNotRunning", err)
	}
	if err := p.Kill(); err != nil {
		t.Errorf("Kill tras salir = %v, quiero nil", err)
	}
}

func TestCloseUnblocksReader(t *testing.T) {
	p, err := Start(context.Background(), Spec{Path: "/bin/sh", Args: []string{"-c", "sleep 60"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer p.Kill()
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(p.Stdout())
		read <- err
	}()
	p.Close()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("Close no desbloqueó al lector")
	}
}

func TestStartErrors(t *testing.T) {
	if _, err := Start(context.Background(), Spec{}); err == nil {
		t.Error("Start sin Path no devolvió error")
	}
	if _, err := Start(context.Background(), Spec{Path: "/no/existe/wari"}); err == nil {
		t.Error("Start de un binario inexistente no devolvió error")
	}
}
