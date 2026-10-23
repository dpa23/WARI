package layout

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/dap"
)

const (
	programCS = "class P {\n  void Main() {\n    int x = 1;\n    Run();\n  }\n  void Run() {}\n}\n"
	helperCS  = "class H {\n  void Run() {}\n}\n"

	launchOK = `{"configurations": [{"name": "API", "adapter": "netcoredbg", "request": "launch", "args": {"program": "/opt/app.dll", "stopAtEntry": false}}]}`
)

// dbgProject crea un proyecto con Program.cs y Helper.cs y, si launch no está
// vacío, .wari/launch.json.
func dbgProject(t *testing.T, launch string) (root, main, helper string) {
	t.Helper()
	root = t.TempDir()
	main = write(t, root, "src/Program.cs", programCS)
	helper = write(t, root, "src/Helper.cs", helperCS)
	if launch != "" {
		write(t, root, ".wari/launch.json", launch)
	}
	return root, main, helper
}

// dbgLayout abre Program.cs en un layout de 120x30 con árbol y agentes
// plegados y el adaptador falso inyectado.
func dbgLayout(t *testing.T, root, file string, cfg DebugConfig) *driver {
	t.Helper()
	m := newLayout(t, Config{Root: root, File: file, Debug: cfg}, 120, 30)
	m, _ = send(m, press('b', tea.ModCtrl), press('g', tea.ModCtrl))
	return newDriver(t, m)
}

// gutter devuelve el glifo del margen de la línea (base 1) en la pantalla, o
// "?" si la línea no se ve.
func gutter(m Model, line int) string {
	g := m.geometry()
	rows := lines(m)
	for i := g.ed.Y; i < g.ed.Y+g.bodyH-g.dockH && i < len(rows); i++ {
		s := strings.TrimLeft(plain(rows[i]), " ")
		n := 0
		for n < len(s) && unicode.IsDigit(rune(s[n])) {
			n++
		}
		if n == 0 || n >= len(s) {
			continue
		}
		var v int
		for _, c := range s[:n] {
			v = v*10 + int(c-'0')
		}
		if v == line {
			r, _ := utf8.DecodeRuneInString(s[n:])
			return string(r)
		}
	}
	return "?"
}

// execLineHighlighted dice si la fila de la línea (base 1) lleva el fondo de
// la línea actual del depurador.
func execLineHighlighted(m Model, line int) bool {
	g := m.geometry()
	rows := lines(m)
	for i := g.ed.Y; i < g.ed.Y+g.bodyH-g.dockH && i < len(rows); i++ {
		if strings.HasPrefix(strings.TrimLeft(plain(rows[i]), " "), strconv.Itoa(line)) &&
			strings.Contains(rows[i], "\x1b[48;2;70;62;20m") {
			return true
		}
	}
	return false
}

func rowNames(m Model) []string {
	var out []string
	for _, r := range m.dbg.rows {
		out = append(out, strings.Repeat(">", r.depth)+r.name)
	}
	return out
}

func stoppedWithTree(m Model) bool {
	return len(m.dbg.frames) == 2 && len(m.dbg.rows) > 0 && !m.dbg.busy
}

func TestDebugFullFlow(t *testing.T) {
	adapter := installAdapter(t)
	root, main, helper := dbgProject(t, launchOK)
	f := newFakeDAP(main, helper)
	l := &fakeLauncher{f: f}
	d := dbgLayout(t, root, main, DebugConfig{Launcher: l.launch})

	// Breakpoint en la línea 3, antes de iniciar: se ve en el margen.
	d.send(press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyF9, 0))
	if got := gutter(d.m, 3); got != "○" {
		t.Fatalf("breakpoint sin verificar: margen %q\n%s", got, screen(d.m))
	}
	if f.count("initialize") != 0 {
		t.Fatal("poner un breakpoint no debe lanzar el adaptador")
	}

	// Iniciar: llega el stopped y se ve la línea, la pila y las variables.
	d.send(press(tea.KeyF5, 0))
	d.until("parada en el breakpoint", stoppedWithTree)

	cmds := f.commands()
	want := []string{"initialize", "launch", "setBreakpoints", "configurationDone"}
	if !slices.Equal(cmds[:4], want) {
		t.Fatalf("secuencia = %v, quería %v", cmds, want)
	}
	l.mu.Lock()
	if l.launches != 1 || l.path != adapter || !slices.Equal(l.args, []string{"--interpreter=vscode"}) || l.dir != root {
		t.Fatalf("lanzamiento: %d %q %v %q", l.launches, l.path, l.args, l.dir)
	}
	l.mu.Unlock()
	f.mu.Lock()
	var got, wantArgs any
	json.Unmarshal(f.launchArgs, &got)
	json.Unmarshal([]byte(`{"program": "/opt/app.dll", "stopAtEntry": false}`), &wantArgs)
	f.mu.Unlock()
	if !jsonEqual(got, wantArgs) {
		t.Fatalf("args de launch = %s: deben ser los de launch.json, tal cual", f.launchArgs)
	}
	if bps := f.breakpoints(main); !slices.Equal(bps, []int{3}) {
		t.Fatalf("breakpoints enviados = %v", bps)
	}

	if d.m.ed.ExecLine() != 2 || d.m.ed.Path() != main {
		t.Fatalf("línea resaltada = %d en %s", d.m.ed.ExecLine(), d.m.ed.Path())
	}
	if g := gutter(d.m, 3); g != "▶" {
		t.Fatalf("margen de la línea actual = %q\n%s", g, screen(d.m))
	}
	if !execLineHighlighted(d.m, 3) {
		t.Fatal("la línea actual debe resaltarse")
	}
	s := screen(d.m)
	for _, w := range []string{"Pila · detenido (breakpoint)", "Main  Program.cs:3", "Run  Helper.cs:2", "Variables",
		"▾ Locals", "x = 42  (int)", "▸ obj = {Foo}", "▸ Statics", "DBG detenido · shift+f5 detener"} {
		if !strings.Contains(s, w) {
			t.Fatalf("falta %q en la pantalla:\n%s", w, s)
		}
	}
	checkDims(t, d.m, 120, 30, "depuración detenida")

	// Variables navegables por teclado: abajo, derecha expande (asíncrono).
	d.send(press('v', tea.ModAlt))
	if d.m.focus != focusDock || d.m.dock.sub != dockVars {
		t.Fatalf("alt+v debe llevar el foco al dock: %v %v", d.m.focus, d.m.dock.sub)
	}
	d.send(press(tea.KeyDown, 0), press(tea.KeyDown, 0)) // x, obj
	if d.m.dbg.varSel != 2 {
		t.Fatalf("selección = %d", d.m.dbg.varSel)
	}
	d.send(press(tea.KeyRight, 0))
	d.until("expandir obj", func(m Model) bool { return !m.dbg.busy && len(m.dbg.rows) == 6 })
	if got := rowNames(d.m); !slices.Equal(got, []string{"Locals", ">x", ">obj", ">>name", ">>inner", "Statics"}) {
		t.Fatalf("filas = %v", got)
	}
	d.send(press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnter, 0)) // inner, enter
	d.until("expandir inner", func(m Model) bool { return !m.dbg.busy && len(m.dbg.rows) == 7 })
	if got := rowNames(d.m); got[5] != ">>>deep" {
		t.Fatalf("filas = %v", got)
	}
	if !strings.Contains(screen(d.m), "▾ inner = {Bar}") || !strings.Contains(screen(d.m), "deep = true  (bool)") {
		t.Fatalf("el árbol anidado no se ve:\n%s", screen(d.m))
	}
	d.send(press(tea.KeyLeft, 0)) // pliega inner
	if len(d.m.dbg.rows) != 6 || d.m.dbg.varSel != 4 {
		t.Fatalf("tras plegar inner: %v sel %d", rowNames(d.m), d.m.dbg.varSel)
	}
	d.send(press(tea.KeyLeft, 0)) // hoja plegada: sube al padre
	if d.m.dbg.varSel != 2 {
		t.Fatalf("izquierda sobre inner plegado debe subir a obj: sel %d", d.m.dbg.varSel)
	}
	d.send(press(tea.KeyLeft, 0)) // pliega obj
	if got := rowNames(d.m); !slices.Equal(got, []string{"Locals", ">x", ">obj", "Statics"}) {
		t.Fatalf("filas = %v", got)
	}
	d.send(press(tea.KeyRight, 0)) // ya cargado: se expande en el acto
	if d.m.dbg.busy || len(d.m.dbg.rows) != 6 {
		t.Fatalf("reexpandir no debe volver a pedir: busy=%v filas=%v", d.m.dbg.busy, rowNames(d.m))
	}
	d.send(press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyRight, 0)) // Statics
	d.until("expandir Statics", func(m Model) bool { return !m.dbg.busy && len(m.dbg.rows) == 7 })
	if got := rowNames(d.m); got[6] != ">Count" {
		t.Fatalf("filas = %v", got)
	}

	// Pila: elegir otro marco salta a su archivo y recarga las variables.
	d.send(text("p"), press(tea.KeyDown, 0), press(tea.KeyEnter, 0))
	d.until("marco 2", func(m Model) bool {
		return m.dbg.frame == 1 && !m.dbg.busy && len(m.dbg.rows) == 2 && m.dbg.rows[1].name == "y"
	})
	if d.m.ed.Path() != helper || d.m.ed.ExecLine() != 1 {
		t.Fatalf("marco 2: %s línea %d", d.m.ed.Path(), d.m.ed.ExecLine())
	}
	if d.m.focus != focusDock {
		t.Fatal("elegir un marco no debe quitar el foco del dock")
	}
	d.send(press(tea.KeyUp, 0), press(tea.KeyEnter, 0))
	d.until("marco 1", func(m Model) bool { return m.dbg.frame == 0 && len(m.dbg.rows) == 4 })
	if d.m.ed.Path() != main || d.m.ed.ExecLine() != 2 {
		t.Fatalf("marco 1: %s línea %d", d.m.ed.Path(), d.m.ed.ExecLine())
	}

	// next: al reanudar se descarta todo; la nueva parada trae la línea 4.
	d.send(press(tea.KeyF10, 0))
	if len(d.m.dbg.frames) != 0 || d.m.dbg.tree != nil || len(d.m.dbg.rows) != 0 || d.m.ed.ExecLine() != -1 {
		t.Fatalf("al reanudar debe descartarse la pila y el árbol: frames=%d tree=%v rows=%d exec=%d",
			len(d.m.dbg.frames), d.m.dbg.tree, len(d.m.dbg.rows), d.m.ed.ExecLine())
	}
	if g := gutter(d.m, 3); g == "▶" || execLineHighlighted(d.m, 3) {
		t.Fatalf("la línea actual no debe seguir resaltada al reanudar: %q", g)
	}
	d.until("parada del next", func(m Model) bool {
		return stoppedWithTree(m) && m.dbg.frames[0].Line == 4
	})
	if d.m.ed.ExecLine() != 3 || gutter(d.m, 4) != "▶" || gutter(d.m, 3) != "◉" {
		t.Fatalf("tras next: exec=%d margen3=%q margen4=%q", d.m.ed.ExecLine(), gutter(d.m, 3), gutter(d.m, 4))
	}
	if f.count("next") != 1 || !strings.Contains(d.m.msg, "detenido en src/Program.cs:4 (step)") {
		t.Fatalf("next=%d msg=%q", f.count("next"), d.m.msg)
	}

	// continue: el depurado termina y la sesión se cierra sola.
	d.send(press(tea.KeyF8, 0))
	d.until("fin de la sesión", func(m Model) bool { return m.dbg.sess == nil && !m.dbg.starting })
	out := outText(d.m)
	if !strings.Contains(out, "hola desde el depurado") || !strings.Contains(out, "Depuración terminada.") {
		t.Fatalf("salida = %q", out)
	}
	if d.m.ed.ExecLine() != -1 || gutter(d.m, 4) == "▶" {
		t.Fatal("tras terminar no debe quedar línea resaltada")
	}
	waitExited(t, l)
	if f.count("disconnect") != 1 {
		t.Fatalf("comandos = %v", f.commands())
	}
	if gutter(d.m, 3) != "○" {
		t.Fatalf("sin sesión el breakpoint vuelve a pendiente: %q", gutter(d.m, 3))
	}

	// Nada en marcha: esc cierra el dock.
	d.send(press('v', tea.ModAlt), press(tea.KeyEsc, 0))
	if d.m.dock.open || d.m.focus == focusDock {
		t.Fatalf("esc debe cerrar el dock vacío: open=%v focus=%v", d.m.dock.open, d.m.focus)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func waitExited(t *testing.T, l *fakeLauncher) {
	t.Helper()
	p := l.process()
	if p == nil {
		t.Fatal("el adaptador no se lanzó")
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("el proceso del adaptador sigue vivo")
	}
}

func TestDebugStopKeyEndsSession(t *testing.T) {
	installAdapter(t)
	root, main, helper := dbgProject(t, launchOK)
	f := newFakeDAP(main, helper)
	l := &fakeLauncher{f: f}
	d := dbgLayout(t, root, main, DebugConfig{Launcher: l.launch})
	d.send(press('b', tea.ModAlt), press(tea.KeyF5, 0)) // breakpoint en la línea 1 y a depurar
	d.until("parada", stoppedWithTree)
	if d.m.ed.ExecLine() != 0 {
		t.Fatalf("línea resaltada = %d", d.m.ed.ExecLine())
	}
	d.send(press(tea.KeyF5, 0)) // F5 con la sesión detenida continúa
	d.until("fin", func(m Model) bool { return m.dbg.sess == nil })
	if f.count("continue") != 1 {
		t.Fatalf("F5 detenido debe continuar: %v", f.commands())
	}

	// Segunda sesión, cerrada con shift+F5.
	d.send(press(tea.KeyF5, 0))
	d.until("parada 2", stoppedWithTree)
	d.send(press(tea.KeyF5, tea.ModShift))
	d.until("detenida", func(m Model) bool { return m.dbg.sess == nil && !m.dbg.ending })
	waitExited(t, l)
	if f.count("disconnect") != 2 {
		t.Fatalf("comandos = %v", f.commands())
	}
	for i := 0; i < 500 && l.process().killCount() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if l.process().killCount() == 0 {
		t.Fatal("al cerrar la sesión debe terminarse el grupo del adaptador")
	}
	d.m.hub.mu.Lock()
	left := d.m.hub.sess
	d.m.hub.mu.Unlock()
	if left != nil {
		t.Fatal("el hub no debe conservar la sesión cerrada")
	}
	if d.m.debugInfo() != "" {
		t.Fatalf("la barra no debe seguir mostrando la sesión: %q", d.m.debugInfo())
	}
}

func TestDebugBreakpointsDuringSession(t *testing.T) {
	installAdapter(t)
	root, main, helper := dbgProject(t, launchOK)
	f := newFakeDAP(main, helper)
	l := &fakeLauncher{f: f}
	d := dbgLayout(t, root, main, DebugConfig{Launcher: l.launch})
	d.send(press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyF9, 0), press(tea.KeyF5, 0))
	d.until("parada", stoppedWithTree)
	if g := gutter(d.m, 3); g != "▶" {
		t.Fatalf("margen = %q", g)
	}

	// Poner otro breakpoint con la sesión viva: se envía al adaptador.
	d.send(press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyF9, 0)) // línea 5
	d.until("breakpoint enviado", func(m Model) bool { return slices.Equal(f.breakpoints(main), []int{3, 5}) && gutter(m, 5) == "◉" })
	// Quitarlo.
	d.send(press(tea.KeyF9, 0))
	d.until("breakpoint quitado", func(m Model) bool { return slices.Equal(f.breakpoints(main), []int{3}) && gutter(m, 5) == " " })
}

func TestDebugUnsavedEditorIsNeverDiscarded(t *testing.T) {
	installAdapter(t)
	root, main, helper := dbgProject(t, launchOK)
	f := newFakeDAP(main, helper)
	l := &fakeLauncher{f: f}
	d := dbgLayout(t, root, main, DebugConfig{Launcher: l.launch})
	d.send(press(tea.KeyF9, 0), press(tea.KeyF5, 0))
	d.until("parada", stoppedWithTree)
	d.send(text("z")) // edita Program.cs: queda sin guardar
	if !d.m.ed.Dirty() {
		t.Fatal("el editor debería estar sucio")
	}
	d.send(press('v', tea.ModAlt), text("p"), press(tea.KeyDown, 0), press(tea.KeyEnter, 0))
	if d.m.ed.Path() != main || !d.m.ed.Dirty() {
		t.Fatalf("no debe abrir otro archivo con cambios sin guardar: %s dirty=%v", d.m.ed.Path(), d.m.ed.Dirty())
	}
	if !strings.Contains(d.m.msg, "guarda") {
		t.Fatalf("msg = %q", d.m.msg)
	}
	// Una segunda parada tampoco lo descarta.
	d.until("variables del marco 2", func(m Model) bool { return !m.dbg.busy && len(m.dbg.rows) == 2 })
	d.send(press(tea.KeyF10, 0))
	d.until("parada", stoppedWithTree)
	if d.m.ed.Path() != main || !d.m.ed.Dirty() {
		t.Fatal("la parada no debe descartar cambios sin guardar")
	}
}

func TestDebugAttachRequest(t *testing.T) {
	installAdapter(t)
	root, main, helper := dbgProject(t, `[{"name": "pegarse", "adapter": "netcoredbg", "request": "attach", "args": {"processId": 1234}}]`)
	f := newFakeDAP(main, helper)
	l := &fakeLauncher{f: f}
	d := dbgLayout(t, root, main, DebugConfig{Launcher: l.launch})
	d.send(press(tea.KeyF5, 0))
	d.until("parada", stoppedWithTree)
	if f.count("attach") != 1 || f.count("launch") != 0 {
		t.Fatalf("comandos = %v", f.commands())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if string(f.launchArgs) != `{"processId":1234}` && string(f.launchArgs) != `{"processId": 1234}` {
		t.Fatalf("args = %s", f.launchArgs)
	}
}

// --- Avisos ----------------------------------------------------------------------------

func noticeText(m Model) string { return strings.Join(m.dbg.notice, "\n") }

func TestDebugAdapterNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root, main, _ := dbgProject(t, launchOK)
	d := dbgLayout(t, root, main, DebugConfig{})
	d.send(press(tea.KeyF5, 0))
	if !strings.Contains(d.m.msg, "netcoredbg no está instalado") || !d.m.dock.open {
		t.Fatalf("msg = %q open=%v", d.m.msg, d.m.dock.open)
	}
	if s := screen(d.m); !strings.Contains(s, "netcoredbg no está instalado") {
		t.Fatalf("el aviso debe verse en el panel:\n%s", s)
	}
	if d.m.dbg.starting || d.m.dbg.sess != nil {
		t.Fatal("no debe iniciarse nada")
	}
}

func TestDebugJavaNeedsJdtls(t *testing.T) {
	root := t.TempDir()
	app := write(t, root, "App.java", "class App {}\n")
	write(t, root, ".wari/launch.json", `[{"adapter": "java-debug", "args": {}}]`)
	d := dbgLayout(t, root, app, DebugConfig{})
	d.send(press(tea.KeyF5, 0))
	if !strings.Contains(d.m.msg, "java-debug requiere jdtls con el plugin") {
		t.Fatalf("msg = %q", d.m.msg)
	}
	// Con un PortProvider (la integración con jdtls) se conecta por TCP; aquí
	// solo se comprueba que el aviso desaparece y se llama al proveedor.
	called := make(chan struct{}, 1)
	d = dbgLayout(t, root, app, DebugConfig{Ports: map[string]dap.PortProvider{
		"java-debug": func(ctx context.Context) (string, error) {
			called <- struct{}{}
			return "", errors.New("sin puerto de prueba")
		},
	}})
	d.send(press(tea.KeyF5, 0))
	d.until("sin puerto", func(m Model) bool { return !m.dbg.starting })
	select {
	case <-called:
	default:
		t.Fatal("debe pedir el puerto al PortProvider")
	}
	if !strings.Contains(noticeText(d.m), "puerto") {
		t.Fatalf("aviso = %q", noticeText(d.m))
	}
}

func TestDebugLaunchFileProblems(t *testing.T) {
	installAdapter(t)
	cases := []struct {
		name, launch, want string
	}{
		{"sin archivo", "", "Falta .wari/launch.json"},
		{"json inválido", "{", "no sirve"},
		{"sin configuraciones", `{"configurations": []}`, "ninguna configuración"},
		{"request raro", `[{"adapter": "netcoredbg", "request": "fly"}]`, "launch o attach"},
		{"args no objeto", `[{"adapter": "netcoredbg", "args": [1]}]`, "objeto JSON"},
		{"otro adaptador", `[{"adapter": "java-debug"}]`, `"adapter": "netcoredbg"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, main, _ := dbgProject(t, c.launch)
			d := dbgLayout(t, root, main, DebugConfig{})
			d.send(press(tea.KeyF5, 0))
			text := noticeText(d.m)
			if !strings.Contains(text, c.want) {
				t.Fatalf("aviso = %q, quería %q", text, c.want)
			}
			if c.name == "sin archivo" {
				// Explica cómo crearlo, sin inventar argumentos.
				for _, w := range []string{"configurations", "adapter", "request", "args", "no los inventa"} {
					if !strings.Contains(text, w) {
						t.Fatalf("la ayuda debe mencionar %q: %q", w, text)
					}
				}
				if s := screen(d.m); !strings.Contains(s, "Falta .wari/launch.json") {
					t.Fatalf("la ayuda debe verse en el panel:\n%s", s)
				}
			}
			if d.m.dbg.starting || d.m.dbg.sess != nil {
				t.Fatal("no debe iniciarse")
			}
		})
	}
}

func TestDebugUnsupportedFileAndBreakpoint(t *testing.T) {
	root := t.TempDir()
	txt := write(t, root, "notes.txt", "hola\n")
	d := dbgLayout(t, root, txt, DebugConfig{})
	d.send(press(tea.KeyF9, 0))
	if !strings.Contains(d.m.msg, "no se pueden poner breakpoints") || len(d.m.dbg.bps) != 0 {
		t.Fatalf("msg = %q", d.m.msg)
	}
	d.send(press(tea.KeyF5, 0))
	if !strings.Contains(noticeText(d.m), "No hay adaptador de depuración para .txt") {
		t.Fatalf("aviso = %q", noticeText(d.m))
	}
	// Sin archivo abierto no hay breakpoint que poner.
	d = dbgLayout(t, root, "", DebugConfig{})
	d.send(press(tea.KeyF9, 0))
	if !strings.Contains(d.m.msg, "no hay archivo abierto") {
		t.Fatalf("msg = %q", d.m.msg)
	}
	// Sin sesión, los controles avisan en vez de fallar.
	d.send(press(tea.KeyF10, 0))
	if !strings.Contains(d.m.msg, "no hay sesión") {
		t.Fatalf("msg = %q", d.m.msg)
	}
	d.send(press(tea.KeyF5, tea.ModShift))
	if !strings.Contains(d.m.msg, "no hay ninguna sesión") {
		t.Fatalf("msg = %q", d.m.msg)
	}
}

func TestDebugShutdownOnQuit(t *testing.T) {
	installAdapter(t)
	root, main, helper := dbgProject(t, launchOK)
	f := newFakeDAP(main, helper)
	l := &fakeLauncher{f: f}
	d := dbgLayout(t, root, main, DebugConfig{Launcher: l.launch})
	d.send(press(tea.KeyF9, 0), press(tea.KeyF5, 0))
	d.until("parada", stoppedWithTree)
	d.send(press('c', tea.ModCtrl)) // salir: cierra la sesión sin dejar el adaptador
	waitExited(t, l)
	// Shutdown es idempotente y sirve desde cualquier copia del modelo.
	d.m.Shutdown()
	d.m.Shutdown()
}

func TestLoadLaunch(t *testing.T) {
	dir := t.TempDir()
	load := func(content string) ([]launchConfig, error) {
		write(t, dir, ".wari/launch.json", content)
		return loadLaunch(dir)
	}
	cfgs, err := load(`{"configurations": [{"name": "a", "adapter": "netcoredbg"}, {"adapter": "java-debug", "request": "attach", "args": {"x": 1}}]}`)
	if err != nil || len(cfgs) != 2 {
		t.Fatalf("cfgs = %+v, %v", cfgs, err)
	}
	if cfgs[0].Request != "launch" || string(cfgs[0].Args) != "{}" {
		t.Fatalf("valores por omisión: %+v", cfgs[0])
	}
	if cfgs[1].Request != "attach" || string(cfgs[1].Args) != `{"x": 1}` {
		t.Fatalf("args opacos: %+v", cfgs[1])
	}
	if cfgs, err = load(`[{"adapter": "netcoredbg", "args": null}]`); err != nil || string(cfgs[0].Args) != "{}" {
		t.Fatalf("lista: %+v, %v", cfgs, err)
	}
	for _, bad := range []string{``, `[]`, `{"configurations": [{}]}`, `[{"adapter": "x", "request": "r"}]`, `[{"adapter": "x", "args": "s"}]`} {
		if _, err := load(bad); err == nil {
			t.Errorf("%q debería fallar", bad)
		}
	}
	// Un archivo enorme se rechaza sin leerlo entero.
	if _, err := load(strings.Repeat(" ", maxLaunchFile+10)); err == nil || !strings.Contains(err.Error(), "grande") {
		t.Errorf("archivo enorme: %v", err)
	}
	// Un enlace simbólico que escapa de la raíz no se sigue.
	outside := filepath.Join(t.TempDir(), "fuera.json")
	if err := os.WriteFile(outside, []byte(`[{"adapter": "netcoredbg"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".wari", "launch.json")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("sin enlaces simbólicos: %v", err)
	}
	if _, err := loadLaunch(dir); err == nil {
		t.Error("launch.json como enlace fuera de la raíz no debe leerse")
	}
}

func TestDockSizesAndFocusCycle(t *testing.T) {
	installAdapter(t)
	root, main, helper := dbgProject(t, launchOK)
	f := newFakeDAP(main, helper)
	l := &fakeLauncher{f: f}
	m := newLayout(t, Config{Root: root, File: main, Debug: DebugConfig{Launcher: l.launch}}, 120, 30)
	d := newDriver(t, m)
	d.send(press(tea.KeyF9, 0), press(tea.KeyF5, 0))
	d.until("parada", stoppedWithTree)

	// Con árbol, editor, dock y agentes visibles, el foco los recorre en orden.
	var order []focus
	cur := d.m.moveFocus(-1) // del editor hacia atrás
	for range 4 {
		order = append(order, cur.focus)
		cur = cur.moveFocus(-1)
	}
	if want := []focus{focusTree, focusChat, focusDock, focusEditor}; !slices.Equal(order, want) {
		t.Fatalf("orden del foco = %v, quería %v", order, want)
	}

	d.send(press('v', tea.ModAlt))
	for _, sz := range [][2]int{{200, 60}, {120, 30}, {80, 24}, {60, 12}, {40, 10}, {30, 7}, {20, 5}, {10, 3}, {8, 2}, {120, 30}} {
		d.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		checkDims(t, d.m, sz[0], sz[1], "dock "+strconv.Itoa(sz[0])+"x"+strconv.Itoa(sz[1]))
		if g := d.m.geometry(); g.dockH > 0 {
			if g.bodyH-g.dockH < 4 {
				t.Fatalf("%dx%d: el editor se quedó con %d filas", sz[0], sz[1], g.bodyH-g.dockH)
			}
		} else if d.m.focus == focusDock {
			t.Fatalf("%dx%d: sin dock visible el foco no puede estar en él", sz[0], sz[1])
		}
	}
	// Con poco ancho solo caben algunas secciones, nunca desbordan.
	d.send(tea.WindowSizeMsg{Width: 50, Height: 30})
	if s := screen(d.m); !strings.Contains(s, "Variables") {
		t.Fatalf("la sección de variables debe quedarse:\n%s", s)
	}
}
