package sandbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeRuntime instala en el PATH un "podman" que registra sus argumentos, uno
// por línea, y se comporta según variables de entorno:
//
//	FAKE_STDOUT / FAKE_STDERR  texto que "el contenedor" escribe
//	FAKE_RUNTIME_ERR           mensaje que el CLI escribe en su stderr
//	FAKE_HANG                  el run no termina por sí solo
//	FAKE_RM_ERR                el rm falla con ese mensaje
const fakeScript = `#!/bin/sh
case "$1" in
--version) echo "$FAKE_NAME version 9.9.9"; exit 0 ;;
esac
{
	printf '== %s\n' "$1"
	for a in "$@"; do printf '%s\n' "$a"; done
} >> "$FAKE_LOG"
case "$1" in
run)
	out=
	for a in "$@"; do
		case "$a" in
		type=bind,src=*,dst=/out) s=${a#type=bind,src=}; out=${s%,dst=/out} ;;
		esac
	done
	[ -n "$FAKE_SYMLINK_STDERR" ] && ln -s "$FAKE_SYMLINK_STDERR" "$out/.stderr.log"
	[ -n "$FAKE_STDOUT" ] && printf '%s' "$FAKE_STDOUT"
	[ -n "$FAKE_STDERR" ] && printf '%s' "$FAKE_STDERR" >> "$out/.stderr.log"
	[ -n "$FAKE_RUNTIME_ERR" ] && printf '%s\n' "$FAKE_RUNTIME_ERR" >&2
	[ -n "$FAKE_HANG" ] && exec sleep 60
	exit "${FAKE_EXIT:-0}"
	;;
rm)
	if [ -n "$FAKE_RM_ERR" ]; then printf '%s\n' "$FAKE_RM_ERR" >&2; exit 1; fi
	;;
esac
`

func installFake(t *testing.T, names ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("el runtime falso es un guion de sh")
	}
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls.log")
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(strings.Replace(fakeScript, "$FAKE_NAME", n, 1)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", log)
	return log
}

// calls devuelve cada invocación registrada: verbo seguido de argumentos.
func calls(t *testing.T, log string) [][]string {
	t.Helper()
	b, err := os.ReadFile(log)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.HasPrefix(l, "== ") {
			out = append(out, nil)
			continue
		}
		out[len(out)-1] = append(out[len(out)-1], l)
	}
	return out
}

func testProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "hola")
	return root
}

func collect(t *testing.T, r *Run) []Line {
	t.Helper()
	var lines []Line
	timeout := time.After(15 * time.Second)
	for {
		select {
		case l, ok := <-r.Logs():
			if !ok {
				return lines
			}
			lines = append(lines, l)
		case <-timeout:
			t.Fatal("los logs no se cerraron")
		}
	}
}

func TestDetectRuntime(t *testing.T) {
	installFake(t, "podman", "docker")
	rt, err := DetectRuntime(context.Background(), "")
	if err != nil || rt.Name != "podman" || !strings.Contains(rt.Version, "9.9.9") {
		t.Fatalf("rt = %+v err = %v", rt, err)
	}
	rt, err = DetectRuntime(context.Background(), "docker")
	if err != nil || rt.Name != "docker" {
		t.Fatalf("preferido docker: rt = %+v err = %v", rt, err)
	}
}

func TestDetectRuntimeNone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := DetectRuntime(context.Background(), ""); !errors.Is(err, ErrNoRuntime) {
		t.Fatalf("err = %v", err)
	}
	if _, err := (&Container{}).Run(context.Background(), Spec{Root: t.TempDir(), Image: "x", Command: []string{"y"}}); !errors.Is(err, ErrNoRuntime) {
		t.Fatalf("Run err = %v", err)
	}
}

func TestRunStreamsLogsPerChannel(t *testing.T) {
	log := installFake(t, "podman")
	t.Setenv("FAKE_STDOUT", "uno\ndos\nsin-salto")
	t.Setenv("FAKE_STDERR", "err1\r\nerr2\n")
	t.Setenv("FAKE_RUNTIME_ERR", "aviso del runtime")
	c := &Container{RunsDir: t.TempDir()}
	root := testProject(t)
	run, err := c.Run(context.Background(), Spec{Root: root, Image: "alpine:3", Command: []string{"echo", "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	lines := collect(t, run)
	var got = map[Stream][]string{}
	for _, l := range lines {
		got[l.Stream] = append(got[l.Stream], l.Text)
	}
	if strings.Join(got[Stdout], "|") != "uno|dos|sin-salto" {
		t.Errorf("stdout = %v", got[Stdout])
	}
	if strings.Join(got[Stderr], "|") != "err1|err2" {
		t.Errorf("stderr = %v", got[Stderr])
	}
	if strings.Join(got[RuntimeLog], "|") != "aviso del runtime" {
		t.Errorf("runtime = %v", got[RuntimeLog])
	}
	if err := run.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	select {
	case <-run.Done():
	default:
		t.Error("Done debía estar cerrado")
	}

	cs := calls(t, log)
	if len(cs) != 1 || cs[0][0] != "run" {
		t.Fatalf("llamadas = %v", cs)
	}
	args := cs[0]
	if !hasPair(args, "--name", run.Name) || !strings.HasPrefix(run.Name, "wari-") {
		t.Errorf("nombre: %v", args)
	}
	// El sandbox monta la raíz con los enlaces resueltos; en macOS el
	// directorio temporal está bajo /var, un enlace a /private/var.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPair(args, "--network", "none") || !hasPair(args, "-v", realRoot+":/workspace:O") {
		t.Errorf("args = %v", args)
	}
	if filepath.Dir(run.OutDir) != c.RunsDir || filepath.Base(run.OutDir) != run.ID {
		t.Errorf("OutDir = %q", run.OutDir)
	}
	if err := run.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.OutDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("OutDir sigue: %v", err)
	}
}

func TestTailStderrSymlinkSecretNotRead(t *testing.T) {
	skipNoSymlinks(t)
	installFake(t, "podman")
	secretDir := t.TempDir()
	secretFile := filepath.Join(secretDir, "secret.txt")
	write(t, secretFile, "SECRET_TOKEN_12345\n")

	t.Setenv("FAKE_SYMLINK_STDERR", secretFile)
	c := &Container{RunsDir: t.TempDir()}
	root := testProject(t)
	run, err := c.Run(context.Background(), Spec{Root: root, Image: "alpine", Command: []string{"echo", "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	lines := collect(t, run)
	for _, l := range lines {
		if strings.Contains(l.Text, "SECRET_TOKEN_12345") {
			t.Fatalf("secreto leído a través de symlink en .stderr.log: %v", l)
		}
	}
}

func TestRunExitError(t *testing.T) {
	installFake(t, "podman")
	t.Setenv("FAKE_EXIT", "3")
	run, err := (&Container{RunsDir: t.TempDir()}).Run(context.Background(),
		Spec{Root: testProject(t), Image: "alpine", Command: []string{"false"}})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, run)
	var ee *exec.ExitError
	if err := run.Wait(); !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("Wait = %v", err)
	}
}

func TestRunUniqueNamesAndRejectsBadSpecWithoutLaunching(t *testing.T) {
	log := installFake(t, "podman")
	c := &Container{RunsDir: t.TempDir()}
	root := testProject(t)
	var names []string
	for range 3 {
		run, err := c.Run(context.Background(), Spec{Root: root, Image: "alpine", Command: []string{"true"}})
		if err != nil {
			t.Fatal(err)
		}
		collect(t, run)
		names = append(names, run.Name)
	}
	sort.Strings(names)
	if names[0] == names[1] || names[1] == names[2] {
		t.Fatalf("nombres repetidos: %v", names)
	}
	before := len(calls(t, log))
	if _, err := c.Run(context.Background(), Spec{Root: root, Image: "alpine", Command: []string{"true"}, Network: "host"}); err == nil {
		t.Fatal("red host debía rechazarse")
	}
	if _, err := c.Run(context.Background(), Spec{Root: filepath.Join(root, "no"), Image: "alpine", Command: []string{"true"}}); err == nil {
		t.Fatal("raíz inexistente debía rechazarse")
	}
	if after := len(calls(t, log)); after != before {
		t.Fatalf("un Spec inválido lanzó el runtime (%d llamadas más)", after-before)
	}
	if entries, _ := os.ReadDir(c.RunsDir); len(entries) != 3 {
		t.Fatalf("un Spec inválido dejó carpetas de salida: %d", len(entries))
	}
}

func rmCalls(t *testing.T, log string) [][]string {
	var out [][]string
	for _, c := range calls(t, log) {
		if len(c) > 0 && c[0] == "rm" {
			out = append(out, c)
		}
	}
	return out
}

func TestStopRunsRmForce(t *testing.T) {
	log := installFake(t, "podman")
	t.Setenv("FAKE_HANG", "1")
	run, err := (&Container{RunsDir: t.TempDir()}).Run(context.Background(),
		Spec{Root: testProject(t), Image: "alpine", Command: []string{"sleep", "99"}})
	if err != nil {
		t.Fatal(err)
	}
	// Espera a que el runtime falso haya registrado el run.
	deadline := time.Now().Add(5 * time.Second)
	for len(calls(t, log)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := run.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-run.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done no se cerró tras Stop")
	}
	rms := rmCalls(t, log)
	if len(rms) != 1 || strings.Join(rms[0], " ") != "rm -f -t 0 -v "+run.Name {
		t.Fatalf("rm = %v, quería rm -f -t 0 -v %s", rms, run.Name)
	}
	if err := run.Stop(); err != nil || len(rmCalls(t, log)) != 1 {
		t.Fatalf("Stop debía ser idempotente: %v", err)
	}
	if err := run.Wait(); err == nil {
		t.Error("un proceso matado debía devolver error")
	}
}

func TestContextCancelRunsRm(t *testing.T) {
	log := installFake(t, "podman")
	t.Setenv("FAKE_HANG", "1")
	ctx, cancel := context.WithCancel(context.Background())
	run, err := (&Container{RunsDir: t.TempDir()}).Run(ctx,
		Spec{Root: testProject(t), Image: "alpine", Command: []string{"sleep", "99"}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	collect(t, run)
	deadline := time.Now().Add(5 * time.Second)
	for len(rmCalls(t, log)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if rms := rmCalls(t, log); len(rms) != 1 || rms[0][len(rms[0])-1] != run.Name {
		t.Fatalf("rm = %v", rms)
	}
}

func TestContextCancelAlwaysRunsRmEvenIfProcessDone(t *testing.T) {
	log := installFake(t, "podman")
	rt, err := DetectRuntime(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	const runs = 20
	for i := 0; i < runs; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		c := &Container{Runtime: &rt, RunsDir: t.TempDir()}
		run, err := c.Run(ctx, Spec{Root: testProject(t), Image: "alpine", Command: []string{"true"}})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-run.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("Done no se cerró")
		}
		deadline := time.Now().Add(2 * time.Second)
		var found bool
		for time.Now().Before(deadline) {
			for _, rm := range rmCalls(t, log) {
				if len(rm) > 0 && rm[len(rm)-1] == run.Name {
					found = true
					break
				}
			}
			if found {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !found {
			t.Fatalf("iteración %d: rm no fue llamado para %s", i, run.Name)
		}
	}
}

func TestStopIgnoresMissingContainer(t *testing.T) {
	installFake(t, "podman")
	t.Setenv("FAKE_HANG", "1")
	t.Setenv("FAKE_RM_ERR", "Error: no container with name or ID found")
	run, err := (&Container{RunsDir: t.TempDir()}).Run(context.Background(),
		Spec{Root: testProject(t), Image: "alpine", Command: []string{"sleep", "99"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Stop(); err != nil {
		t.Fatalf("un contenedor ausente no es error: %v", err)
	}
}

func TestStopReportsRealRmFailure(t *testing.T) {
	installFake(t, "podman")
	t.Setenv("FAKE_HANG", "1")
	t.Setenv("FAKE_RM_ERR", "Error: permission denied")
	run, err := (&Container{RunsDir: t.TempDir()}).Run(context.Background(),
		Spec{Root: testProject(t), Image: "alpine", Command: []string{"sleep", "99"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Stop(); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Stop = %v", err)
	}
}

func TestStopWithoutLogReaderDoesNotHang(t *testing.T) {
	installFake(t, "podman")
	t.Setenv("FAKE_HANG", "1")
	t.Setenv("FAKE_STDOUT", strings.Repeat("linea\n", logBuffer*3))
	run, err := (&Container{RunsDir: t.TempDir()}).Run(context.Background(),
		Spec{Root: testProject(t), Image: "alpine", Command: []string{"sleep", "99"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // el buffer de Logs se llena
	done := make(chan struct{})
	go func() { run.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop se colgó con el canal de logs lleno")
	}
	select {
	case <-run.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done no se cerró")
	}
}

func TestSplitterLongLines(t *testing.T) {
	var got []string
	sp := splitter{emit: func(s string) { got = append(got, s) }}
	sp.write([]byte("a\r\nb"))
	sp.write([]byte("c\n" + strings.Repeat("x", maxLine+10)))
	sp.flush()
	if len(got) != 4 || got[0] != "a" || got[1] != "bc" || len(got[2]) != maxLine || got[3] != strings.Repeat("x", 10) {
		t.Fatalf("got = %q", got)
	}
}

// --- integración real ---------------------------------------------------

func treeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s|%v|%d\n", rel, fi.Mode(), fi.Size())
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// realRuntime devuelve un Container con un runtime real y una imagen pequeña
// que ya esté descargada; si falta algo, salta. Nunca descarga imágenes.
func realRuntime(t *testing.T) (*Container, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integración con contenedores")
	}
	rt, err := DetectRuntime(context.Background(), "")
	if err != nil {
		t.Skip("sin podman ni docker")
	}
	c := &Container{Runtime: &rt, RunsDir: t.TempDir()}
	for _, img := range []string{
		"docker.io/library/alpine:latest", "docker.io/library/busybox:latest",
		"docker.io/library/ubuntu:24.04", "alpine", "busybox", "ubuntu:24.04",
	} {
		if c.HasImage(context.Background(), img) {
			return c, img
		}
	}
	t.Skip("ninguna imagen pequeña (alpine, busybox, ubuntu) está descargada; no se descargan en los tests")
	return nil, ""
}

func TestIntegrationProjectStaysIntact(t *testing.T) {
	c, image := realRuntime(t)
	strategies := []Strategy{StrategyAuto, StrategyCopy}
	for _, st := range strategies {
		t.Run(fmt.Sprintf("strategy=%q", st), func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "a.txt"), "original")
			write(t, filepath.Join(root, "sub", "b.txt"), "b")
			before := treeHash(t, root)

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			script := `echo x >> /workspace/a.txt; rm /workspace/sub/b.txt; ` +
				`mkdir -p /workspace/target && echo 1 > /workspace/target/o; ` +
				`echo nuevo > /workspace/nuevo.txt; echo built > /out/app.jar; ` +
				`echo oops >&2; ls /workspace; cat /workspace/a.txt`
			run, err := c.Run(ctx, Spec{
				Root: root, Image: image, Strategy: st,
				Command: []string{"sh", "-c", script},
				Memory:  "256m", CPU: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr, rtlog []string
			for _, l := range collect(t, run) {
				switch l.Stream {
				case Stdout:
					stdout = append(stdout, l.Text)
				case Stderr:
					stderr = append(stderr, l.Text)
				default:
					rtlog = append(rtlog, l.Text)
				}
			}
			if err := run.Wait(); err != nil {
				t.Fatalf("Wait: %v (runtime: %v)", err, rtlog)
			}
			joined := strings.Join(stdout, "\n")
			// El contenedor sí vio sus propios cambios...
			for _, want := range []string{"nuevo.txt", "target", "original", "x"} {
				if !strings.Contains(joined, want) {
					t.Errorf("stdout no contiene %q: %q", want, joined)
				}
			}
			if strings.Join(stderr, "|") != "oops" {
				t.Errorf("stderr = %v", stderr)
			}
			// ...y el proyecto real quedó idéntico.
			if after := treeHash(t, root); after != before {
				t.Fatal("el proyecto real cambió")
			}
			if got := readFile(t, filepath.Join(run.OutDir, "app.jar")); got != "built\n" {
				t.Fatalf("app.jar = %q", got)
			}

			// Promover es explícito y es lo único que toca el proyecto.
			if err := Promote(run, "app.jar"); err != nil {
				t.Fatal(err)
			}
			if readFile(t, filepath.Join(root, "app.jar")) != "built\n" {
				t.Fatal("la promoción no copió el artefacto")
			}
			if readFile(t, filepath.Join(root, "a.txt")) != "original" {
				t.Fatal("a.txt cambió")
			}
		})
	}
}

func TestIntegrationNetworkNoneAndStop(t *testing.T) {
	c, image := realRuntime(t)
	root := testProject(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run, err := c.Run(ctx, Spec{
		Root: root, Image: image,
		Command: []string{"sh", "-c", "echo started; ls /sys/class/net; sleep 120"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	timeout := time.After(60 * time.Second)
	for len(seen) < 2 {
		select {
		case l, ok := <-run.Logs():
			if !ok {
				t.Fatalf("el contenedor terminó antes de tiempo: %v", seen)
			}
			if l.Stream == Stdout {
				seen = append(seen, l.Text)
			}
		case <-timeout:
			t.Fatalf("sin salida: %v", seen)
		}
	}
	if seen[1] != "lo" {
		t.Errorf("con red none solo debía existir lo, hay %v", seen[1:])
	}
	if err := run.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	out, err := quick(context.Background(), 20*time.Second, c.Runtime.Path,
		"ps", "-a", "--filter", "name="+run.Name, "--format", "{{.Names}}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("el contenedor sigue vivo: %q", out)
	}
}

func TestRmArgsPerRuntime(t *testing.T) {
	docker := &Run{rt: Runtime{Name: "docker"}, Name: "wari-x"}
	if got := strings.Join(docker.rmArgs(), " "); got != "rm -f -v wari-x" {
		t.Errorf("docker: %s", got)
	}
}
