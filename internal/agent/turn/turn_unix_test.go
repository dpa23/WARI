//go:build unix

package turn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/proc"
)

// testDriver ejecuta un script sh por turno. El script recibe el prompt en
// $1 y la sesión a reanudar en $2, y escribe líneas con un protocolo mínimo:
//
//	start <id>   -> agent.Started
//	text <t>     -> agent.TextDelta
//	done         -> agent.Done
//	fail         -> agent.Done{IsError}
//	cualquier otra línea es un error de parseo
type testDriver struct {
	script   string
	argsFile string
}

func (d testDriver) Name() string { return "fake" }

func (d testDriver) Spec(task agent.Task) (proc.Spec, error) {
	script := fmt.Sprintf("printf '%%s\\n' \"$1\" \"$2\" > '%s'\n%s", d.argsFile, d.script)
	return proc.Spec{
		Path: "/bin/sh",
		Args: []string{"-c", script, "sh", task.Prompt, task.SessionID},
		Dir:  task.Dir,
	}, nil
}

func (testDriver) Parse(line []byte) ([]agent.Event, error) {
	f := strings.Fields(string(line))
	if len(f) == 0 {
		return nil, nil
	}
	switch f[0] {
	case "start":
		return []agent.Event{agent.Started{SessionID: f[1]}}, nil
	case "text":
		return []agent.Event{agent.TextDelta{Text: strings.Join(f[1:], " ")}}, nil
	case "done":
		return []agent.Event{agent.Done{SessionID: "s1"}}, nil
	case "fail":
		return []agent.Event{agent.Done{SessionID: "s1", IsError: true}}, nil
	}
	return nil, fmt.Errorf("línea inesperada: %q", line)
}

// newRunner crea un Runner cuyo turno ejecuta body, y devuelve el archivo
// donde cada turno anota su prompt y su sesión.
func newRunner(t *testing.T, body string) (*Runner, string) {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args")
	r := New(testDriver{script: body, argsFile: argsFile})
	t.Cleanup(func() { r.Close() })
	return r, argsFile
}

func startTurn(t *testing.T, r *Runner, task agent.Task) {
	t.Helper()
	if err := r.Start(context.Background(), task); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

// collectUntil lee eventos hasta que stop devuelve true o se cierra el canal.
func collectUntil(t *testing.T, r *Runner, stop func(agent.Event) bool) []agent.Event {
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

func isStarted(ev agent.Event) bool { _, ok := ev.(agent.Started); return ok }

func waitStatus(t *testing.T, r *Runner, want agent.Status) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.Status() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Status = %s, quiero %s", r.Status(), want)
}

func readArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// waitGroupGone espera a que el grupo pgid no tenga procesos.
func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("quedan procesos en el grupo %d", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// noEvents verifica que no haya eventos pendientes ni lleguen en un rato.
func noEvents(t *testing.T, r *Runner) {
	t.Helper()
	select {
	case ev, ok := <-r.Events():
		if ok {
			t.Fatalf("evento inesperado: %#v", ev)
		}
		t.Fatal("el canal de eventos está cerrado")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestTurnAndSend(t *testing.T) {
	r, argsFile := newRunner(t, `echo "start s1"; echo "text ho"; echo "text la"; echo done`)
	startTurn(t, r, agent.Task{Prompt: "hola"})

	events := collectUntil(t, r, isDone)
	if len(events) != 4 {
		t.Fatalf("eventos = %v, quiero Started, dos TextDelta y Done", events)
	}
	if s, ok := events[0].(agent.Started); !ok || s.SessionID != "s1" {
		t.Errorf("primer evento = %v, quiero Started s1", events[0])
	}
	// Done llega con el proceso ya terminado: el estado ya es Idle y Send
	// funciona en el acto, sin esperar.
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status al recibir Done = %s, quiero idle", s)
	}

	if err := r.Send("otra pregunta"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	collectUntil(t, r, isDone)
	args := readArgs(t, argsFile)
	if !slices.Equal(args, []string{"otra pregunta", "s1"}) {
		t.Errorf("Send no reanudó la sesión: prompt y sesión = %q", args)
	}
}

func TestSendWithoutSession(t *testing.T) {
	r, _ := newRunner(t, `echo done`)
	if err := r.Send("hola"); !errors.Is(err, agent.ErrNoSession) {
		t.Errorf("Send sin sesión = %v, quiero ErrNoSession", err)
	}
}

func TestSpecErrorIsWrappedWithName(t *testing.T) {
	r := New(failingDriver{})
	defer r.Close()
	err := r.Start(context.Background(), agent.Task{Prompt: "x"})
	if err == nil || !strings.HasPrefix(err.Error(), "fake: ") {
		t.Errorf("Start = %v, quiero un error con prefijo \"fake: \"", err)
	}
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
}

type failingDriver struct{ testDriver }

func (failingDriver) Spec(agent.Task) (proc.Spec, error) {
	return proc.Spec{}, errors.New("sin ejecutable")
}

func TestStartWhileBusy(t *testing.T) {
	r, _ := newRunner(t, `echo "start s1"; sleep 60`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	if err := r.Start(context.Background(), agent.Task{Prompt: "otra"}); !errors.Is(err, agent.ErrBusy) {
		t.Errorf("Start con un turno en curso = %v, quiero ErrBusy", err)
	}
}

func TestCloseMidTurnLeavesNoProcesses(t *testing.T) {
	// El turno lanza dos nietos, como haría la CLI al usar herramientas.
	r, _ := newRunner(t, `echo "start s1"; sleep 60 & sleep 60 & wait`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	collectUntil(t, r, isStarted)
	pgid := r.PGID()
	if pgid == 0 {
		t.Fatal("PGID = 0 con un turno en curso")
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	events := collectUntil(t, r, func(agent.Event) bool { return false }) // hasta que se cierre el canal
	for _, ev := range events {
		if isDone(ev) {
			t.Errorf("un turno interrumpido por Close emitió Done: %v", events)
		}
	}
	if s := r.Status(); s != agent.StatusDone {
		t.Errorf("Status tras Close = %s, quiero done", s)
	}
	waitGroupGone(t, pgid)
	if err := r.Start(context.Background(), agent.Task{Prompt: "x"}); !errors.Is(err, agent.ErrDone) {
		t.Errorf("Start tras Close = %v, quiero ErrDone", err)
	}
	if err := r.Cancel(); err != nil {
		t.Errorf("Cancel tras Close = %v, quiero nil", err)
	}
	if err := r.Close(); err != nil {
		t.Errorf("segundo Close = %v, quiero nil", err)
	}
}

func TestConcurrentCloseWaitsForFirst(t *testing.T) {
	r, _ := newRunner(t, `echo "start s1"; sleep 60 & sleep 60 & wait`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	collectUntil(t, r, isStarted)

	// closed vacía lo pendiente sin bloquear y dice si el canal ya se cerró.
	closed := func() bool {
		for {
			select {
			case _, ok := <-r.Events():
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
			r.Close()
			results <- closed()
		}()
	}
	for range 2 {
		if !<-results {
			t.Error("un Close volvió antes de que el primero cerrara el canal")
		}
	}
}

func TestExitWithoutResult(t *testing.T) {
	r, _ := newRunner(t, `echo boom >&2; exit 1`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	events := collectUntil(t, r, isDone)
	if len(events) != 2 {
		t.Fatalf("eventos = %v, quiero Error y Done", events)
	}
	e, ok := events[0].(agent.Error)
	if !ok || !strings.Contains(e.Err.Error(), "boom") || !strings.Contains(e.Err.Error(), "fake") {
		t.Errorf("primer evento = %v, quiero un Error con el nombre de la CLI y el stderr", events[0])
	}
	if d := events[1].(agent.Done); !d.IsError {
		t.Errorf("Done = %+v, quiero IsError", d)
	}
	if s := r.Status(); s != agent.StatusError {
		t.Errorf("Status = %s, quiero error", s)
	}
}

func TestLeftoverProcessDoesNotBlockTurn(t *testing.T) {
	// La CLI deja un proceso en segundo plano que conserva stdout: el turno
	// igual debe terminar y el resto del grupo debe morir.
	// El sleep 0.5 da tiempo a leer el PGID antes de que el líder salga.
	r, _ := newRunner(t, `echo "start s1"; echo done; sleep 60 & sleep 0.5; exit 0`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	collectUntil(t, r, isStarted)
	pgid := r.PGID()
	collectUntil(t, r, isDone)
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
	waitGroupGone(t, pgid)
}

func TestResumeMissingSessionReportsErrSessionNotFound(t *testing.T) {
	r, _ := newRunner(t, `echo fail; echo "No conversation found" >&2; exit 1`)
	startTurn(t, r, agent.Task{Prompt: "hola", SessionID: "perdida"})
	events := collectUntil(t, r, isDone)
	var found bool
	for _, ev := range events {
		if e, ok := ev.(agent.Error); ok && errors.Is(e.Err, agent.ErrSessionNotFound) {
			found = true
		}
	}
	if !found {
		t.Errorf("eventos = %v, quiero un Error con ErrSessionNotFound", events)
	}
	if d := events[len(events)-1].(agent.Done); !d.IsError {
		t.Errorf("Done = %+v, quiero IsError", d)
	}
}

func TestErrorResultSetsErrorStatus(t *testing.T) {
	r, _ := newRunner(t, `echo "start s1"; echo fail; exit 1`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	collectUntil(t, r, isDone)
	waitStatus(t, r, agent.StatusError)
}

func TestMalformedLineDoesNotStopTurn(t *testing.T) {
	r, _ := newRunner(t, `echo 'no es json'; echo "start s1"; echo done`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	events := collectUntil(t, r, isDone)
	if e, ok := events[0].(agent.Error); !ok || !strings.Contains(e.Err.Error(), "no es json") {
		t.Errorf("primer evento = %v, quiero el Error del parser", events[0])
	}
	waitStatus(t, r, agent.StatusIdle)
}

func TestPauseResume(t *testing.T) {
	r, _ := newRunner(t, `echo "start s1"; sleep 60`)
	startTurn(t, r, agent.Task{Prompt: "hola"})
	collectUntil(t, r, isStarted)

	if err := r.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if s := r.Status(); s != agent.StatusPaused {
		t.Errorf("Status = %s, quiero paused", s)
	}
	if err := r.Pause(); err == nil {
		t.Error("Pause de un turno ya pausado no devolvió error")
	}
	if err := r.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if s := r.Status(); s != agent.StatusRunning {
		t.Errorf("Status = %s, quiero running", s)
	}
}

// cancelBody: el turno "lento" deja dos nietos; el resto termina normal.
const cancelBody = `case "$1" in
slow) echo "start s1"; sleep 60 & sleep 60 & wait ;;
*) echo "start s2"; echo "text listo"; echo done ;;
esac`

func TestCancelMidTurnLeavesNoProcesses(t *testing.T) {
	r, argsFile := newRunner(t, cancelBody)
	startTurn(t, r, agent.Task{Prompt: "slow"})
	collectUntil(t, r, isStarted)
	pgid := r.PGID()
	if pgid == 0 {
		t.Fatal("PGID = 0 con un turno en curso")
	}

	if err := r.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status tras Cancel = %s, quiero idle", s)
	}
	if got := r.PGID(); got != 0 {
		t.Errorf("PGID tras Cancel = %d, quiero 0", got)
	}
	waitGroupGone(t, pgid)
	noEvents(t, r) // ni Done ni nada del turno cancelado

	// El agente sigue usable: el turno siguiente funciona y su único Done es
	// el suyo.
	startTurn(t, r, agent.Task{Prompt: "rapido"})
	events := collectUntil(t, r, isDone)
	if len(events) != 3 {
		t.Fatalf("eventos del turno siguiente = %v, quiero Started, TextDelta y Done", events)
	}
	if s, ok := events[0].(agent.Started); !ok || s.SessionID != "s2" {
		t.Errorf("primer evento = %v, quiero Started s2", events[0])
	}
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
	noEvents(t, r)

	// La sesión del turno cancelado se pudo reanudar: Send usa la última.
	if err := r.Send("sigue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	collectUntil(t, r, isDone)
	if args := readArgs(t, argsFile); !slices.Equal(args, []string{"sigue", "s2"}) {
		t.Errorf("prompt y sesión del Send = %q", args)
	}
}

func TestCancelKeepsSessionFromCancelledTurn(t *testing.T) {
	r, argsFile := newRunner(t, `case "$1" in
slow) echo "start s1"; sleep 60 ;;
*) echo "start s1"; echo done ;;
esac`)
	startTurn(t, r, agent.Task{Prompt: "slow"})
	collectUntil(t, r, isStarted)
	if err := r.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := r.Send("sigue"); err != nil {
		t.Fatalf("Send tras Cancel: %v", err)
	}
	collectUntil(t, r, isDone)
	if args := readArgs(t, argsFile); !slices.Equal(args, []string{"sigue", "s1"}) {
		t.Errorf("prompt y sesión del Send = %q, quiero reanudar s1", args)
	}
}

func TestCancelWithoutTurnDoesNothing(t *testing.T) {
	r, _ := newRunner(t, cancelBody)
	if err := r.Cancel(); err != nil {
		t.Errorf("Cancel sin turno = %v, quiero nil", err)
	}
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}

	// Tras un turno terminado tampoco hace nada.
	startTurn(t, r, agent.Task{Prompt: "x"})
	collectUntil(t, r, isDone)
	waitStatus(t, r, agent.StatusIdle)
	if err := r.Cancel(); err != nil {
		t.Errorf("Cancel tras un turno terminado = %v, quiero nil", err)
	}
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
	noEvents(t, r)
	if err := r.Start(context.Background(), agent.Task{Prompt: "y"}); err != nil {
		t.Errorf("Start tras Cancel sin turno = %v", err)
	}
	collectUntil(t, r, isDone)
}

func TestCancelKeepsErrorStatusOfFinishedTurn(t *testing.T) {
	r, _ := newRunner(t, `echo fail; exit 1`)
	startTurn(t, r, agent.Task{Prompt: "x"})
	collectUntil(t, r, isDone)
	waitStatus(t, r, agent.StatusError)
	if err := r.Cancel(); err != nil {
		t.Errorf("Cancel = %v", err)
	}
	if s := r.Status(); s != agent.StatusError {
		t.Errorf("Status = %s, quiero error (el turno ya había terminado)", s)
	}
}

func TestCancelWithFullChannelAndNoReader(t *testing.T) {
	// El turno produce eventos sin parar y nadie lee: el canal se llena y el
	// turno queda bloqueado en emit. Cancel debe desbloquearlo, no dejar
	// eventos del turno cancelado y no emitir Done.
	r, _ := newRunner(t, `echo "start s1"; while :; do echo "text x"; done`)
	startTurn(t, r, agent.Task{Prompt: "x"})
	deadline := time.Now().Add(5 * time.Second)
	for len(r.events) < cap(r.events) {
		if time.Now().After(deadline) {
			t.Fatalf("el canal no se llenó: %d eventos", len(r.events))
		}
		time.Sleep(10 * time.Millisecond)
	}
	pgid := r.PGID()

	cancelled := make(chan error, 1)
	go func() { cancelled <- r.Cancel() }()
	select {
	case err := <-cancelled:
		if err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Cancel se bloqueó con el canal lleno")
	}
	waitGroupGone(t, pgid)
	if n := len(r.events); n != 0 {
		t.Errorf("quedan %d eventos del turno cancelado en el canal", n)
	}
	noEvents(t, r)
	if s := r.Status(); s != agent.StatusIdle {
		t.Errorf("Status = %s, quiero idle", s)
	}
}

func TestCancelWhileReaderIsListening(t *testing.T) {
	// Un lector concurrente (como el del TUI) nunca ve un Done del turno
	// cancelado, aunque el turno emita sin parar mientras se cancela.
	r, _ := newRunner(t, `echo "start s1"; while :; do echo "text x"; done`)
	startTurn(t, r, agent.Task{Prompt: "x"})

	gotDone := make(chan struct{}, 1)
	stop := make(chan struct{})
	readerExit := make(chan struct{})
	go func() {
		defer close(readerExit)
		for {
			select {
			case ev := <-r.Events():
				if isDone(ev) {
					gotDone <- struct{}{}
				}
			case <-stop:
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond) // deja que el turno emita
	if err := r.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
	<-readerExit
	select {
	case <-gotDone:
		t.Error("el turno cancelado emitió Done")
	default:
	}
}

func TestCloseDuringCancel(t *testing.T) {
	r, _ := newRunner(t, cancelBody)
	startTurn(t, r, agent.Task{Prompt: "slow"})
	collectUntil(t, r, isStarted)
	errs := make(chan error, 2)
	go func() { errs <- r.Cancel() }()
	go func() { errs <- r.Close() }()
	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("error: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Cancel y Close concurrentes se bloquearon")
		}
	}
	if s := r.Status(); s != agent.StatusDone {
		t.Errorf("Status = %s, quiero done", s)
	}
}
