package lsp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeLauncher struct {
	mu    sync.Mutex
	n     atomic.Int32
	procs []*fakeProc
	fail  error
	delay time.Duration
	paths []string
	args  [][]string
	dirs  []string
}

func (l *fakeLauncher) launch(t *testing.T) Launcher {
	return func(ctx context.Context, path string, args []string, dir string) (Process, error) {
		l.n.Add(1)
		time.Sleep(l.delay)
		if l.fail != nil {
			return nil, l.fail
		}
		p, _ := newProcPair(t, defaultHandle(nil))
		l.mu.Lock()
		l.procs = append(l.procs, p)
		l.paths = append(l.paths, path)
		l.args = append(l.args, args)
		l.dirs = append(l.dirs, dir)
		l.mu.Unlock()
		return p, nil
	}
}

func TestManagerStartsOnDemandAndReuses(t *testing.T) {
	fakePath(t, "jdtls", "csharp-ls")
	l := &fakeLauncher{delay: 20 * time.Millisecond}
	m := NewManager(context.Background(), l.launch(t), Options{ShutdownTimeout: time.Second})
	defer m.Shutdown()
	if l.n.Load() != 0 || len(m.Status()) != 0 {
		t.Fatal("nada debe arrancar antes del primer Get")
	}
	root := t.TempDir()

	var wg sync.WaitGroup
	clients := make([]*Client, 8)
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := m.Get(bg(t), Known[0], root)
			if err != nil {
				t.Error(err)
			}
			clients[i] = c
		}()
	}
	wg.Wait()
	if l.n.Load() != 1 {
		t.Fatalf("lanzamientos=%d, quería 1 compartido", l.n.Load())
	}
	for _, c := range clients {
		if c != clients[0] {
			t.Fatal("Get concurrente debe devolver el mismo cliente")
		}
	}
	if l.dirs[0] != root || len(l.args[0]) != 2 || l.args[0][0] != "-data" {
		t.Errorf("dir=%q args=%v", l.dirs[0], l.args[0])
	}
	if !clients[0].Capabilities().Completion {
		t.Error("el cliente debía estar inicializado")
	}

	other, err := m.Get(bg(t), Known[2], root)
	if err != nil || other == clients[0] || l.n.Load() != 2 {
		t.Fatalf("segundo servidor: err=%v n=%d", err, l.n.Load())
	}
	st := m.Status()
	if len(st) != 2 || st[0].Name != "csharp-ls" || st[1].Name != "jdtls" || st[1].Root != root {
		t.Errorf("Status=%+v", st)
	}
	if st[0].RSSErr == nil {
		t.Error("el proceso falso no tiene PGID: RSSErr debía informarlo")
	}
}

func TestManagerRestartsDeadServer(t *testing.T) {
	fakePath(t, "jdtls")
	l := &fakeLauncher{}
	m := NewManager(context.Background(), l.launch(t), Options{ShutdownTimeout: time.Second})
	defer m.Shutdown()
	root := t.TempDir()
	c1, err := m.Get(bg(t), Known[0], root)
	if err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.procs[0].stdin.Close() // el servidor falso sale al ver EOF
	l.mu.Unlock()
	select {
	case <-c1.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("el cliente no detectó la caída")
	}
	if got := m.Status(); len(got) != 0 {
		t.Errorf("Status debía ocultar al servidor caído: %+v", got)
	}
	c2, err := m.Get(bg(t), Known[0], root)
	if err != nil || c2 == c1 || l.n.Load() != 2 {
		t.Fatalf("reinicio: err=%v n=%d", err, l.n.Load())
	}
}

func TestManagerMissingServerAndLaunchFailure(t *testing.T) {
	fakePath(t) // PATH vacío
	l := &fakeLauncher{}
	m := NewManager(context.Background(), l.launch(t), Options{})
	if _, err := m.Get(bg(t), Known[0], t.TempDir()); !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("err=%v", err)
	}
	if l.n.Load() != 0 {
		t.Error("no debe lanzarse un servidor ausente")
	}
	fakePath(t, "jdtls")
	l.fail = errors.New("sin permisos")
	if _, err := m.Get(bg(t), Known[0], t.TempDir()); err == nil {
		t.Fatal("quería error de lanzamiento")
	}
	l.fail = nil
	if _, err := m.Get(bg(t), Known[0], t.TempDir()); err != nil {
		t.Fatalf("tras un fallo debe poder reintentarse: %v", err)
	}
	m.Shutdown()
}

func TestManagerInitTimeoutKillsProcess(t *testing.T) {
	fakePath(t, "jdtls")
	var proc *fakeProc
	launch := func(ctx context.Context, path string, args []string, dir string) (Process, error) {
		// Servidor que nunca responde initialize.
		p, _ := newProcPair(t, func(f *fakeServer, m message) {})
		proc = p
		return p, nil
	}
	m := NewManager(context.Background(), launch, Options{ShutdownTimeout: 50 * time.Millisecond})
	m.InitTimeout = 100 * time.Millisecond
	_, err := m.Get(bg(t), Known[0], t.TempDir())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if proc.killCount() != 1 {
		t.Errorf("kills=%d: un servidor que no inicia debe matarse", proc.killCount())
	}
	if len(m.Status()) != 0 {
		t.Error("no debe quedar entrada")
	}
}

func TestManagerShutdownClosesAll(t *testing.T) {
	fakePath(t, "jdtls", "csharp-ls")
	l := &fakeLauncher{}
	m := NewManager(context.Background(), l.launch(t), Options{ShutdownTimeout: time.Second})
	root := t.TempDir()
	m.Get(bg(t), Known[0], root)
	m.Get(bg(t), Known[2], root)
	if err := m.Shutdown(); err != nil {
		t.Fatal(err)
	}
	for i, p := range l.procs {
		if p.killCount() != 1 {
			t.Errorf("proc %d: kills=%d", i, p.killCount())
		}
		select {
		case <-p.Done():
		default:
			t.Errorf("proc %d sigue vivo", i)
		}
	}
	if len(m.Status()) != 0 {
		t.Error("Status debía quedar vacío")
	}
}
