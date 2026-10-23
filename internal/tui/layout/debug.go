package layout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/dap"
	"github.com/dpa23/wari/internal/tui/editorview"
)

// Depuración (fase 14, desde el TUI) sobre internal/dap.
//
// toggle_breakpoint pone o quita un breakpoint en la línea del cursor; se
// marca en el margen del editor (◉ verificado por el adaptador, ○ aún sin
// verificar) y vale antes de iniciar la sesión: se envían al arrancar. Los
// breakpoints son por línea y no siguen a las ediciones: si insertas o borras
// líneas encima, hay que ponerlos de nuevo.
//
// debug_start lanza la sesión con el adaptador del archivo abierto (.cs:
// netcoredbg; .java y .kt: java-debug) si está en el PATH; si no, avisa en el
// panel de depuración. Con una sesión detenida, debug_start hace de continue.
// Después: debug_continue, debug_next, debug_step_in, debug_step_out y
// debug_stop. Al detenerse, el editor abre el archivo del marco superior y
// resalta la línea (el margen muestra ▶), el dock muestra la pila y las
// variables, que se navegan con el teclado (ver dock.go). Al reanudar se
// descarta el árbol de variables: sus referencias solo valen en esa parada.
//
// # Configuración de lanzamiento: .wari/launch.json
//
// WARI no inventa los argumentos de launch ni de attach: cada adaptador
// define los suyos (netcoredbg y java-debug los documentan) y WARI los envía
// tal cual. Se leen de .wari/launch.json, con esta forma:
//
//	{
//	  "configurations": [
//	    {
//	      "name": "API",              // nombre visible (opcional)
//	      "adapter": "netcoredbg",    // netcoredbg o java-debug
//	      "request": "launch",        // launch (por omisión) o attach
//	      "args": { ... }             // objeto JSON opaco: los argumentos del adaptador
//	    }
//	  ]
//	}
//
// También vale una lista de configuraciones sin el objeto exterior. Se usa
// la primera cuyo "adapter" coincide con el del archivo abierto. Sin el
// archivo, el panel explica cómo crearlo.
//
// Nada bloquea Update: la conexión, las peticiones y la lectura de eventos
// corren en tea.Cmd y vuelven como mensajes con la generación de la sesión y
// la época de la parada, para descartar lo que llega tarde. El árbol de
// variables (dap.Tree) no es seguro entre goroutines: Update lo toca solo
// cuando no hay una expansión en curso (busy); mientras una goroutine lo
// expande es suya, y el dibujo usa siempre las filas ya copiadas (varRow).

// DebugConfig configura la depuración.
type DebugConfig struct {
	// Launcher lanza los adaptadores de stdio; nil usa dap.DefaultLauncher
	// (internal/proc: grupo propio, sin huérfanos).
	Launcher dap.Launcher
	// Ports da la forma de conectarse a los adaptadores TCP, por nombre de
	// adaptador ("java-debug": pedirle a jdtls vscode.java.startDebugSession).
	// Sin entrada, ese adaptador no se puede iniciar y el panel lo explica.
	Ports map[string]dap.PortProvider
}

const (
	launchFile = "launch.json"
	// maxLaunchFile acota la lectura de .wari/launch.json.
	maxLaunchFile = 1 << 20
	// startTimeout acota el arranque de la sesión (initialize hasta launch).
	startTimeout = 2 * time.Minute
	opTimeout    = 30 * time.Second
)

// launchConfig es una entrada de .wari/launch.json.
type launchConfig struct {
	Name    string          `json:"name"`
	Adapter string          `json:"adapter"`
	Request string          `json:"request"`
	Args    json.RawMessage `json:"args"`
}

// loadLaunch lee .wari/launch.json: una lista de configuraciones o un objeto
// con "configurations". Valida adapter, request y que args sea un objeto.
func loadLaunch(root string) ([]launchConfig, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(filepath.Join(".wari", launchFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxLaunchFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxLaunchFile {
		return nil, errors.New("el archivo es demasiado grande")
	}
	var cfgs []launchConfig
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		err = json.Unmarshal(data, &cfgs)
	} else {
		var obj struct {
			Configurations []launchConfig `json:"configurations"`
		}
		err = json.Unmarshal(data, &obj)
		cfgs = obj.Configurations
	}
	if err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if len(cfgs) == 0 {
		return nil, errors.New("no define ninguna configuración")
	}
	for i := range cfgs {
		c := &cfgs[i]
		if c.Adapter == "" {
			return nil, fmt.Errorf("la configuración %d no tiene \"adapter\"", i+1)
		}
		switch c.Request {
		case "":
			c.Request = "launch"
		case "launch", "attach":
		default:
			return nil, fmt.Errorf("la configuración %d: \"request\" debe ser launch o attach, no %q", i+1, c.Request)
		}
		args := bytes.TrimSpace(c.Args)
		switch {
		case len(args) == 0 || string(args) == "null":
			c.Args = json.RawMessage("{}")
		case args[0] != '{':
			return nil, fmt.Errorf("la configuración %d: \"args\" debe ser un objeto JSON", i+1)
		default:
			c.Args = args
		}
	}
	return cfgs, nil
}

// launchHelp explica cómo crear .wari/launch.json (err es lo que falló al
// leerlo).
func launchHelp(err error) []string {
	var head string
	if errors.Is(err, os.ErrNotExist) {
		head = "Falta .wari/launch.json."
	} else {
		head = ".wari/launch.json no sirve: " + err.Error()
	}
	return []string{
		head,
		"Créalo con las configuraciones de lanzamiento:",
		`  {"configurations": [{"name": "API", "adapter": "netcoredbg",`,
		`    "request": "launch", "args": { ... }}]}`,
		"adapter: netcoredbg (.cs) o java-debug (.java, .kt) · request: launch o attach",
		"args: los argumentos de launch o attach que documenta el adaptador;",
		"WARI no los inventa: los envía tal cual.",
	}
}

// --- Estado ---------------------------------------------------------------------------

// varRow es una fila del árbol de variables ya copiada para dibujar. node solo
// se desreferencia en Update con el árbol libre.
type varRow struct {
	node                        *dap.Node
	depth                       int
	name, value, typ            string
	scope, expandable, expanded bool
}

// dbgState es el estado de la depuración.
type dbgState struct {
	sess *dap.Session
	// gen identifica la sesión (o su arranque); los mensajes de una anterior
	// se descartan.
	gen         int
	starting    bool
	cancelStart context.CancelFunc
	cancelled   bool
	ending      bool
	adapter     string
	config      string
	// epoch cambia en cada parada y al reanudar: el resultado de una consulta
	// de otra época se descarta.
	epoch    int
	stopText string
	frames   []dap.StackFrame
	frame    int // marco actual (el de las variables y la línea resaltada)
	stackSel int // fila del cursor en la pila
	tree     *dap.Tree
	busy     bool // una goroutine expande el árbol
	rows     []varRow
	varSel   int
	notice   []string
	// bps son los breakpoints pedidos por archivo (ruta limpia) y línea
	// (base 1); sobreviven a las sesiones.
	bps map[string]map[int]bool
}

// Mensajes de la depuración.
type (
	dbgStartedMsg struct {
		gen  int
		sess *dap.Session
		err  error
	}
	dbgEventMsg struct {
		gen int
		ev  dap.Event
	}
	dbgEndMsg  struct{ gen int }
	dbgStopMsg struct {
		gen, epoch int
		frames     []dap.StackFrame
		tree       *dap.Tree
		err        error
	}
	dbgTreeMsg struct {
		gen, epoch, frame int
		tree              *dap.Tree
		err               error
	}
	dbgExpandMsg struct {
		gen  int
		tree *dap.Tree
		err  error
	}
	dbgOpMsg struct {
		gen   int
		epoch int
		what  string
		err   error
	}
	dbgBPMsg      struct{ err error }
	dbgStoppedMsg struct{ err error }
)

// dbgStatus es el estado en palabras para la barra y el título de la pila.
func (m Model) dbgStatus() string {
	switch {
	case m.dbg.starting:
		return "iniciando"
	case m.dbg.sess == nil:
		return "sin sesión"
	case m.dbg.ending:
		return "terminando"
	}
	switch m.dbg.sess.State() {
	case dap.StateStopped:
		if m.dbg.stopText != "" {
			return "detenido (" + m.dbg.stopText + ")"
		}
		return "detenido"
	case dap.StateTerminated:
		return "terminado"
	}
	return "en ejecución"
}

// debugInfo es el texto de la barra mientras hay sesión ("" si no hay): el
// estado (sin el motivo de la parada, que va en el panel) y la tecla de parada.
func (m Model) debugInfo() string {
	if !m.dbg.starting && m.dbg.sess == nil {
		return ""
	}
	status, _, _ := strings.Cut(m.dbgStatus(), " (")
	s := "DBG " + status
	if k := m.firstKey("debug_stop"); k != "" {
		s += " · " + k + " detener"
	}
	return s
}

// --- Breakpoints ----------------------------------------------------------------------

// toggleBreakpoint es la acción toggle_breakpoint.
func (m Model) toggleBreakpoint() (Model, tea.Cmd) {
	if !m.hasEd {
		m.msg = "no hay archivo abierto"
		return m, nil
	}
	path := filepath.Clean(m.ed.Path())
	if _, ok := dap.AdapterForPath(path); !ok {
		m.msg = "no se pueden poner breakpoints en este tipo de archivo (.cs, .java, .kt)"
		return m, nil
	}
	line := m.ed.Buffer().Cursor().Line + 1
	if s := m.dbg.sess; s != nil {
		line = requestedLine(s, path, line)
	}
	if m.dbg.bps == nil {
		m.dbg.bps = map[string]map[int]bool{}
	}
	lines := m.dbg.bps[path]
	if lines == nil {
		lines = map[int]bool{}
		m.dbg.bps[path] = lines
	}
	set := !lines[line]
	if set {
		lines[line] = true
		m.msg = fmt.Sprintf("breakpoint en %s:%d", m.display(path), line)
	} else {
		delete(lines, line)
		m.msg = fmt.Sprintf("breakpoint quitado de %s:%d", m.display(path), line)
	}
	m.applyDebugMarks()
	s := m.dbg.sess
	if s == nil {
		return m, nil
	}
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		if set {
			return dbgBPMsg{err: s.SetBreakpoint(ctx, path, line)}
		}
		return dbgBPMsg{err: s.ClearBreakpoint(ctx, path, line)}
	}
}

// requestedLine traduce una línea del margen a la que se pidió: el adaptador
// puede haber movido el breakpoint a otra línea ejecutable.
func requestedLine(s *dap.Session, path string, line int) int {
	for _, b := range s.Breakpoints(path) {
		if b.ActualLine == line && b.Line != line {
			return b.Line
		}
	}
	return line
}

// applyDebugMarks pone en el margen del editor los breakpoints del archivo y
// la línea actual del depurado.
func (m *Model) applyDebugMarks() {
	if !m.hasEd {
		return
	}
	path := filepath.Clean(m.ed.Path())
	var marks map[int]editorview.BreakpointMark
	add := func(line int, mk editorview.BreakpointMark) {
		if marks == nil {
			marks = map[int]editorview.BreakpointMark{}
		}
		marks[line-1] = mk
	}
	for line := range m.dbg.bps[path] {
		add(line, editorview.BreakpointPending)
	}
	if s := m.dbg.sess; s != nil {
		for _, b := range s.Breakpoints(path) {
			if !m.dbg.bps[path][b.Line] {
				continue // ya quitado, a la espera de que el adaptador lo confirme
			}
			line := b.Line
			if b.ActualLine > 0 {
				line = b.ActualLine
				if line != b.Line {
					delete(marks, b.Line-1)
				}
			}
			if b.Verified {
				add(line, editorview.BreakpointVerified)
			} else {
				add(line, editorview.BreakpointPending)
			}
		}
	}
	m.ed.SetBreakpoints(marks)
	m.ed.SetExecLine(-1)
	if f, ok := m.currentFrame(); ok && f.Line > 0 && m.framePath(f) == path {
		m.ed.SetExecLine(f.Line - 1)
	}
}

// currentFrame es el marco elegido de la pila.
func (m Model) currentFrame() (dap.StackFrame, bool) {
	if m.dbg.frame < 0 || m.dbg.frame >= len(m.dbg.frames) {
		return dap.StackFrame{}, false
	}
	return m.dbg.frames[m.dbg.frame], true
}

// framePath es la ruta absoluta y limpia del archivo del marco ("" si no tiene).
func (m Model) framePath(f dap.StackFrame) string {
	if f.Source == nil || f.Source.Path == "" {
		return ""
	}
	p := filepath.FromSlash(f.Source.Path)
	if !filepath.IsAbs(p) {
		p = filepath.Join(m.root, p)
	}
	return filepath.Clean(p)
}

// --- Inicio y fin ---------------------------------------------------------------------

// debugNotice muestra un aviso de la depuración en el dock.
func (m *Model) debugNotice(lines ...string) {
	m.dbg.notice = lines
	if len(lines) > 0 {
		m.msg = lines[0]
	}
	m.openDock()
}

// adapterProblem explica por qué el adaptador no se puede usar ("" si se puede).
func (m Model) adapterProblem(a dap.Adapter) string {
	if a.Transport == dap.TCP {
		if m.dbgCfg.Ports[a.Name] == nil {
			return a.Name + " requiere jdtls con el plugin (com.microsoft.java.debug.plugin cargado como bundle) y WARI aún no lo arranca por su cuenta"
		}
		return ""
	}
	if _, err := a.Detect(); err != nil {
		return a.Name + " no está instalado (no está en el PATH)"
	}
	return ""
}

// knownAdapter busca un adaptador conocido por nombre.
func knownAdapter(name string) (dap.Adapter, bool) {
	for _, a := range dap.Known {
		if a.Name == name {
			return a, true
		}
	}
	return dap.Adapter{}, false
}

// debugStart es la acción debug_start: con una sesión detenida continúa.
func (m Model) debugStart() (Model, tea.Cmd) {
	if m.dbg.starting {
		m.msg = "la depuración se está iniciando"
		return m, nil
	}
	if s := m.dbg.sess; s != nil {
		if _, stopped := s.StoppedThread(); stopped {
			return m.debugResume("continue")
		}
		m.msg = "ya hay una sesión de depuración (" + m.firstKey("debug_stop") + " la detiene)"
		return m, nil
	}
	m.dbg.notice = nil

	var adapter dap.Adapter
	var have bool
	if m.hasEd {
		if adapter, have = dap.AdapterForPath(m.ed.Path()); !have {
			m.debugNotice("No hay adaptador de depuración para "+filepath.Ext(m.ed.Path())+".",
				"Se depura .cs (netcoredbg) y .java o .kt (java-debug): abre uno de esos archivos.")
			return m, nil
		}
		if why := m.adapterProblem(adapter); why != "" {
			m.debugNotice(why)
			return m, nil
		}
	}
	cfgs, err := loadLaunch(m.root)
	if err != nil {
		m.debugNotice(launchHelp(err)...)
		return m, nil
	}
	if !have {
		a, ok := knownAdapter(cfgs[0].Adapter)
		if !ok {
			m.debugNotice(fmt.Sprintf("El adaptador %q de .wari/launch.json no existe (netcoredbg o java-debug).", cfgs[0].Adapter))
			return m, nil
		}
		if why := m.adapterProblem(a); why != "" {
			m.debugNotice(why)
			return m, nil
		}
		adapter = a
	}
	var cfg *launchConfig
	for i := range cfgs {
		if cfgs[i].Adapter == adapter.Name {
			cfg = &cfgs[i]
			break
		}
	}
	if cfg == nil {
		m.debugNotice(fmt.Sprintf(".wari/launch.json no tiene ninguna configuración con \"adapter\": %q.", adapter.Name))
		return m, nil
	}

	m.dbg.gen++
	m.dbg.starting, m.dbg.cancelled, m.dbg.ending = true, false, false
	m.dbg.adapter, m.dbg.config = adapter.Name, cfg.Name
	m.discardStop()
	m.openDock()
	m.msg = "iniciando " + adapter.Name + "…"

	startCtx, cancel := context.WithCancel(m.hub.ctx)
	m.dbg.cancelStart = cancel
	type bp struct {
		file string
		line int
	}
	var bps []bp
	for f, lines := range m.dbg.bps {
		for l := range lines {
			bps = append(bps, bp{f, l})
		}
	}
	h, gen, root := m.hub, m.dbg.gen, m.root
	launcher, port := m.dbgCfg.Launcher, m.dbgCfg.Ports[adapter.Name]
	cfgCopy := *cfg
	return m, func() tea.Msg {
		defer cancel()
		client, err := dap.Connect(h.ctx, adapter, dap.ConnectOptions{Launcher: launcher, Dir: root, Port: port})
		if err != nil {
			return dbgStartedMsg{gen: gen, err: err}
		}
		s := dap.NewSession(client, dap.SessionOptions{AdapterID: adapter.AdapterID})
		if !h.setSession(s) {
			return dbgStartedMsg{gen: gen, err: errors.New("WARI se está cerrando")}
		}
		// Antes de Start, SetBreakpoint solo registra: se envían al arrancar.
		for _, b := range bps {
			if err := s.SetBreakpoint(startCtx, b.file, b.line); err != nil {
				_ = s.Stop(startCtx)
				return dbgStartedMsg{gen: gen, sess: s, err: err}
			}
		}
		ctx, stop := context.WithTimeout(startCtx, startTimeout)
		defer stop()
		if cfgCopy.Request == "attach" {
			err = s.StartAttach(ctx, cfgCopy.Args)
		} else {
			err = s.Start(ctx, cfgCopy.Args)
		}
		return dbgStartedMsg{gen: gen, sess: s, err: err}
	}
}

// onDebugStarted incorpora el resultado del arranque.
func (m Model) onDebugStarted(msg dbgStartedMsg) (Model, tea.Cmd) {
	if msg.gen != m.dbg.gen || !m.dbg.starting {
		if msg.sess != nil {
			s := msg.sess
			m.hub.clearSession(s)
			return m, stopSessionCmd(s)
		}
		return m, nil
	}
	m.dbg.starting = false
	m.dbg.cancelStart = nil
	if msg.err != nil {
		if msg.sess != nil {
			m.hub.clearSession(msg.sess)
		}
		why := "no se pudo iniciar la depuración: " + msg.err.Error()
		if m.dbg.cancelled {
			why = "depuración cancelada"
		}
		m.dbg.notice = []string{why}
		m.msg = why
		m.applyDebugMarks()
		return m, nil
	}
	m.dbg.sess = msg.sess
	m.dbg.notice = nil
	m.applyDebugMarks()
	name := m.dbg.adapter
	if m.dbg.config != "" {
		name += " · " + m.dbg.config
	}
	m.msg = "depurando con " + name
	return m, pumpDebug(msg.gen, msg.sess)
}

// pumpDebug espera el siguiente evento del adaptador.
func pumpDebug(gen int, s *dap.Session) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-s.Events()
		if !ok {
			return dbgEndMsg{gen: gen}
		}
		return dbgEventMsg{gen: gen, ev: ev}
	}
}

// onDebugEvent aplica un evento del adaptador y sigue leyendo.
func (m Model) onDebugEvent(msg dbgEventMsg) (Model, tea.Cmd) {
	if msg.gen != m.dbg.gen || m.dbg.sess == nil {
		return m, nil
	}
	s := m.dbg.sess
	cmds := []tea.Cmd{pumpDebug(msg.gen, s)}
	switch e := msg.ev.(type) {
	case dap.StoppedEvent:
		m.discardStop()
		m.dbg.stopText = e.Reason
		cmds = append(cmds, m.fetchStop())
	case dap.ContinuedEvent:
		m.discardStop()
		m.applyDebugMarks()
	case dap.OutputEvent:
		switch e.Category {
		case "telemetry":
		case "stderr":
			m.appendOut("stderr", e.Output)
		default:
			m.appendOut("debug", e.Output)
		}
	case dap.ExitedEvent:
		m.appendOut("debug", fmt.Sprintf("El depurado terminó con código %d.", e.ExitCode))
	case dap.TerminatedEvent:
		m.discardStop()
		m.applyDebugMarks()
		if !m.dbg.ending {
			m.dbg.ending = true
			cmds = append(cmds, stopSessionCmd(s))
		}
	case dap.BreakpointEvent:
		m.applyDebugMarks()
	}
	return m, tea.Batch(cmds...)
}

// discardStop descarta lo que depende de la parada: pila, línea resaltada y
// árbol de variables (sus referencias no valen tras reanudar).
func (m *Model) discardStop() {
	m.dbg.epoch++
	m.dbg.frames, m.dbg.frame, m.dbg.stackSel = nil, 0, 0
	m.dbg.tree, m.dbg.busy = nil, false
	m.dbg.rows, m.dbg.varSel = nil, 0
	m.dbg.stopText = ""
}

// fetchStop pide la pila y el árbol del marco superior de la parada actual.
func (m Model) fetchStop() tea.Cmd {
	s, gen, epoch := m.dbg.sess, m.dbg.gen, m.dbg.epoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		frames, err := s.Stack(ctx, 0)
		if err != nil {
			return dbgStopMsg{gen: gen, epoch: epoch, err: err}
		}
		msg := dbgStopMsg{gen: gen, epoch: epoch, frames: frames}
		if len(frames) > 0 {
			msg.tree, msg.err = s.Tree(ctx, frames[0].ID)
		}
		return msg
	}
}

// onDebugStop muestra la parada: pila, variables y línea resaltada.
func (m Model) onDebugStop(msg dbgStopMsg) (Model, tea.Cmd) {
	if msg.gen != m.dbg.gen || msg.epoch != m.dbg.epoch || m.dbg.sess == nil {
		return m, nil
	}
	if msg.err != nil && len(msg.frames) == 0 {
		m.msg = "depuración: " + msg.err.Error()
		return m, nil
	}
	m.dbg.frames, m.dbg.frame, m.dbg.stackSel = msg.frames, 0, 0
	m.dbg.tree, m.dbg.busy = msg.tree, false
	m.dbg.rows, m.dbg.varSel = nil, 0
	m.rebuildRows()
	m.dock.sub = dockVars
	m.openDock()
	m.jumpToFrame(0)
	if msg.err != nil {
		m.msg = "variables: " + msg.err.Error()
	}
	return m, nil
}

// jumpToFrame lleva el editor al archivo y la línea del marco i y la resalta.
// Nunca descarta cambios sin guardar ni roba el foco.
func (m *Model) jumpToFrame(i int) {
	m.dbg.frame = i
	f, ok := m.currentFrame()
	if !ok {
		m.applyDebugMarks()
		return
	}
	if p := m.framePath(f); p != "" {
		where := fmt.Sprintf("%s:%d", m.display(p), f.Line)
		if !m.hasEd || m.ed.Path() != p {
			switch {
			case m.hasEd && m.ed.Dirty():
				m.msg = "detenido en " + where + ": guarda " + m.display(m.ed.Path()) + " para abrirlo"
			default:
				prior := m.focus
				m.openFile(p)
				m.setFocus(prior)
				m.applyFocus()
			}
		}
		if m.hasEd && m.ed.Path() == p && f.Line > 0 {
			m.ed.GoTo(f.Line-1, 0)
			m.msg = "detenido en " + where
			if m.dbg.stopText != "" {
				m.msg += " (" + m.dbg.stopText + ")"
			}
		}
	}
	m.applyDebugMarks()
}

// stopSessionCmd cierra la sesión (disconnect y fin del grupo del adaptador).
func stopSessionCmd(s *dap.Session) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return dbgStoppedMsg{err: s.Stop(ctx)}
	}
}

// onDebugEnd limpia la sesión cuando se cerró su flujo de eventos.
func (m Model) onDebugEnd(msg dbgEndMsg) (Model, tea.Cmd) {
	if msg.gen != m.dbg.gen || m.dbg.sess == nil {
		return m, nil
	}
	s := m.dbg.sess
	m.hub.clearSession(s)
	m.dbg.sess, m.dbg.ending = nil, false
	m.discardStop()
	m.applyDebugMarks()
	m.appendOut("debug", "Depuración terminada.")
	m.msg = "depuración terminada"
	if m.focus == focusDock && !m.busyDock() {
		m.dock.sub = dockOut
	}
	// Si el adaptador cayó sin disconnect, esto mata su grupo (idempotente).
	return m, stopSessionCmd(s)
}

// debugStop es la acción debug_stop.
func (m Model) debugStop() (Model, tea.Cmd) {
	switch {
	case m.dbg.starting:
		m.dbg.cancelled = true
		if m.dbg.cancelStart != nil {
			m.dbg.cancelStart()
		}
		m.msg = "cancelando la depuración…"
		return m, nil
	case m.dbg.sess == nil:
		m.msg = "no hay ninguna sesión de depuración"
		return m, nil
	case m.dbg.ending:
		return m, nil
	}
	m.dbg.ending = true
	m.msg = "deteniendo la depuración…"
	return m, stopSessionCmd(m.dbg.sess)
}

// --- Control de la ejecución -----------------------------------------------------------

// debugResume reanuda el depurado ("continue", "next", "in" u "out") y
// descarta la pila y las variables de la parada.
func (m Model) debugResume(what string) (Model, tea.Cmd) {
	s := m.dbg.sess
	if s == nil {
		m.msg = "no hay sesión de depuración (" + m.firstKey("debug_start") + " la inicia)"
		return m, nil
	}
	if _, stopped := s.StoppedThread(); !stopped {
		m.msg = "el depurado no está detenido"
		return m, nil
	}
	m.discardStop()
	m.applyDebugMarks()
	m.msg = "depurando…"
	gen, epoch := m.dbg.gen, m.dbg.epoch
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		var err error
		switch what {
		case "next":
			err = s.Next(ctx, 0)
		case "in":
			err = s.StepIn(ctx, 0)
		case "out":
			err = s.StepOut(ctx, 0)
		default:
			err = s.Continue(ctx, 0)
		}
		return dbgOpMsg{gen: gen, epoch: epoch, what: what, err: err}
	}
}

// onDebugOp atiende el resultado de reanudar; si falló y el depurado sigue
// detenido, recupera la pila.
func (m Model) onDebugOp(msg dbgOpMsg) (Model, tea.Cmd) {
	if msg.gen != m.dbg.gen || m.dbg.sess == nil || msg.err == nil {
		return m, nil
	}
	m.msg = "depuración: " + msg.err.Error()
	if msg.epoch == m.dbg.epoch {
		if _, stopped := m.dbg.sess.StoppedThread(); stopped {
			m.dbg.epoch++
			return m, m.fetchStop()
		}
	}
	return m, nil
}

// --- Pila y variables ----------------------------------------------------------------

// rebuildRows copia el árbol a filas dibujables, conservando la selección.
// Solo con el árbol libre (no busy).
func (m *Model) rebuildRows() {
	var keep *dap.Node
	if m.dbg.varSel >= 0 && m.dbg.varSel < len(m.dbg.rows) {
		keep = m.dbg.rows[m.dbg.varSel].node
	}
	m.dbg.rows = nil
	if m.dbg.tree == nil {
		m.dbg.varSel = 0
		return
	}
	sel := 0
	for i, r := range m.dbg.tree.Rows() {
		n := r.Node
		m.dbg.rows = append(m.dbg.rows, varRow{
			node: n, depth: r.Depth, name: n.Name, value: n.Value, typ: n.Type,
			scope: n.Scope, expandable: n.Expandable(), expanded: n.Expanded,
		})
		if n == keep {
			sel = i
		}
	}
	m.dbg.varSel = max(0, min(sel, len(m.dbg.rows)-1))
}

// stackKey mueve el cursor de la pila; enter elige el marco.
func (m Model) stackKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	n := len(m.dbg.frames)
	switch {
	case n == 0:
	case key.Matches(k, palKeys.up):
		m.dbg.stackSel = max(0, m.dbg.stackSel-1)
	case key.Matches(k, palKeys.down):
		m.dbg.stackSel = min(n-1, m.dbg.stackSel+1)
	case m.is(k, "page_up"):
		m.dbg.stackSel = max(0, m.dbg.stackSel-dockPage)
	case m.is(k, "page_down"):
		m.dbg.stackSel = min(n-1, m.dbg.stackSel+dockPage)
	case m.is(k, "send"):
		return m.selectFrame(m.dbg.stackSel)
	}
	return m, nil
}

// selectFrame elige un marco: salta a su línea y recarga las variables.
func (m Model) selectFrame(i int) (Model, tea.Cmd) {
	if i < 0 || i >= len(m.dbg.frames) || m.dbg.sess == nil {
		return m, nil
	}
	m.jumpToFrame(i)
	s, gen, epoch, frame := m.dbg.sess, m.dbg.gen, m.dbg.epoch, m.dbg.frames[i].ID
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		tree, err := s.Tree(ctx, frame)
		return dbgTreeMsg{gen: gen, epoch: epoch, frame: i, tree: tree, err: err}
	}
}

// onDebugTree instala el árbol de otro marco.
func (m Model) onDebugTree(msg dbgTreeMsg) (Model, tea.Cmd) {
	if msg.gen != m.dbg.gen || msg.epoch != m.dbg.epoch || msg.frame != m.dbg.frame || m.dbg.sess == nil {
		return m, nil
	}
	m.dbg.tree, m.dbg.busy = msg.tree, false
	m.dbg.rows, m.dbg.varSel = nil, 0
	m.rebuildRows()
	if msg.err != nil {
		m.msg = "variables: " + msg.err.Error()
	}
	return m, nil
}

// varsKey navega el árbol de variables: flechas, derecha o enter expanden,
// izquierda pliega o sube al padre.
func (m Model) varsKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	rows := m.dbg.rows
	n := len(rows)
	if n == 0 {
		return m, nil
	}
	sel := max(0, min(m.dbg.varSel, n-1))
	switch {
	case key.Matches(k, palKeys.up):
		m.dbg.varSel = max(0, sel-1)
	case key.Matches(k, palKeys.down):
		m.dbg.varSel = min(n-1, sel+1)
	case m.is(k, "page_up"):
		m.dbg.varSel = max(0, sel-dockPage)
	case m.is(k, "page_down"):
		m.dbg.varSel = min(n-1, sel+dockPage)
	case k.String() == "right":
		r := rows[sel]
		switch {
		case !r.expandable:
		case r.expanded:
			if sel+1 < n && rows[sel+1].depth > r.depth {
				m.dbg.varSel = sel + 1
			}
		default:
			return m.expandVar(sel)
		}
	case k.String() == "left":
		r := rows[sel]
		if r.expandable && r.expanded {
			if m.dbg.busy {
				m.msg = "cargando variables…"
				return m, nil
			}
			m.dbg.tree.Collapse(r.node)
			m.rebuildRows()
			return m, nil
		}
		for i := sel - 1; i >= 0; i-- { // sube al padre
			if rows[i].depth < r.depth {
				m.dbg.varSel = i
				break
			}
		}
	case m.is(k, "send"):
		r := rows[sel]
		switch {
		case !r.expandable:
		case r.expanded:
			if m.dbg.busy {
				m.msg = "cargando variables…"
				return m, nil
			}
			m.dbg.tree.Collapse(r.node)
			m.rebuildRows()
		default:
			return m.expandVar(sel)
		}
	}
	return m, nil
}

// expandVar expande la fila sel. Un nodo con hijos ya cargados se expande en
// el acto; si no, una goroutine los pide y el árbol es suyo hasta que vuelva.
func (m Model) expandVar(sel int) (Model, tea.Cmd) {
	if m.dbg.busy {
		m.msg = "cargando variables…"
		return m, nil
	}
	n := m.dbg.rows[sel].node
	tree := m.dbg.tree
	if len(n.Children) > 0 {
		_ = tree.Expand(context.Background(), n) // sin red: ya están cargados
		m.rebuildRows()
		return m, nil
	}
	m.dbg.busy = true
	m.msg = "cargando variables…"
	gen := m.dbg.gen
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		return dbgExpandMsg{gen: gen, tree: tree, err: tree.Expand(ctx, n)}
	}
}

// onDebugExpand recupera el árbol tras una expansión.
func (m Model) onDebugExpand(msg dbgExpandMsg) (Model, tea.Cmd) {
	if msg.gen != m.dbg.gen || msg.tree != m.dbg.tree {
		return m, nil // la parada ya pasó: el árbol se descartó
	}
	m.dbg.busy = false
	m.rebuildRows()
	if msg.err != nil {
		m.msg = "no se pudo expandir: " + msg.err.Error()
	} else {
		m.msg = ""
	}
	return m, nil
}

// debugUpdate atiende los mensajes de la depuración. handled es false para
// los demás.
func (m Model) debugUpdate(msg tea.Msg) (_ Model, _ tea.Cmd, handled bool) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case dbgStartedMsg:
		m, cmd = m.onDebugStarted(msg)
	case dbgEventMsg:
		m, cmd = m.onDebugEvent(msg)
	case dbgEndMsg:
		m, cmd = m.onDebugEnd(msg)
	case dbgStopMsg:
		m, cmd = m.onDebugStop(msg)
	case dbgTreeMsg:
		m, cmd = m.onDebugTree(msg)
	case dbgExpandMsg:
		m, cmd = m.onDebugExpand(msg)
	case dbgOpMsg:
		m, cmd = m.onDebugOp(msg)
	case dbgBPMsg:
		m.applyDebugMarks()
		if msg.err != nil {
			m.msg = "breakpoint: " + msg.err.Error()
		}
	case dbgStoppedMsg:
		if msg.err != nil {
			m.msg = "detener la depuración: " + msg.err.Error()
		}
	default:
		return m, nil, false
	}
	return m, cmd, true
}
