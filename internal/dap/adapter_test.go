package dap

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/proc"
)

// TestMain convierte el binario de test en un adaptador DAP falso cuando se
// lanza con WARI_DAP_FAKE: así se prueba el arranque real con internal/proc.
//
//	WARI_DAP_FAKE=ok     adaptador del escenario completo
//	WARI_DAP_FAKE=hang   no responde a disconnect ni sale al cerrar stdin
//
// Con WARI_DAP_CHILD=1 deja además un descendiente en el mismo grupo.
func TestMain(m *testing.M) {
	mode := os.Getenv("WARI_DAP_FAKE")
	if mode == "" {
		os.Exit(m.Run())
	}
	if os.Getenv("WARI_DAP_CHILD") == "1" {
		cmd := exec.Command("sleep", "120")
		if err := cmd.Start(); err != nil {
			os.Exit(3)
		}
	}
	handle := scenario()
	if mode == "hang" {
		sc := handle
		handle = func(f *fakeAdapter, r req) {
			if r.Command == "disconnect" {
				return
			}
			sc(f, r)
		}
	}
	f := newFakeAdapter(os.Stdin, os.Stdout, os.Stdout, handle)
	f.serve()
	if mode == "hang" {
		select {}
	}
	os.Exit(0)
}

// helperLauncher lanza el binario de test como adaptador y recuerda el
// proceso para inspeccionarlo.
type helperLauncher struct {
	mode  string
	child bool
	p     *proc.Process
}

func (h *helperLauncher) launch(ctx context.Context, path string, args []string, dir string) (Process, error) {
	env := append(os.Environ(), "WARI_DAP_FAKE="+h.mode)
	if h.child {
		env = append(env, "WARI_DAP_CHILD=1")
	}
	p, err := proc.Start(ctx, proc.Spec{Path: path, Args: args, Dir: dir, Env: env, Stdin: true, KillGrace: 300 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	h.p = p
	return p, nil
}

func helperAdapter(t *testing.T) Adapter {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Adapter{Name: "fake", AdapterID: "fake", Command: exe, Transport: Stdio}
}

func TestStdioAdapterFullFlowAndNoOrphans(t *testing.T) {
	h := &helperLauncher{mode: "ok", child: true}
	s, err := Open(t.Context(), helperAdapter(t), ConnectOptions{Launcher: h.launch, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(context.Background()) })
	ctx := ctx5(t)
	if err := s.SetBreakpoint(ctx, "/p/Program.cs", 10); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	waitEvent[StoppedEvent](t, s.Events())
	frames, err := s.Stack(ctx, 0)
	if err != nil || len(frames) != 2 {
		t.Fatalf("stack = %+v, %v", frames, err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.p.Done():
	default:
		t.Fatal("el proceso del adaptador sigue vivo tras Stop")
	}
	assertNoOrphans(t, h.p.PGID())
}

func TestStdioAdapterHangingIsKilled(t *testing.T) {
	h := &helperLauncher{mode: "hang", child: true}
	s, err := Open(t.Context(), helperAdapter(t), ConnectOptions{
		Launcher: h.launch,
		Client:   Options{CloseTimeout: 300 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.opts.StopTimeout = 300 * time.Millisecond
	ctx := ctx5(t)
	if err := s.Start(ctx, nil); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = s.Stop(ctx)
	if err == nil {
		t.Fatal("quería error: el adaptador ignoró disconnect")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Stop tardó %v", time.Since(start))
	}
	select {
	case <-h.p.Done():
	default:
		t.Fatal("el proceso sigue vivo")
	}
	assertNoOrphans(t, h.p.PGID())
}

func TestContextCancelKillsAdapterGroup(t *testing.T) {
	h := &helperLauncher{mode: "ok", child: true}
	ctx, cancel := context.WithCancel(t.Context())
	c, err := Connect(ctx, helperAdapter(t), ConnectOptions{Launcher: h.launch})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	cancel()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("la conexión no cayó al cancelar el contexto de vida")
	}
	<-h.p.Done()
	assertNoOrphans(t, h.p.PGID())
}

func TestDefaultLauncherStartsProcess(t *testing.T) {
	p, err := DefaultLauncher(t.Context(), "/bin/cat", nil, "")
	if err != nil {
		t.Skipf("sin /bin/cat: %v", err)
	}
	defer p.Kill()
	if p.Stdin() == nil || p.PGID() <= 0 {
		t.Fatal("proceso sin stdin o pgid")
	}
	if err := p.Kill(); err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	if _, err := DefaultLauncher(t.Context(), "/no/existe/dap", nil, ""); err == nil {
		t.Fatal("quería error al lanzar un binario inexistente")
	}
}

func TestKnownAdapters(t *testing.T) {
	var netcore, java *Adapter
	for i := range Known {
		switch Known[i].Name {
		case "netcoredbg":
			netcore = &Known[i]
		case "java-debug":
			java = &Known[i]
		}
	}
	if netcore == nil || java == nil {
		t.Fatalf("Known = %+v", Known)
	}
	if netcore.Command != "netcoredbg" || len(netcore.Args) != 1 || netcore.Args[0] != "--interpreter=vscode" || netcore.Transport != Stdio {
		t.Fatalf("netcoredbg = %+v", netcore)
	}
	if java.Transport != TCP || java.Command != "" {
		t.Fatalf("java-debug = %+v", java)
	}
	if a, ok := AdapterForPath("/x/Program.CS"); !ok || a.Name != "netcoredbg" {
		t.Fatalf("AdapterForPath(.CS) = %+v, %v", a, ok)
	}
	if a, ok := AdapterForPath("/x/App.java"); !ok || a.Name != "java-debug" {
		t.Fatalf("AdapterForPath(.java) = %+v, %v", a, ok)
	}
	if _, ok := AdapterForPath("/x/a.txt"); ok {
		t.Fatal("txt no tiene adaptador")
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "netcoredbg")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if p, err := Known[0].Detect(); err != nil || p != bin {
		t.Fatalf("Detect = %q, %v", p, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Known[0].Detect(); !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("Detect sin PATH: %v", err)
	}
	// Sin ejecutable propio no es error de Detect.
	if p, err := Known[1].Detect(); err != nil || p != "" {
		t.Fatalf("java-debug Detect = %q, %v", p, err)
	}
}

func TestConnectStdioLauncherInjected(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "netcoredbg")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	var gotPath, gotDir string
	var gotArgs []string
	errBoom := errors.New("boom")
	_, err := Connect(t.Context(), Known[0], ConnectOptions{
		Dir: "/proyecto",
		Launcher: func(ctx context.Context, path string, args []string, d string) (Process, error) {
			gotPath, gotArgs, gotDir = path, args, d
			return nil, errBoom
		},
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v", err)
	}
	if gotPath != bin || gotDir != "/proyecto" || len(gotArgs) != 1 || gotArgs[0] != "--interpreter=vscode" {
		t.Fatalf("launcher recibió %q %v %q", gotPath, gotArgs, gotDir)
	}
}

func TestConnectErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Connect(t.Context(), Known[0], ConnectOptions{}); !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("netcoredbg ausente: %v", err)
	}
	if _, err := Connect(t.Context(), Known[1], ConnectOptions{}); !errors.Is(err, ErrNeedsPortProvider) {
		t.Fatalf("java-debug sin hook: %v", err)
	}
	boom := errors.New("jdtls no está")
	_, err := Connect(t.Context(), Known[1], ConnectOptions{Port: func(context.Context) (string, error) { return "", boom }})
	if !errors.Is(err, boom) {
		t.Fatalf("hook con error: %v", err)
	}
	// Puerto cerrado.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if _, err := ConnectTCP(t.Context(), addr, ConnectOptions{DialTimeout: time.Second}); err == nil {
		t.Fatal("quería error al conectar a un puerto cerrado")
	}
	if _, err := Connect(t.Context(), Adapter{Name: "x", Transport: Stdio}, ConnectOptions{}); !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("adaptador sin comando: %v", err)
	}
}

func TestJavaDebugViaPortHook(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("sin TCP local: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		newFakeAdapter(conn, conn, conn, scenario()).serve()
	}()
	hookCalls := 0
	s, err := Open(t.Context(), Known[1], ConnectOptions{Port: func(ctx context.Context) (string, error) {
		hookCalls++
		return ln.Addr().String(), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := ctx5(t)
	if err := s.Start(ctx, []byte(`{"mainClass":"App"}`)); err != nil {
		t.Fatal(err)
	}
	waitEvent[StoppedEvent](t, s.Events())
	if hookCalls != 1 {
		t.Fatalf("hook llamado %d veces", hookCalls)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
