package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const wait = 3 * time.Second

func newWatcher(t *testing.T, root string, ignore func(string, bool) bool) *Watcher {
	t.Helper()
	w, err := New(Options{Root: root, Ignore: ignore, Debounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

// next espera el siguiente cambio.
func next(t *testing.T, w *Watcher) Change {
	t.Helper()
	select {
	case c, ok := <-w.Changes():
		if !ok {
			t.Fatal("canal cerrado")
		}
		return c
	case <-time.After(wait):
		t.Fatal("sin cambio a tiempo")
	}
	return Change{}
}

// none comprueba que no llega nada durante un rato.
func none(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case c := <-w.Changes():
		t.Fatalf("cambio inesperado: %+v", c)
	case <-time.After(150 * time.Millisecond):
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWriteRemove(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "uno")
	w := newWatcher(t, root, nil)

	write(t, filepath.Join(root, "b.txt"), "x")
	if c := next(t, w); c.Path != "b.txt" || c.Op != Create {
		t.Fatalf("create: %+v", c)
	}
	write(t, filepath.Join(root, "a.txt"), "dos")
	if c := next(t, w); c.Path != "a.txt" || c.Op != Write {
		t.Fatalf("write: %+v", c)
	}
	if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if c := next(t, w); c.Path != "a.txt" || c.Op != Remove {
		t.Fatalf("remove: %+v", c)
	}
}

func TestDebounceCoalesces(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	write(t, p, "0")
	w := newWatcher(t, root, nil)
	const writes = 20
	for i := 0; i < writes; i++ {
		write(t, p, "x")
	}
	// El debounce funde las ráfagas. En macOS (kqueue) algunos eventos llegan
	// tarde y pueden dar un segundo cambio, así que no se exige exactamente
	// uno: basta con que sean muy pocos, todos sobre el mismo archivo, y que
	// después se calme.
	n := 0
	for {
		select {
		case c := <-w.Changes():
			if c.Path != "a.txt" || c.Op != Write {
				t.Fatalf("cambio inesperado: %+v", c)
			}
			n++
			continue
		case <-time.After(400 * time.Millisecond):
		}
		break
	}
	if n < 1 || n > 3 {
		t.Errorf("%d cambios para %d escrituras seguidas, quiero entre 1 y 3", n, writes)
	}
}

func TestRecursiveAndNewDirs(t *testing.T) {
	root := t.TempDir()
	w := newWatcher(t, root, nil)

	write(t, filepath.Join(root, "x", "y", "z.txt"), "1")
	got := map[string]bool{}
	// Puede llegar como creación del archivo; basta con ver z.txt.
	for len(got) == 0 {
		c := next(t, w)
		got[c.Path] = true
		if c.Path != "x/y/z.txt" {
			t.Fatalf("ruta = %q", c.Path)
		}
	}
	// Un archivo posterior en la carpeta nueva ya se vigila.
	write(t, filepath.Join(root, "x", "y", "w.txt"), "2")
	if c := next(t, w); c.Path != "x/y/w.txt" {
		t.Fatalf("%+v", c)
	}
}

func TestIgnore(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{".git", ".wari", "sub/.git", "out"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ign := func(rel string, isDir bool) bool {
		return IgnoreDefault(rel, isDir) || rel == "out" || rel == "skip.log"
	}
	w := newWatcher(t, root, ign)
	write(t, filepath.Join(root, ".git", "HEAD"), "x")
	write(t, filepath.Join(root, ".wari", "checkpoints", "a", "1", "f"), "x")
	write(t, filepath.Join(root, "sub", ".git", "c"), "x")
	write(t, filepath.Join(root, "out", "o.class"), "x")
	write(t, filepath.Join(root, "skip.log"), "x")
	none(t, w)
	write(t, filepath.Join(root, "ok.txt"), "x")
	if c := next(t, w); c.Path != "ok.txt" {
		t.Fatalf("%+v", c)
	}
}

func TestCloseClosesChannel(t *testing.T) {
	w := newWatcher(t, t.TempDir(), nil)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-w.Changes():
		if ok {
			t.Fatal("no debería haber cambios")
		}
	case <-time.After(wait):
		t.Fatal("el canal no se cerró")
	}
	if err := w.Close(); err != nil {
		t.Fatal("Close debe ser idempotente:", err)
	}
}

func TestMerge(t *testing.T) {
	for _, c := range []struct{ old, op, want Op }{
		{0, Write, Write},
		{Create, Write, Create},
		{Create, Remove, 0},
		{Write, Remove, Remove},
		{Remove, Create, Write},
		{Remove, Write, Write},
	} {
		if got := merge(c.old, c.op); got != c.want {
			t.Errorf("merge(%v,%v) = %v, se esperaba %v", c.old, c.op, got, c.want)
		}
	}
}

func TestNewErrors(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("sin raíz debe fallar")
	}
	f := filepath.Join(t.TempDir(), "f")
	write(t, f, "x")
	if _, err := New(Options{Root: f}); err == nil {
		t.Fatal("un archivo no es raíz")
	}
	if _, err := New(Options{Root: filepath.Join(t.TempDir(), "no")}); err == nil {
		t.Fatal("raíz inexistente")
	}
}
