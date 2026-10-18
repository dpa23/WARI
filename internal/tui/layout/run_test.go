package layout

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/sandbox"
)

// fakePodman es un "podman" de sh que hace de runtime de contenedores:
//
//	FAKE_NOIMAGE=1   la imagen no está descargada (image inspect falla)
//	FAKE_STDOUT      lo que "el contenedor" escribe en stdout
//	FAKE_STDERR      lo que escribe en stderr (va al archivo de /out)
//	FAKE_HANG        el run no termina: duerme esos segundos
//	FAKE_EXIT        código de salida
const fakePodman = `#!/bin/sh
case "$1" in
--version) echo "podman version 9.9.9"; exit 0 ;;
image) [ "$FAKE_NOIMAGE" = 1 ] && { echo "no such image" >&2; exit 1; }; echo sha256:abc; exit 0 ;;
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
	[ -n "$FAKE_STDOUT" ] && printf '%s' "$FAKE_STDOUT"
	[ -n "$FAKE_STDERR" ] && printf '%s' "$FAKE_STDERR" >> "$out/.stderr.log"
	[ -n "$FAKE_HANG" ] && exec sleep "$FAKE_HANG"
	exit "${FAKE_EXIT:-0}"
	;;
rm) ;;
esac
`

// installPodman pone el runtime falso en el PATH y devuelve el registro de
// llamadas.
func installPodman(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("el runtime falso es un guion de sh")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(fakePodman), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", log)
	return log
}

// podmanCalls devuelve cada invocación registrada: verbo y argumentos.
func podmanCalls(t *testing.T, log string) [][]string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
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

// argAfter devuelve el argumento que sigue a flag en una llamada.
func argAfter(call []string, flag string) string {
	if i := slices.Index(call, flag); i >= 0 && i+1 < len(call) {
		return call[i+1]
	}
	return ""
}

func runLayout(t *testing.T, files map[string]string) (*driver, string, string) {
	t.Helper()
	log := installPodman(t)
	root := t.TempDir()
	for rel, content := range files {
		write(t, root, rel, content)
	}
	sb := &sandbox.Container{RunsDir: t.TempDir()}
	m := newLayout(t, Config{Root: root, Sandbox: sb}, 120, 30)
	// Árbol y agentes plegados: el editor (y el dock) usan todo el ancho.
	m, _ = send(m, press('b', tea.ModCtrl), press('g', tea.ModCtrl))
	return newDriver(t, m), root, log
}

func outText(m Model) string {
	var sb strings.Builder
	for _, l := range m.out {
		sb.WriteString(l.text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// psHas dice si algún proceso del sistema tiene s en su línea de comando.
func psHas(t *testing.T, s string) bool {
	t.Helper()
	out, err := exec.Command("ps", "-eo", "args=").Output()
	if err != nil {
		t.Skipf("sin ps: %v", err)
	}
	return strings.Contains(string(out), s)
}

func TestRunProjectStreamsLogsAndStops(t *testing.T) {
	t.Setenv("FAKE_STDOUT", "arrancando la app\nlisto en :8080\n")
	t.Setenv("FAKE_STDERR", "aviso: algo\n")
	t.Setenv("FAKE_HANG", "61.4711")
	d, _, log := runLayout(t, map[string]string{"pom.xml": "<project/>"})

	d.send(press('g', tea.ModAlt))
	d.until("pregunta de red", func(m Model) bool { return m.run.confirm != nil })
	if s := screen(d.m); !strings.Contains(s, "¿Permitir red?") || !strings.Contains(s, "necesita red") {
		t.Fatalf("falta la pregunta de la red:\n%s", s)
	}
	if calls := podmanCalls(t, log); len(calls) != 0 {
		t.Fatalf("no debe lanzar nada antes de confirmar: %v", calls)
	}

	d.send(text("n")) // sin red: el valor por omisión
	d.until("logs en streaming", func(m Model) bool {
		return strings.Contains(outText(m), "listo en :8080") && strings.Contains(outText(m), "aviso: algo")
	})
	if !d.m.run.active {
		t.Fatal("la ejecución debería seguir en curso")
	}
	calls := podmanCalls(t, log)
	if len(calls) != 1 || calls[0][0] != "run" {
		t.Fatalf("llamadas = %v", calls)
	}
	if got := argAfter(calls[0], "--network"); got != "none" {
		t.Fatalf("red = %q, quería none", got)
	}
	if !slices.Contains(calls[0], "maven:3-eclipse-temurin-21") || !slices.Contains(calls[0], "spring-boot:run") {
		t.Fatalf("faltan la imagen o el comando del perfil: %v", calls[0])
	}

	// La tecla de parada se ve siempre: en el título del panel y en la barra.
	s := screen(d.m)
	if strings.Count(s, "alt+x detener") < 2 {
		t.Fatalf("la tecla de parada debe verse en el panel y en la barra:\n%s", s)
	}
	checkDims(t, d.m, 120, 30, "dock con salida")
	if !psHas(t, "sleep 61.4711") {
		t.Fatal("el contenedor falso debería estar corriendo")
	}

	d.send(press('x', tea.ModAlt))
	d.until("parada", func(m Model) bool { return !m.run.active })
	d.until("registro de la parada", func(m Model) bool { return strings.Contains(outText(m), "se detuvo") })
	// Stop mata el grupo y después ejecuta rm -f; el fin de los logs no espera a ese rm.
	hasRm := func() bool {
		for _, c := range podmanCalls(t, log) {
			if c[0] == "rm" {
				return true
			}
		}
		return false
	}
	for i := 0; i < 500 && !hasRm(); i++ {
		d.drain(10 * time.Millisecond)
	}
	if !hasRm() {
		t.Fatalf("Stop debe ejecutar rm -f: %v", podmanCalls(t, log))
	}
	if psHas(t, "sleep 61.4711") {
		t.Fatal("quedó un proceso huérfano del contenedor")
	}
	if strings.Contains(screen(d.m), "alt+x detener") {
		t.Fatal("sin ejecución no debe ofrecerse detener")
	}
}

func TestRunProjectNetworkConfirmAllowsBridge(t *testing.T) {
	t.Setenv("FAKE_STDOUT", "ok\n")
	d, _, log := runLayout(t, map[string]string{"build.gradle": ""})
	d.send(press('g', tea.ModAlt))
	d.until("pregunta de red", func(m Model) bool { return m.run.confirm != nil })
	d.send(text("y"))
	d.until("fin", func(m Model) bool {
		return !m.run.active && strings.Contains(outText(m), "terminó (código 0)")
	})
	calls := podmanCalls(t, log)
	if len(calls) != 1 || argAfter(calls[0], "--network") != "bridge" {
		t.Fatalf("llamadas = %v", calls)
	}
	if !slices.Contains(calls[0], "gradle:jdk21") {
		t.Fatalf("perfil gradle: %v", calls[0])
	}
	if !strings.Contains(outText(d.m), "ok") {
		t.Fatalf("salida = %q", outText(d.m))
	}
}

func TestRunProjectConfirmCancel(t *testing.T) {
	d, _, log := runLayout(t, map[string]string{"App.csproj": "<Project/>"})
	d.send(press('g', tea.ModAlt))
	d.until("pregunta", func(m Model) bool { return m.run.confirm != nil })
	d.send(press(tea.KeyEsc, 0))
	if d.m.run.confirm != nil || d.m.run.active || !strings.Contains(d.m.msg, "cancelada") {
		t.Fatalf("esc debe cancelar: confirm=%v active=%v msg=%q", d.m.run.confirm, d.m.run.active, d.m.msg)
	}
	d.drain(100 * time.Millisecond)
	if calls := podmanCalls(t, log); len(calls) != 0 {
		t.Fatalf("cancelar no debe lanzar nada: %v", calls)
	}
}

func TestRunProjectExitCode(t *testing.T) {
	t.Setenv("FAKE_EXIT", "3")
	d, _, _ := runLayout(t, map[string]string{"pom.xml": "<project/>"})
	d.send(press('g', tea.ModAlt))
	d.until("pregunta", func(m Model) bool { return m.run.confirm != nil })
	d.send(text("n"))
	d.until("fin", func(m Model) bool { return strings.Contains(outText(m), "código 3") })
	if d.m.run.active {
		t.Fatal("la ejecución terminó")
	}
}

func TestRunProjectMissingImageNeverRuns(t *testing.T) {
	t.Setenv("FAKE_NOIMAGE", "1")
	d, _, log := runLayout(t, map[string]string{"pom.xml": "<project/>"})
	d.send(press('g', tea.ModAlt))
	d.until("aviso de imagen", func(m Model) bool { return strings.Contains(outText(m), "no está descargada") })
	out := outText(d.m)
	if !strings.Contains(out, "podman pull maven:3-eclipse-temurin-21") || !strings.Contains(out, "nunca descarga") {
		t.Fatalf("falta cómo descargarla: %q", out)
	}
	if d.m.run.active || d.m.run.confirm != nil {
		t.Fatal("sin imagen no debe preguntar ni lanzar")
	}
	if calls := podmanCalls(t, log); len(calls) != 0 {
		t.Fatalf("no debe descargar ni lanzar: %v", calls)
	}
	if !strings.Contains(d.m.msg, "podman pull") {
		t.Fatalf("la barra debe decirlo: %q", d.m.msg)
	}
}

func TestRunProjectNoProfileOrNoSandbox(t *testing.T) {
	d, _, _ := runLayout(t, map[string]string{"README.md": "x"})
	d.send(press('g', tea.ModAlt))
	if !strings.Contains(d.m.msg, "no se detectó") || d.m.run.checking {
		t.Fatalf("msg = %q", d.m.msg)
	}
	m := newLayout(t, Config{}, 100, 30)
	m, _ = send(m, press('g', tea.ModAlt))
	if !strings.Contains(m.msg, "no está disponible") {
		t.Fatalf("sin sandbox: %q", m.msg)
	}
}

func TestRunOutputIsSanitized(t *testing.T) {
	t.Setenv("FAKE_STDOUT", "\x1b[31mrojo\x1b[0m\x1b]0;titulo\x07 \x1b[2Jfin\r\n")
	d, _, _ := runLayout(t, map[string]string{"pom.xml": "<project/>"})
	d.send(press('g', tea.ModAlt))
	d.until("pregunta", func(m Model) bool { return m.run.confirm != nil })
	d.send(text("n"))
	d.until("fin", func(m Model) bool { return !m.run.active && len(m.out) > 2 })
	if !strings.Contains(outText(d.m), "rojo fin") {
		t.Fatalf("salida = %q", outText(d.m))
	}
	for _, l := range d.m.out {
		for _, r := range l.text {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Fatalf("la salida conserva el carácter de control %q en %q", r, l.text)
			}
		}
	}
	if sanitizeLine("a\tb\x00c\u0085d") != "a    bcd" {
		t.Fatalf("sanitizeLine = %q", sanitizeLine("a\tb\x00c\u0085d"))
	}
}

func TestPromoteArtifactExplicitAndConfirmsOverwrite(t *testing.T) {
	t.Setenv("FAKE_STDOUT", "listo\n")
	d, root, _ := runLayout(t, map[string]string{"pom.xml": "<project/>", "target/app.jar": "viejo"})

	// Sin ejecución previa no hay de dónde promover.
	d.send(press('w', tea.ModAlt))
	if d.m.pal.open || !strings.Contains(d.m.msg, "no hay ninguna ejecución") {
		t.Fatalf("msg = %q", d.m.msg)
	}

	d.send(press('g', tea.ModAlt))
	d.until("pregunta", func(m Model) bool { return m.run.confirm != nil })
	d.send(text("n"))
	d.until("fin", func(m Model) bool {
		return !m.run.active && m.run.run != nil && strings.Contains(outText(m), "terminó")
	})

	// El "proyecto" deja un artefacto en /out (aquí lo escribe el test).
	run := d.m.run.run
	if err := os.WriteFile(filepath.Join(run.OutDir, "app.jar"), []byte("raiz"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(run.OutDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.OutDir, "docs", "a.txt"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}

	d.send(press('w', tea.ModAlt))
	if !d.m.pal.open || d.m.pal.mode != palPromote {
		t.Fatal("promote_artifact debe abrir la entrada de la ruta")
	}
	if note := d.m.pal.note; !strings.Contains(note, "app.jar") || !strings.Contains(note, "docs/") {
		t.Fatalf("la nota debe listar las salidas: %q", note)
	}
	// El proyecto real no se toca hasta aceptar.
	if b, _ := os.ReadFile(filepath.Join(root, "target", "app.jar")); string(b) != "viejo" {
		t.Fatalf("el proyecto cambió antes de promover: %q", b)
	}

	// Una ruta que no existe en /out falla sin tocar el proyecto.
	for _, r := range "nada.bin" {
		d.send(text(string(r)))
	}
	d.send(press(tea.KeyEnter, 0))
	d.until("error de promoción", func(m Model) bool { return strings.Contains(m.msg, "no se pudo promover") })
	if _, err := os.Stat(filepath.Join(root, "nada.bin")); err == nil {
		t.Fatal("una promoción fallida no debe crear nada en el proyecto")
	}

	// El artefacto existe en /out y, con otra versión, en el proyecto.
	if err := os.MkdirAll(filepath.Join(run.OutDir, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.OutDir, "target", "app.jar"), []byte("nuevo"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.send(press('w', tea.ModAlt))
	for _, r := range "target/app.jar" {
		d.send(text(string(r)))
	}
	d.send(press(tea.KeyEnter, 0))
	if !d.m.pal.open || !strings.Contains(d.m.pal.note, "ya existe") {
		t.Fatalf("debe pedir confirmar la sobrescritura: open=%v note=%q", d.m.pal.open, d.m.pal.note)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "target", "app.jar")); string(b) != "viejo" {
		t.Fatal("no se debe sobrescribir sin confirmar")
	}
	d.send(press(tea.KeyEnter, 0))
	d.until("promovido", func(m Model) bool { return strings.Contains(m.msg, "promovido") })
	if b, _ := os.ReadFile(filepath.Join(root, "target", "app.jar")); string(b) != "nuevo" {
		t.Fatalf("el artefacto no se promovió: %q", b)
	}

	// Un directorio nuevo se promueve sin preguntar, y no se acepta salir de /out.
	d.send(press('w', tea.ModAlt))
	for _, r := range "docs" {
		d.send(text(string(r)))
	}
	d.send(press(tea.KeyEnter, 0))
	d.until("directorio promovido", func(m Model) bool { return strings.Contains(m.msg, "promovido docs") })
	if b, _ := os.ReadFile(filepath.Join(root, "docs", "a.txt")); string(b) != "doc" {
		t.Fatalf("docs/a.txt = %q", b)
	}
	d.send(press('w', tea.ModAlt))
	for _, r := range "../x" {
		d.send(text(string(r)))
	}
	d.send(press(tea.KeyEnter, 0))
	if !d.m.pal.open || !strings.Contains(d.m.pal.note, "relativa") {
		t.Fatalf("debe rechazar rutas fuera de /out: %q", d.m.pal.note)
	}
}
