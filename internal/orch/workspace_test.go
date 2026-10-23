package orch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/workspace/checkpoint"
)

// collector vacía Updates en una lista para poder esperar avisos y eventos.
type collector struct {
	mu  sync.Mutex
	ups []Update
}

func collect(s *Supervisor) *collector {
	c := &collector{}
	go func() {
		for u := range s.Updates() {
			c.mu.Lock()
			c.ups = append(c.ups, u)
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *collector) all() []Update {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Update(nil), c.ups...)
}

// notices devuelve los avisos de la clase dada.
func (c *collector) notices(kind NoticeKind) []Notice {
	var out []Notice
	for _, u := range c.all() {
		if u.Notice != nil && u.Notice.Kind == kind {
			out = append(out, *u.Notice)
		}
	}
	return out
}

func (c *collector) waitNotice(t *testing.T, kind NoticeKind, match func(Notice) bool) Notice {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		for _, n := range c.notices(kind) {
			if match == nil || match(n) {
				return n
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("sin aviso de clase %d", kind)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitEdit espera a que el evento FileEditProposed del agente haya sido
// reenviado (el checkpoint ya está hecho en ese punto).
func (c *collector) waitEdit(t *testing.T, id ID, path string) { c.waitEditN(t, id, path, 1) }

// countEdits cuenta los eventos de edición reenviados del agente y la ruta.
func (c *collector) countEdits(id ID, path string) int {
	n := 0
	for _, u := range c.all() {
		if ev, ok := u.Event.(agent.FileEditProposed); ok && u.ID == id && ev.Path == path {
			n++
		}
	}
	return n
}

// waitEditN espera a que hayan llegado al menos n eventos de edición.
func (c *collector) waitEditN(t *testing.T, id ID, path string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c.countEdits(id, path) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("el evento de edición de %s no llegó", path)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type wsEnv struct {
	t     *testing.T
	root  string
	store *checkpoint.Store
	s     *Supervisor
	c     *collector
}

func newWsEnv(t *testing.T, watch bool) *wsEnv {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s := New(4)
	t.Cleanup(s.Close)
	if err := s.AttachWorkspace(Workspace{
		Root: root, Store: store, RunID: "t-", Watch: watch, Debounce: 20 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	return &wsEnv{t: t, root: root, store: store, s: s, c: collect(s)}
}

func (w *wsEnv) path(rel string) string { return filepath.Join(w.root, filepath.FromSlash(rel)) }

func (w *wsEnv) write(rel, body string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(w.path(rel)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.path(rel), []byte(body), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wsEnv) read(rel string) string {
	w.t.Helper()
	b, err := os.ReadFile(w.path(rel))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(b)
}

func (w *wsEnv) hash(rel string) string {
	w.t.Helper()
	h, err := checkpoint.SHA256(w.path(rel))
	if err != nil {
		w.t.Fatal(err)
	}
	return h
}

// start lanza un agente falso en marcha.
func (w *wsEnv) start(name string) (ID, *fake) {
	f := newFake()
	id := w.s.Submit(name, f, agent.Task{Prompt: name}, Budget{})
	waitState(w.t, w.s, id, Running)
	return id, f
}

// propose hace lo que haría una CLI: anuncia la edición y, cuando WARI ya la
// vio, la ejecuta.
func (w *wsEnv) propose(id ID, f *fake, rel, newBody string) {
	w.t.Helper()
	abs := w.path(rel)
	n := w.c.countEdits(id, abs)
	f.emit(agent.FileEditProposed{ToolCallID: "t", Tool: "Edit", Path: abs})
	w.c.waitEditN(w.t, id, abs, n+1)
	w.write(rel, newBody)
}

func TestTwoAgentsDistinctFilesInParallel(t *testing.T) {
	w := newWsEnv(t, false)
	w.write("a.txt", "A0")
	w.write("b.txt", "B0")
	hashA, hashB := w.hash("a.txt"), w.hash("b.txt")
	a, fa := w.start("agente 1")
	b, fb := w.start("agente 2")

	w.propose(a, fa, "a.txt", "A1")
	w.propose(b, fb, "b.txt", "B1")

	wantState(t, w.s, a, Running)
	wantState(t, w.s, b, Running)
	if n := w.c.notices(NoticeConflict); len(n) != 0 {
		t.Fatalf("avisos de choque: %+v", n)
	}
	if got := w.s.Claims(a); len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("claims de a = %v", got)
	}
	if h := w.s.Holder(w.path("b.txt")); h != b {
		t.Fatalf("dueño de b.txt = %d", h)
	}
	// Los checkpoints guardan el contenido previo.
	oldA, curA, err := w.s.Diff(a, 1, "a.txt")
	if err != nil || string(oldA) != "A0" || string(curA) != "A1" {
		t.Fatalf("diff a: %q %q %v", oldA, curA, err)
	}
	// Revertir a no toca lo de b.
	if err := w.s.Pause(a); err != nil {
		t.Fatal(err)
	}
	if err := w.s.RevertTurn(a, 1); err != nil {
		t.Fatal(err)
	}
	if w.hash("a.txt") != hashA || w.read("b.txt") != "B1" {
		t.Fatal("el revert de a debe dejar a.txt idéntico y no tocar b.txt")
	}
	if err := w.s.Pause(b); err != nil {
		t.Fatal(err)
	}
	if err := w.s.RevertAgent(b); err != nil {
		t.Fatal(err)
	}
	if w.hash("b.txt") != hashB {
		t.Fatal("RevertAgent no dejó b.txt idéntico")
	}
}

func TestConflictPausesSecondAgentBeforeItEdits(t *testing.T) {
	w := newWsEnv(t, false)
	w.write("x.txt", "X0")
	a, fa := w.start("agente 1")
	b, fb := w.start("agente 2")

	w.propose(a, fa, "x.txt", "X-por-1")
	// b propone editar lo mismo: choque. Todavía no ha editado nada.
	fb.emit(agent.FileEditProposed{Tool: "Edit", Path: w.path("x.txt")})
	n := w.c.waitNotice(t, NoticeConflict, nil)
	if n.Agent != b || n.Holder != a || n.Path != "x.txt" {
		t.Fatalf("aviso = %+v", n)
	}
	if want := "choque: agente 2 quiere editar x.txt, reclamado por agente 1"; n.Text != want {
		t.Fatalf("texto = %q", n.Text)
	}
	waitState(t, w.s, b, Paused)
	if fb.get(&fb.pauses) != 1 {
		t.Fatal("el agente no recibió Pause")
	}
	wantState(t, w.s, a, Running)
	if w.read("x.txt") != "X-por-1" {
		t.Fatal("el archivo no debe haber cambiado por b")
	}
	if h := w.s.Holder(w.path("x.txt")); h != a {
		t.Fatalf("el dueño sigue siendo a, no %d", h)
	}
	// El checkpoint de b existe (contenido tras la edición de a).
	if old, _, err := w.s.Diff(b, 1, "x.txt"); err != nil || string(old) != "X-por-1" {
		t.Fatalf("checkpoint de b: %q %v", old, err)
	}

	// Reanudar es la decisión del usuario: b toma el archivo.
	if err := w.s.Resume(b); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.s, b, Running)
	if h := w.s.Holder(w.path("x.txt")); h != b {
		t.Fatalf("tras reanudar el dueño es b, no %d", h)
	}
	tk := w.c.waitNotice(t, NoticeTakeover, nil)
	if tk.Agent != b || tk.Holder != a {
		t.Fatalf("takeover = %+v", tk)
	}
}

func TestConflictStopDiscardsAndReleaseFrees(t *testing.T) {
	w := newWsEnv(t, false)
	w.write("x.txt", "X0")
	a, fa := w.start("agente 1")
	b, fb := w.start("agente 2")
	w.propose(a, fa, "x.txt", "1")
	fb.emit(agent.FileEditProposed{Path: w.path("x.txt")})
	waitState(t, w.s, b, Paused)

	if err := w.s.Stop(b); err != nil {
		t.Fatal(err)
	}
	wantState(t, w.s, b, Stopped)
	if h := w.s.Holder(w.path("x.txt")); h != a {
		t.Fatalf("detener a b no cambia al dueño: %d", h)
	}
	// Detener al dueño libera sus archivos.
	if err := w.s.Stop(a); err != nil {
		t.Fatal(err)
	}
	if h := w.s.Holder(w.path("x.txt")); h != 0 {
		t.Fatalf("tras detener a a nadie reclama x.txt, dueño %d", h)
	}
	// Claim explícito y Release manual.
	if err := w.s.Claim(b, "dir/"); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Claim(a, "dir/f.go"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Claim dentro de carpeta ajena = %v", err)
	}
	if err := w.s.Release(b); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Claim(a, "dir/f.go"); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetExceededReleasesClaims(t *testing.T) {
	w := newWsEnv(t, false)
	f := newFake()
	id := w.s.Submit("a", f, agent.Task{Prompt: "a"}, Budget{MaxTokens: 10})
	waitState(t, w.s, id, Running)
	f.emit(agent.FileEditProposed{Path: w.path("z.txt")})
	w.c.waitEdit(t, id, w.path("z.txt"))
	f.emit(agent.Usage{InputTokens: 20})
	waitState(t, w.s, id, BudgetExceeded)
	if h := w.s.Holder(w.path("z.txt")); h != 0 {
		t.Fatalf("dueño = %d", h)
	}
}

func TestRevertTurnIdenticalToCheckpointAndNewFile(t *testing.T) {
	w := newWsEnv(t, false)
	w.write("a.txt", "original\n")
	w.write("sub/b.txt", "otro\n")
	hashA, hashB := w.hash("a.txt"), w.hash("sub/b.txt")
	id, f := w.start("agente 1")

	w.propose(id, f, "a.txt", "cambiado 1\n")
	w.propose(id, f, "sub/b.txt", "cambiado 2\n")
	w.propose(id, f, "nuevo.txt", "creado por el agente\n")
	w.write("a.txt", "cambiado otra vez\n")

	// Corriendo, no se puede revertir.
	if err := w.s.RevertTurn(id, 1); !errors.Is(err, ErrState) {
		t.Fatalf("RevertTurn corriendo = %v", err)
	}
	if err := w.s.Pause(id); err != nil {
		t.Fatal(err)
	}
	if err := w.s.RevertTurn(id, 1); err != nil {
		t.Fatal(err)
	}
	if w.hash("a.txt") != hashA || w.hash("sub/b.txt") != hashB {
		t.Fatal("los archivos no quedaron idénticos al checkpoint (SHA256)")
	}
	if _, err := os.Stat(w.path("nuevo.txt")); !os.IsNotExist(err) {
		t.Fatalf("el archivo nuevo debía borrarse: %v", err)
	}
	eds := w.s.Edits(id)
	if len(eds) != 3 || eds[0] != (Edit{Turn: 1, Path: "a.txt", Saved: true}) {
		t.Fatalf("Edits = %+v", eds)
	}
	// RevertFile de un archivo sin checkpoint en ese turno falla sin tocar nada.
	if err := w.s.RevertFile(id, 9, "a.txt"); err == nil {
		t.Fatal("turno inexistente debe fallar")
	}
}

func TestEditsDedupAndTurns(t *testing.T) {
	w := newWsEnv(t, false)
	w.write("a.txt", "0")
	id, f := w.start("agente 1")
	w.propose(id, f, "a.txt", "1")
	w.propose(id, f, "a.txt", "2") // mismo turno: no repite
	if got := w.s.Edits(id); len(got) != 1 {
		t.Fatalf("Edits = %+v", got)
	}
	// Segundo turno: otro checkpoint con el contenido de ese momento.
	f.emit(agent.Done{SessionID: "s"})
	waitState(t, w.s, id, Idle)
	if err := w.s.Send(id, "más"); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.s, id, Running)
	w.propose(id, f, "a.txt", "3")
	eds := w.s.Edits(id)
	if len(eds) != 2 || eds[1].Turn != 2 {
		t.Fatalf("Edits = %+v", eds)
	}
	old, cur, err := w.s.Diff(id, 2, "a.txt")
	if err != nil || string(old) != "2" || string(cur) != "3" {
		t.Fatalf("diff turno 2: %q %q %v", old, cur, err)
	}
	old, _, err = w.s.Diff(id, 1, "a.txt")
	if err != nil || string(old) != "0" {
		t.Fatalf("diff turno 1: %q %v", old, err)
	}
}

func TestOutsideRootAndNoStore(t *testing.T) {
	w := newWsEnv(t, false)
	id, f := w.start("agente 1")
	outside := filepath.Join(t.TempDir(), "fuera.txt")
	f.emit(agent.FileEditProposed{Path: outside})
	w.c.waitEdit(t, id, outside)
	eds := w.s.Edits(id)
	if len(eds) != 1 || eds[0].Saved {
		t.Fatalf("Edits = %+v", eds)
	}
	if h := w.s.Holder(outside); h != id {
		t.Fatalf("la ruta fuera de la raíz también se reclama: %d", h)
	}

	// Sin almacén.
	s := New(2)
	defer s.Close()
	g := newFake()
	gid := s.Submit("g", g, agent.Task{Prompt: "g"}, Budget{})
	waitState(t, s, gid, Running)
	if err := s.RevertAgent(gid); !errors.Is(err, ErrNoCheckpoints) {
		t.Fatalf("RevertAgent sin almacén = %v", err)
	}
	if err := s.RevertAgent(99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("agente inexistente = %v", err)
	}
}

func TestCheckpointFailureIsReported(t *testing.T) {
	w := newWsEnv(t, false)
	if err := os.MkdirAll(w.path("dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	id, f := w.start("agente 1")
	f.emit(agent.FileEditProposed{Path: w.path("dir")}) // una carpeta no es archivo
	n := w.c.waitNotice(t, NoticeCheckpointFailed, nil)
	if n.Agent != id || !strings.Contains(n.Text, "dir") {
		t.Fatalf("aviso = %+v", n)
	}
	w.c.waitEdit(t, id, w.path("dir")) // el evento se reenvía igual
}

func TestAttribution(t *testing.T) {
	w := newWsEnv(t, false)
	w.write("a.txt", "0")
	// Sin agentes corriendo: desconocido.
	if id, how := w.s.Attribute("a.txt"); id != 0 || how != AttrUnknown {
		t.Fatalf("sin agentes: %d %v", id, how)
	}
	a, fa := w.start("agente 1")
	// Un solo agente corriendo, sin propuesta: él.
	if id, how := w.s.Attribute(w.path("a.txt")); id != a || how != AttrSoleActive {
		t.Fatalf("único activo: %d %v", id, how)
	}
	b, fb := w.start("agente 2")
	// Dos corriendo sin propuesta: desconocido.
	if id, how := w.s.Attribute("a.txt"); id != 0 || how != AttrUnknown {
		t.Fatalf("dos activos: %d %v", id, how)
	}
	// El último que propuso gana.
	fa.emit(agent.FileEditProposed{Path: w.path("a.txt")})
	w.c.waitEdit(t, a, w.path("a.txt"))
	if id, how := w.s.Attribute("a.txt"); id != a || how != AttrProposal {
		t.Fatalf("propuesta de a: %d %v", id, how)
	}
	fb.emit(agent.FileEditProposed{Path: w.path("a.txt")}) // choca y queda pausado
	waitState(t, w.s, b, Paused)
	if id, how := w.s.Attribute("a.txt"); id != b || how != AttrProposal {
		t.Fatalf("la última propuesta (b, pausado) gana: %d %v", id, how)
	}
	// Una propuesta de un turno anterior ya no cuenta.
	fa.emit(agent.Done{})
	waitState(t, w.s, a, Idle)
	if id, how := w.s.Attribute("a.txt"); id != b || how != AttrProposal {
		t.Fatalf("a terminó su turno: %d %v", id, how)
	}
	if err := w.s.Stop(b); err != nil {
		t.Fatal(err)
	}
	if id, how := w.s.Attribute("a.txt"); id != 0 || how != AttrUnknown {
		t.Fatalf("nadie corre: %d %v", id, how)
	}
}

func TestWatcherNoticesAndAttribution(t *testing.T) {
	w := newWsEnv(t, true)
	w.write("a.txt", "0")
	w.write("b.txt", "0")
	time.Sleep(50 * time.Millisecond)
	a, fa := w.start("agente 1")

	// Cambio con propuesta previa: atribuido por propuesta.
	w.propose(a, fa, "a.txt", "1")
	n := w.c.waitNotice(t, NoticeChanged, func(n Notice) bool { return n.Path == "a.txt" && n.Op == "write" })
	if n.Agent != a || n.Attr != AttrProposal {
		t.Fatalf("aviso = %+v", n)
	}

	// Cambio hecho por un comando de shell: único activo.
	w.write("b.txt", "shell")
	n = w.c.waitNotice(t, NoticeChanged, func(n Notice) bool { return n.Path == "b.txt" && n.Op == "write" })
	if n.Agent != a || n.Attr != AttrSoleActive {
		t.Fatalf("aviso = %+v", n)
	}
	var detected bool
	for _, e := range w.s.Edits(a) {
		detected = detected || (e.Path == "b.txt" && e.Detected && !e.Saved)
	}
	if !detected {
		t.Fatalf("Edits = %+v", w.s.Edits(a))
	}

	// Escritura del usuario: sin aviso.
	w.s.NoteUserWrite(w.path("c.txt"))
	w.write("c.txt", "usuario")
	// Con dos agentes corriendo: desconocido.
	b, _ := w.start("agente 2")
	_ = b
	w.write("d.txt", "?")
	n = w.c.waitNotice(t, NoticeChanged, func(n Notice) bool { return n.Path == "d.txt" })
	if n.Agent != 0 || n.Attr != AttrUnknown {
		t.Fatalf("aviso = %+v", n)
	}
	for _, x := range w.c.notices(NoticeChanged) {
		if x.Path == "c.txt" {
			t.Fatalf("el guardado del usuario no debe atribuirse: %+v", x)
		}
	}
	// Los checkpoints (.wari) no generan avisos.
	for _, x := range w.c.notices(NoticeChanged) {
		if strings.HasPrefix(x.Path, ".wari") {
			t.Fatalf("aviso de .wari: %+v", x)
		}
	}
}

func TestRevertIsNotAttributedToAgent(t *testing.T) {
	w := newWsEnv(t, true)
	w.write("a.txt", "0")
	time.Sleep(50 * time.Millisecond)
	a, fa := w.start("agente 1")
	w.propose(a, fa, "a.txt", "1")
	w.c.waitNotice(t, NoticeChanged, func(n Notice) bool { return n.Path == "a.txt" && n.Op == "write" })
	if err := w.s.Pause(a); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	before := len(w.c.notices(NoticeChanged))
	if err := w.s.RevertTurn(a, 1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if after := len(w.c.notices(NoticeChanged)); after != before {
		t.Fatalf("el revert generó %d aviso(s) de cambio: %+v", after-before, w.c.notices(NoticeChanged))
	}
}

func TestCloseWithWatcherIsClean(t *testing.T) {
	root := t.TempDir()
	s := New(2)
	if err := s.AttachWorkspace(Workspace{Root: root, Watch: true}); err != nil {
		t.Fatal(err)
	}
	f := newFake()
	s.Submit("a", f, agent.Task{Prompt: "a"}, Budget{})
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close no terminó")
	}
	if err := s.AttachWorkspace(Workspace{Root: root, Watch: true}); !errors.Is(err, ErrClosed) {
		t.Fatalf("AttachWorkspace tras Close = %v", err)
	}
}

// Dos agentes anuncian la edición del mismo archivo en el mismo instante: debe
// haber un solo dueño y un solo choque, sin importar quién gane la carrera.
func TestSimultaneousProposalsOnSameFileYieldOneOwnerAndOneConflict(t *testing.T) {
	for round := 0; round < 20; round++ {
		w := newWsEnv(t, false)
		w.write("x.txt", "X0")
		a, fa := w.start("agente 1")
		b, fb := w.start("agente 2")

		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, f := range []*fake{fa, fb} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				f.emit(agent.FileEditProposed{Tool: "Edit", Path: w.path("x.txt")})
			}()
		}
		close(start)
		wg.Wait()

		n := w.c.waitNotice(t, NoticeConflict, nil)
		holder := w.s.Holder(w.path("x.txt"))
		if holder != a && holder != b {
			t.Fatalf("ronda %d: el dueño %d no es ninguno de los dos", round, holder)
		}
		if n.Holder != holder || n.Agent == holder || (n.Agent != a && n.Agent != b) {
			t.Fatalf("ronda %d: choque %+v con dueño %d", round, n, holder)
		}
		waitState(t, w.s, n.Agent, Paused)
		if got := len(w.c.notices(NoticeConflict)); got != 1 {
			t.Fatalf("ronda %d: %d avisos de choque, quería 1", round, got)
		}
	}
}

// closingStore es un Checkpointer que cuenta cuántas veces se cierra.
type closingStore struct{ closed int }

func (*closingStore) Save(string, int, string) error                   { return nil }
func (*closingStore) RevertFile(string, int, string) error             { return nil }
func (*closingStore) RevertTurn(string, int) error                     { return nil }
func (*closingStore) RevertAgent(string) error                         { return nil }
func (*closingStore) Diff(string, int, string) ([]byte, []byte, error) { return nil, nil, nil }
func (c *closingStore) Close() error                                   { c.closed++; return nil }

// El supervisor suelta el almacén de checkpoints al cerrarse (informe H12).
func TestSupervisorCloseClosesTheCheckpointStore(t *testing.T) {
	store := &closingStore{}
	sup := New(1)
	if err := sup.AttachWorkspace(Workspace{Root: t.TempDir(), Store: store, RunID: "t-"}); err != nil {
		t.Fatal(err)
	}
	sup.Close()
	sup.Close()
	if store.closed != 1 {
		t.Fatalf("el almacén debía cerrarse una vez, se cerró %d", store.closed)
	}
}
