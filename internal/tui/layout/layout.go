// Package layout es el modelo raíz de WARI (el que corre tea.Program): árbol
// de archivos a la izquierda, editor al centro y chat de agentes a la
// derecha, con una barra de estado y una paleta de comandos. Es el único
// lugar con AltScreen y cursor real: los componentes se dibujan sin ellos y
// el layout traduce el cursor del que tiene foco a coordenadas absolutas.
//
// Foco. focus_next y focus_prev (tab y shift+tab) recorren los paneles
// visibles en el orden árbol, editor, dock (si está abierto) y agentes. En el editor, tab inserta
// espacios, así que ahí focus_next no actúa cuando su tecla es la misma que la
// de Tab del editor; focus_prev sigue funcionando y sale hacia el panel
// anterior (y el siguiente tras el último es el árbol). La paleta también
// ofrece "Ir al árbol / editor / panel de agentes". Si se reasigna focus_next
// a otra tecla, funciona también dentro del editor.
//
// Atajos globales (valen con cualquier foco): quit, palette, toggle_tree,
// toggle_agents, save, stop_all, focus_next, focus_prev, focus_dock, las de
// ejecución y depuración y, si hay un pipeline,
// pipeline_run, gate_approve, gate_reject y gate_edit (van al panel de agentes
// aunque no tenga el foco). El resto va al panel con foco.
// Todos salen de config.Keymap.
//
// Ayuda. F1 o '?' abren el overlay modal de ayuda con los atajos vigentes de
// config.Keymap agrupados por contexto (Navegación, Editor, Agentes, Diffs, Pipeline).
// Regla para '?': solo actúa cuando el foco no está escribiendo en el editor
// (con archivo abierto) ni en la entrada de texto del chat, para no interceptar
// el signo '?' al redactar código o mensajes. F1 abre la ayuda siempre.
// Esc cierra el overlay; el overlay se adapta sin desbordes a cualquier tamaño.
//
// Snippets. En un archivo .java, .kt o .cs, Tab tras un prefijo exacto expande
// el snippet y Tab / Shift+Tab recorren sus paradas (Esc las termina); ahí
// Tab y Shift+Tab no mueven el foco. Nuevo archivo desde plantilla: acción
// new_file (o la paleta); ver newfile.go.
//
// Imports. Con un tipo conocido bajo el cursor al que le falta el import, la
// línea de estado lo sugiere; import_type (o la paleta) lo inserta, y si hay
// varios candidatos (List: java.util o java.awt) abre un selector. Ver
// imports.go.
//
// Estructura e índice. outline (alt+o) lista en la paleta los símbolos del
// archivo con sus anotaciones y salta al elegir; goto_definition (F12, alt+d)
// busca el identificador bajo el cursor en el mismo archivo, el mismo
// package, los imports y el resto del proyecto (selector si hay varios);
// complete (alt+/, ctrl+space) propone nombres del proyecto. El índice se
// llena en segundo plano y sus tipos se suman a los mapas de imports. Ver
// index.go. Con el foco en el chat, estas teclas son del chat.
//
// LSP opcional (apagado por omisión). toggle_lsp (alt+l, o -lsp al iniciar) lo
// enciende: complete y goto_definition consultan primero al servidor
// (jdtls, kotlin-language-server, csharp-ls) y caen al índice propio si no
// está instalado o no responde a tiempo; los diagnósticos marcan el margen y
// salen en la barra y en la lista de la acción diagnostics (alt+m). Ver lsp.go.
//
// Ejecución en el sandbox y depuración (fases 13 y 14). run_project ejecuta el
// perfil del proyecto en un contenedor (nunca descarga imágenes y pide
// confirmar la red); toggle_breakpoint marca breakpoints en el margen y
// debug_start, debug_continue, debug_next, debug_step_in, debug_step_out y
// debug_stop llevan la sesión con netcoredbg o java-debug, con los argumentos
// de .wari/launch.json. Todo se ve en el dock bajo el editor (salida con su
// tecla de parada siempre visible, pila y variables navegables, y el foco
// con focus_dock). Ver run.go, debug.go y dock.go.
//
// Diff. diff_file (ctrl+r) abre el diff de la última edición de un agente
// contra su checkpoint en el panel central, como vista de solo lectura a todo
// el ancho del editor (números de línea, n/p entre hunks, flechas y pgup/pgdn
// para desplazar, revert_file/revert_turn/revert_agent con su confirmación).
// Esc vuelve al editor: el archivo abierto y sus cambios sin guardar no se
// tocan. El panel de agentes conserva su historial. Ver tui.Panel.DiffKey.
//
// Seguridad de datos: abrir otro archivo con el actual sin guardar, o salir
// en ese caso (con la tecla o desde la paleta), pide repetir la acción; la
// confirmación se desarma con cualquier otra tecla. Guardar la confirma.
// Los archivos binarios o que no son UTF-8 válido no se abren: guardarlos
// corrompería los bytes.
package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/dpa23/wari/internal/tui/theme"

	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/editor"
	"github.com/dpa23/wari/internal/imports"
	"github.com/dpa23/wari/internal/index"
	"github.com/dpa23/wari/internal/lsp"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/snippets"
	"github.com/dpa23/wari/internal/templates"
	"github.com/dpa23/wari/internal/tui"
	"github.com/dpa23/wari/internal/tui/editorview"
	"github.com/dpa23/wari/internal/tui/filetree"
	"github.com/dpa23/wari/internal/tui/icons"
	"github.com/dpa23/wari/internal/tui/tile"
)

const (
	// editorMin es el ancho mínimo del editor; si los demás paneles lo
	// dejarían más estrecho, se pliegan solos (primero agentes, luego árbol).
	editorMin = 20
	// binarySniff es cuántos bytes iniciales se miran para detectar NUL.
	binarySniff = 8000
	sepWidth    = 1
)

type focus int

const (
	focusTree focus = iota
	focusEditor
	focusChat
	// focusTerm es un agente en terminal; cuál lo dice el tile con foco.
	focusTerm
	// focusDock es el dock de salida y depuración bajo el editor (ver dock.go).
	focusDock
)

// Config configura el layout.
type Config struct {
	// Icons elige los iconos junto a los archivos del árbol (apagados si es el cero).
	Icons filetree.IconMode
	// Root es el directorio de trabajo (vacío: el actual).
	Root string
	// File es un archivo a abrir en el editor al iniciar (opcional).
	File string
	// Keymap son los atajos; nil usa config.Default().
	Keymap config.Keymap
	// Snippets son los snippets por lenguaje ("java", "kotlin", "csharp") que
	// expande Tab en el editor; nil los desactiva.
	Snippets map[string]snippets.Set
	// Imports son los mapas tipo→import por lenguaje de import_type y de la
	// sugerencia de la línea de estado; nil los desactiva.
	Imports map[string]imports.Map
	// Index es el índice de símbolos del proyecto (outline aparte, ir a
	// definición, completar y los tipos propios en los imports); nil lo
	// desactiva. El layout lo llena en segundo plano.
	Index *index.Index
	// LSP es el gestor de servidores de lenguaje (fase 12); nil desactiva el
	// LSP por completo. Nada se lanza hasta que se enciende y se abre un
	// archivo. Quien lo crea lo cierra al salir (Manager.Shutdown).
	LSP *lsp.Manager
	// LSPOn enciende el LSP al iniciar; si no, toggle_lsp lo enciende.
	LSPOn bool
	// Templates son las plantillas de "Nuevo archivo desde plantilla"; sin
	// ellas la acción solo avisa.
	Templates []templates.Template
	// Sandbox ejecuta el proyecto en un contenedor (acción run_project, fase
	// 13); nil la desactiva. cmd/wari pasa un *sandbox.Container. Quien crea
	// el layout llama a Model.Shutdown al salir.
	Sandbox Sandbox
	// Debug configura la depuración (fase 14).
	Debug DebugConfig
	// ShowChat muestra el panel de chat al arrancar; por omisión está oculto
	// (los agentes en terminal van en sus propios tiles).
	ShowChat bool
	// Chat configura el panel de agentes (Keymap y Task.Dir los completa el
	// layout).
	Chat tui.Config
}

// Model es el modelo raíz. Receptor de valor.
type Model struct {
	km    config.Keymap
	binds map[string]key.Binding
	root  string

	tree  filetree.Model
	ed    editorview.Model
	hasEd bool
	chat  tui.Panel

	w, h int
	// ts es el área de trabajo en tiles (tiles.go).
	ts    *tiling
	focus focus

	msg         string // mensaje de la barra de estado
	pendingOpen string // archivo cuya apertura espera confirmación
	leaderArmed bool   // la última tecla fue la líder: la siguiente lleva alt
	quitArmed   bool   // la última tecla fue quit con cambios sin guardar

	snips map[string]snippets.Set
	imps  map[string]imports.Map
	imp   importState
	// ix es el índice de símbolos; allImps, los mapas de imports más los tipos
	// del proyecto; xs, las listas de la paleta en los modos de índice.
	ix      *index.Index
	allImps map[string]imports.Map
	xs      indexState
	tpls    []templates.Template
	nf      newFileState
	// lsp es el estado del LSP opcional; xcmd, lo que openFile (que no
	// devuelve Cmd) deja para el final del update.
	lsp  lspState
	xcmd tea.Cmd

	pal palette
	// files es la última lista de archivos del proyecto; se refresca en
	// segundo plano cada vez que se abre la paleta.
	files []string

	helpOpen   bool
	helpScroll int

	// Ejecución en el sandbox y depuración (run.go, debug.go, dock.go). hub es
	// el único puntero compartido entre las copias del modelo.
	hub    *hub
	sbx    Sandbox
	dbgCfg DebugConfig
	run    runState
	dbg    dbgState
	dock   dockState
	out    []outLine
	sup    *orch.Supervisor
}

// filesMsg lleva la lista de archivos del proyecto para la paleta.
type filesMsg struct{ files []string }

var _ tea.Model = Model{}

// New crea el layout. El foco inicial es el árbol, o el editor si se abre un
// archivo.
func New(cfg Config) Model {
	root := cfg.Root
	if root == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	km := cfg.Keymap
	if km == nil {
		km = config.Default()
	}
	binds := make(map[string]key.Binding, len(km))
	for action, keys := range km {
		binds[action] = key.NewBinding(key.WithKeys(keys...))
	}

	chatCfg := cfg.Chat
	chatCfg.Keymap = km
	if chatCfg.Task.Dir == "" {
		chatCfg.Task.Dir = root
	}

	m := Model{
		km: km, binds: binds, root: root,
		snips: cfg.Snippets, imps: cfg.Imports, allImps: cfg.Imports, tpls: cfg.Templates,
		ix:     cfg.Index,
		lsp:    lspState{mgr: cfg.LSP},
		hub:    newHub(),
		sbx:    cfg.Sandbox,
		dbgCfg: cfg.Debug,
		sup:    cfg.Chat.Supervisor,
		tree:   filetree.New(root),
		chat:   tui.NewPanel(chatCfg),
		w:      80,
		h:      24,
		ts:     newTiling(),
		focus:  focusTree,
	}
	m.tree.SetIcons(cfg.Icons)
	switch cfg.Icons {
	case filetree.IconsNerd:
		icons.Use(icons.Nerd)
	case filetree.IconsEmoji:
		icons.Use(icons.Emoji)
	default:
		icons.Use(icons.ASCII)
	}
	if cfg.ShowChat {
		m.setChatOn(true)
	}
	if cfg.File != "" {
		m.openFile(cfg.File)
	}
	m.relayout()
	m.lsp.on = cfg.LSPOn && cfg.LSP != nil
	return m
}

// Init implementa tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.chat.Init(), m.refreshIndex()}
	if theme.NeedsQuery() {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	if m.lsp.on {
		// El arranque del servidor es un mensaje más: así lo atiende Update.
		cmds = append(cmds, func() tea.Msg { return lspBootMsg{} })
	}
	return tea.Batch(cmds...)
}

// Update implementa tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m.update(msg) }

// update atiende un mensaje y suma lo que el LSP necesite: el debounce de
// didChange tras una tecla o un pegado, y los Cmd que dejó openFile.
func (m Model) update(msg tea.Msg) (Model, tea.Cmd) {
	m, cmd := m.updateMsg(msg)
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg:
		if c := m.lspEdited(); c != nil {
			cmd = tea.Batch(cmd, c)
		}
	}
	if m.xcmd != nil {
		cmd = tea.Batch(cmd, m.xcmd)
		m.xcmd = nil
	}
	return m, cmd
}

func (m Model) updateMsg(msg tea.Msg) (Model, tea.Cmd) {
	if m2, cmd, ok := m.lspUpdate(msg); ok {
		return m2, cmd
	}
	if m2, cmd, ok := m.runUpdate(msg); ok {
		return m2, cmd
	}
	if m2, cmd, ok := m.debugUpdate(msg); ok {
		return m2, cmd
	}
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		theme.SetDark(msg.IsDark())
		return m, nil
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.relayout()
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg:
		return m.mouse(msg)
	case tui.LaunchTerminalMsg:
		return m.launchTerm(msg)
	case termChangedMsg:
		return m, listenTerms(m.ts.ch)
	case tui.PaneTickMsg:
		if m.anyTermLive() {
			return m, tui.TickPanes()
		}
		m.ts.ticking = false // el siguiente lanzamiento lo vuelve a armar
		return m, nil
	case tea.PasteMsg:
		if m.helpOpen || m.diffShown() {
			return m, nil // con el diff en el centro, el editor no recibe nada
		}
		if m.pal.open {
			m.pal.addText(msg.Content)
			return m, nil
		}
		m.quitArmed, m.pendingOpen = false, ""
		return m.routeToFocused(msg)
	case filetree.OpenFileMsg:
		m.openFile(msg.Path)
		return m, nil
	case filesMsg:
		if !slices.Equal(msg.files, m.files) {
			m.files = msg.files
			if m.pal.open && m.pal.mode == palCommands {
				m.pal.setItems(m.paletteItems())
			}
		}
		return m, nil
	case editorview.SavedMsg:
		if errors.Is(msg.Err, editorview.ErrChangedOnDisk) {
			// No es un fallo: es el aviso previo a sobrescribir.
			m.msg = msg.Err.Error()
		} else if msg.Err != nil {
			m.msg = "no se pudo guardar: " + msg.Err.Error()
		} else {
			m.msg = "guardado " + m.display(msg.Path)
			return m, m.refreshIndex()
		}
		return m, nil
	case indexedMsg:
		m.onIndexed(msg)
		return m, nil
	}
	// Eventos de agentes, spinner, etc.: son del chat aunque no tenga foco.
	var cmd tea.Cmd
	m.chat, cmd = m.chat.Handle(msg)
	m.updateTreeMarkers()
	return m, cmd
}

// is indica si k corresponde a la acción de la configuración.
func (m Model) is(k tea.KeyPressMsg, action string) bool {
	return key.Matches(k, m.binds[action])
}

// firstKey devuelve la primera tecla de una acción ("" si no tiene).
// En macOS (sin alt ni win) devuelve la forma sin alt+ o la secuencia con líder.
func (m Model) firstKey(action string) string {
	return m.km.Label(runtime.GOOS, action)
}

func (m Model) handleKey(k tea.KeyPressMsg) (Model, tea.Cmd) { return m.handleKeyFrom(k, false) }

// handleKeyFrom atiende una tecla; viaLeader indica que es la que siguió a la
// tecla líder (ya convertida en alt+tecla).
func (m Model) handleKeyFrom(k tea.KeyPressMsg, viaLeader bool) (Model, tea.Cmd) {
	m.msg = ""
	if m.helpOpen {
		return m.helpKey(k)
	}
	if m.pal.open {
		return m.paletteKey(k)
	}
	if m.run.confirm != nil {
		return m.runConfirmKey(k)
	}

	// El formulario de agente nuevo está superpuesto: se queda con las teclas.
	if m.chat.FormActive() {
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Handle(k)
		return m, cmd
	}
	if m.ts.mode {
		return m.tileModeKey(k)
	}

	// Tecla líder: la tecla siguiente se trata como si llevara alt, para los
	// teclados sin alt utilizable (macOS) y las terminales que se comen Option.
	if m.leaderArmed {
		m.leaderArmed = false
		if k.String() == "esc" {
			return m, nil
		}
		k.Mod |= tea.ModAlt
		k.Text = ""
		return m.handleKeyFrom(k, true)
	}
	if m.is(k, "leader") {
		m.leaderArmed = true
		m.msg = "líder: pulsa la tecla de la acción (esc cancela)"
		return m, nil
	}

	// Con el foco en una CLI real (terminal), todo es de la CLI: Ctrl+C, Esc,
	// sus atajos y sus comandos "/". Solo se atienden aquí las teclas de pasar
	// el foco a otro panel y la tecla líder (arriba). Para salir de WARI se
	// pasa el foco a otro panel y se usa su tecla de salir.
	if !viaLeader && m.focus == focusTerm && !m.isTileKey(k) {
		return m.routeToFocused(k)
	}

	// Regla de descubribilidad: F1 siempre abre el overlay de ayuda.
	// La tecla '?' solo abre el overlay de ayuda cuando el foco no está escribiendo
	// en el editor (con un archivo abierto) ni en la entrada de texto del chat,
	// evitando interceptar el carácter '?' al redactar código o hacer preguntas.
	isF1 := k.String() == "f1" || k.String() == "F1"
	isQuestion := k.Text == "?" || k.String() == "?"
	writing := (m.focus == focusEditor && m.hasEd && !m.diffShown()) || (m.focus == focusChat && m.chat.Writing())
	if isF1 || (isQuestion && !writing) {
		m.helpOpen = true
		m.helpScroll = 0
		return m, nil
	}

	// Las confirmaciones pendientes (salir, abrir otro archivo) se desarman
	// con cualquier otra tecla. Abrir la paleta no desarma la de salir, para
	// poder confirmarla con "Salir"; las teclas del árbol no desarman la de
	// abrir, porque Enter sobre el archivo es justo la confirmación.
	armed, pending := m.quitArmed, m.pendingOpen
	m.quitArmed, m.pendingOpen = false, ""

	switch {
	case m.is(k, "quit"):
		return m.requestQuit(armed)
	case m.is(k, "palette"):
		m.quitArmed = armed
		return m, m.openPalette()
	case m.is(k, "focus_tree"):
		return m.focusTreePanel(), nil
	case m.is(k, "focus_editor"):
		return m.focusEditorPanel(), nil
	case m.is(k, "focus_agents"):
		return m.focusAgentsPanel(), nil
	case m.is(k, "toggle_tree"):
		m.setTreeOn(!m.treeOn())
		m.relayout()
		return m, nil
	case m.is(k, "toggle_agents"):
		m.setChatOn(!m.chatOn())
		m.relayout()
		if m.chatOn() {
			m = m.focusPanel(focusChat) // al abrirlo se escribe en él
		}
		return m, nil
	case m.is(k, "tile_zoom"):
		return m.tileZoom(), nil
	case m.is(k, "tile_mode"):
		return m.enterTileMode(), nil
	case m.is(k, "tile_left"), m.is(k, "tile_right"), m.is(k, "tile_up"), m.is(k, "tile_down"):
		return m.tileFocusDir(k), nil
	case m.is(k, "agent_new"):
		return m.newAgent()
	case m.is(k, "save"):
		return m.save()
	case m.is(k, "new_file"):
		m.openTemplatePicker()
		return m, nil
	case m.diffShown() && m.focus == focusEditor && (m.is(k, "import_type") || m.is(k, "outline") ||
		m.is(k, "goto_definition") || m.is(k, "complete") || m.is(k, "diagnostics") || m.is(k, "toggle_breakpoint")):
		// El editor no se ve: sus acciones no deben tocar el buffer a ciegas.
		return m, nil
	case m.is(k, "diff_file"):
		return m.openDiff()
	case m.is(k, "import_type"):
		return m.importType()
	case m.focus != focusChat && m.is(k, "outline"):
		return m.showOutline()
	case m.focus != focusChat && m.is(k, "goto_definition"):
		return m.gotoDef(pending)
	case m.focus != focusChat && m.is(k, "complete"):
		return m.complete()
	case m.is(k, "toggle_lsp"):
		return m.toggleLSP()
	case m.focus != focusChat && m.is(k, "diagnostics"):
		return m.showDiagnostics()
	case m.is(k, "focus_dock"):
		return m.focusDockPanel(), nil
	case m.is(k, "run_project"):
		return m.runProject()
	case m.is(k, "run_stop"):
		return m.runStop()
	case m.is(k, "promote_artifact"):
		return m.promoteStart()
	case m.is(k, "toggle_breakpoint"):
		return m.toggleBreakpoint()
	case m.is(k, "debug_start"):
		return m.debugStart()
	case m.is(k, "debug_continue"):
		return m.debugResume("continue")
	case m.is(k, "debug_next"):
		return m.debugResume("next")
	case m.is(k, "debug_step_in"):
		return m.debugResume("in")
	case m.is(k, "debug_step_out"):
		return m.debugResume("out")
	case m.is(k, "debug_stop"):
		return m.debugStop()
	case m.is(k, "stop_all"):
		return m.stopAll()
	case m.chat.Alert() != "" && m.isResolveConflict(k):
		return m.resolveConflict()
	case m.chat.HasPipeline() && (m.is(k, "pipeline_run") || m.is(k, "gate_approve") ||
		m.is(k, "gate_reject") || m.is(k, "gate_edit")):
		return m.pipelineKey(k)
	case m.focus == focusEditor && m.hasEd && m.ed.SnippetActive() &&
		(m.is(k, "focus_prev") || (m.is(k, "focus_next") && key.Matches(k, m.ed.Keys().Tab))):
		// En una sesión de snippet, Tab y Shift+Tab recorren las paradas.
	case m.is(k, "focus_prev"):
		return m.moveFocus(-1), nil
	case m.is(k, "focus_next"):
		// En el editor, la tecla de Tab inserta espacios y no mueve el foco.
		if !(m.focus == focusEditor && m.hasEd && !m.diffShown() && key.Matches(k, m.ed.Keys().Tab)) {
			return m.moveFocus(1), nil
		}
	}
	if m.focus == focusTree {
		m.pendingOpen = pending
	}
	return m.routeToFocused(k)
}

// diffShown indica que el panel central muestra un diff en lugar del editor.
func (m Model) diffShown() bool { return m.chat.DiffOpen() }

// openDiff abre (o pasa al siguiente) el diff de la última edición del agente
// en el panel central y le da el foco, para leerlo con todo el ancho del
// editor. El archivo abierto y sus cambios sin guardar quedan intactos.
func (m Model) openDiff() (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.chat, cmd = m.chat.Diff()
	if !m.chat.DiffOpen() {
		m.msg = "no hay ediciones que mostrar"
		return m, cmd
	}
	m.setFocus(focusEditor)
	m.applyFocus()
	return m, cmd
}

// focusTreePanel da el foco al árbol de archivos, desplegándolo si estaba oculto.
func (m Model) focusTreePanel() Model {
	if !m.treeOn() {
		m.setTreeOn(true)
		m.relayout()
	}
	return m.focusPanel(focusTree)
}

// focusEditorPanel da el foco al editor.
func (m Model) focusEditorPanel() Model {
	return m.focusPanel(focusEditor)
}

// focusAgentsPanel da el foco al panel de agentes, desplegándolo si estaba oculto.
func (m Model) focusAgentsPanel() Model {
	if !m.chatOn() {
		m.setChatOn(true)
		m.relayout()
	}
	return m.focusPanel(focusChat)
}

// focusDockPanel da el foco al dock de salida y depuración, si está abierto.
func (m Model) focusDockPanel() Model {
	if !m.geometry().visible(focusDock, 0) {
		m.msg = "no hay panel de salida ni de depuración abierto"
		return m
	}
	return m.focusPanel(focusDock)
}

// requestQuit sale, salvo que haya cambios sin guardar y no sea la segunda
// petición seguida: entonces solo avisa. Es el único camino a quit, para la
// tecla y para la paleta.
func (m Model) requestQuit(armed bool) (Model, tea.Cmd) {
	if m.hasEd && m.ed.Dirty() && !armed {
		m.quitArmed = true
		m.msg = m.display(m.ed.Path()) + " tiene cambios sin guardar: " + m.firstKey("quit") + " otra vez para salir sin guardar"
		return m, nil
	}
	return m.quit()
}

// routeToFocused entrega una tecla o un pegado al panel con foco.
func (m Model) routeToFocused(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.focus {
	case focusTree:
		m.tree, cmd = m.tree.Update(msg)
	case focusEditor:
		if m.diffShown() {
			// Vista de solo lectura: las teclas son del diff.
			if k, ok := msg.(tea.KeyPressMsg); ok {
				m.chat, cmd = m.chat.DiffKey(k)
			}
			return m, cmd
		}
		if m.hasEd {
			m.ed, cmd = m.ed.Update(msg)
		}
	case focusChat:
		m.chat, cmd = m.chat.Handle(msg)
	case focusTerm:
		if a, _ := m.focusedTerm(); a != nil {
			switch e := msg.(type) {
			case tea.KeyPressMsg:
				a.SendKey(e)
			case tea.PasteMsg:
				a.Paste(e.Content)
			}
		}
	case focusDock:
		if k, ok := msg.(tea.KeyPressMsg); ok {
			return m.dockKey(k)
		}
	}
	return m, cmd
}

// pipelineKey entrega al panel de agentes una tecla del pipeline o del gate,
// con cualquier foco. Editar el prompt necesita escribir en el panel, así que
// lo muestra y le da el foco.
func (m Model) pipelineKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.is(k, "gate_edit") && !m.chatOn() {
		m.setChatOn(true)
		m.relayout()
	}
	m.chat, cmd = m.chat.Handle(k)
	if m.is(k, "gate_edit") {
		m = m.focusPanel(focusChat)
	}
	return m, cmd
}

// stopAll es la parada global de agentes.
func (m Model) stopAll() (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.chat, cmd = m.chat.StopAll()
	m.msg = "parando todos los agentes"
	return m, cmd
}

// resolveConflictKey devuelve la tecla para resolver choques de archivos.
func (m Model) resolveConflictKey() string {
	return "f6"
}

// isResolveConflict indica si la tecla k activa la resolución de choque.
func (m Model) isResolveConflict(k tea.KeyPressMsg) bool {
	s := strings.ToLower(k.String())
	return s == "f6" || s == "alt+c"
}

// resolveConflict despliega el panel de agentes si está oculto, le da el foco
// y activa la resolución asistida del choque en el agente pausado.
func (m Model) resolveConflict() (Model, tea.Cmd) {
	if !m.chatOn() {
		m.setChatOn(true)
		m.relayout()
	}
	m = m.focusPanel(focusChat)
	var cmd tea.Cmd
	m.chat, cmd = m.chat.FocusConflict()
	return m, cmd
}

// save guarda el archivo abierto.
func (m Model) save() (Model, tea.Cmd) {
	if !m.hasEd {
		m.msg = "no hay archivo abierto"
		return m, nil
	}
	// El guardado también lo ve el watcher: se avisa antes para que no se lo
	// atribuya a un agente que esté corriendo.
	m.chat.NoteUserWrite(m.ed.Path())
	cmd := m.ed.Save()
	m.pendingOpen = ""
	return m, cmd
}

// quit sale matando a los agentes, la depuración y la ejecución del sandbox.
func (m Model) quit() (Model, tea.Cmd) {
	// La depuración y la ejecución del sandbox también se cierran: sin
	// huérfanos, ni con la salida por la vía de la paleta.
	m.Shutdown()
	var cmd tea.Cmd
	m.chat, cmd = m.chat.Quit()
	return m, cmd
}

// --- Foco y geometría -------------------------------------------------------

// relayout aplica la geometría a los componentes y al foco.
func (m *Model) relayout() {
	g := m.geometry()
	if !g.visible(m.focus, m.ts.tree.Focus()) {
		// El foco quedó en un tile que no se ve: pasa al editor o, si
		// tampoco se ve, al primer tile visible.
		if m.focus == focusDock {
			m.focus = focusEditor // el dock se cerró
		}
		if g.showEd {
			_ = m.ts.tree.SetFocus(idEditor)
		} else {
			for _, id := range m.ts.tree.IDs() {
				if g.body(id).W > 0 {
					_ = m.ts.tree.SetFocus(id)
					break
				}
			}
		}
		m.syncFocus()
	}
	if g.showTree {
		m.tree.SetSize(g.tree.W, g.tree.H)
	}
	if m.hasEd && g.showEd {
		m.ed.SetSize(g.ed.W, g.ed.H-g.dockH)
	}
	if g.showEd {
		m.chat.SetDiffArea(g.ed.W, g.ed.H-g.dockH)
	}
	if g.showChat {
		m.chat.SetSize(g.chat.W, g.chat.H)
	}
	for id, a := range m.ts.terms {
		if r := g.body(id); r.W > 0 && r.H > 0 {
			a.Resize(r.W, r.H)
		}
	}
	m.applyFocus()
}

// applyFocus da el foco al panel de m.focus y se lo quita a los demás.
func (m *Model) applyFocus() {
	set := func(on bool, focusFn, blurFn func()) {
		if on {
			focusFn()
		} else {
			blurFn()
		}
	}
	set(m.focus == focusTree, m.tree.Focus, m.tree.Blur)
	if m.hasEd {
		set(m.focus == focusEditor || m.focus == focusDock, m.ed.Focus, m.ed.Blur)
	}
	set(m.focus == focusChat, m.chat.Focus, m.chat.Blur)
}

// focusPanel pasa el foco al panel f si se ve.
func (m Model) focusPanel(f focus) Model {
	if f == focusTerm {
		return m
	}
	if m.geometry().visible(f, 0) {
		_ = m.ts.tree.SetFocus(treeFocusFor(f))
		m.focus = f
		m.applyFocus()
	}
	return m
}

// moveFocus avanza (dir > 0) o retrocede el foco entre los tiles visibles
// (el dock cuenta como uno más, tras el editor).
func (m Model) moveFocus(dir int) Model {
	type stop struct {
		f  focus
		id tile.ID
	}
	g := m.geometry()
	var order []stop
	for _, id := range m.ts.tree.IDs() {
		// También los tiles sin sitio (ocultos o tapados por un zoom): si no,
		// un agente que no cabe quedaría vivo e inalcanzable.
		order = append(order, stop{m.kindOf(id), id})
		if id == idEditor && g.dockH > 0 {
			order = append(order, stop{focusDock, idEditor})
		}
	}
	if len(order) == 0 {
		return m
	}
	cur := 0
	for i, st := range order {
		if st.f == m.focus && (st.f != focusTerm || st.id == m.ts.tree.Focus()) {
			cur = i
		}
	}
	next := order[(cur+dir+len(order))%len(order)]
	_ = m.ts.tree.SetFocus(next.id) // con un zoom activo, el zoom lo sigue
	if m.geometry().body(next.id).W == 0 {
		// Sin sitio (y sin zoom que lo traiga): se maximiza para verlo.
		_ = m.ts.tree.Zoom(next.id)
	}
	m.focus = next.f
	m.applyFocus()
	m.relayout()
	return m
}

// --- Archivos ---------------------------------------------------------------

// display devuelve p relativa a la raíz si está dentro de ella.
func (m Model) display(p string) string {
	if rel, err := filepath.Rel(m.root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return p
}

// openFile abre path en el editor. Con cambios sin guardar en otro archivo,
// la primera vez solo avisa; repetir la apertura los descarta.
func (m *Model) openFile(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if m.hasEd && m.ed.Path() == path {
		m.chat = m.chat.CloseDiff()
		m.setFocus(focusEditor)
		m.pendingOpen = ""
		m.relayout()
		return
	}
	if m.hasEd && m.ed.Dirty() && m.pendingOpen != path {
		m.pendingOpen = path
		m.msg = m.display(m.ed.Path()) + " tiene cambios sin guardar: guarda (" + m.firstKey("save") + ") o repite la apertura para descartarlos"
		return
	}
	m.pendingOpen = ""

	if why := notText(path); why != "" {
		m.msg = "no se abre " + m.display(path) + ": " + why
		return
	}
	buf, fi, err := editor.Open(path)
	if err != nil {
		m.msg = "no se pudo abrir: " + err.Error()
		return
	}
	m.later(m.lspCloseDoc())
	m.ed = editorview.New(buf, fi, path, editorview.KeyMapFrom(m.km))
	m.ed.SetSnippets(m.snips)
	m.ed.SetImports(m.allImps)
	m.hasEd = true
	m.chat = m.chat.CloseDiff()
	m.setFocus(focusEditor)
	m.tree.Reveal(path)
	m.relayout()
	m.applyDebugMarks()
	m.later(m.lspOpenDoc())
}

// notText devuelve por qué path no se puede editar como texto ("" si se
// puede): contiene NUL al inicio (binario) o no es UTF-8 válido, en cuyo caso
// guardarlo reemplazaría los bytes inválidos por U+FFFD. Si no se puede leer
// devuelve "" y deja el error a editor.Open.
func notText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if slices.Contains(data[:min(len(data), binarySniff)], 0) {
		return "parece binario"
	}
	if !utf8.Valid(data) {
		return "no es UTF-8 válido"
	}
	return ""
}

// --- Vista ------------------------------------------------------------------

// View implementa tea.Model: AltScreen y cursor real solo aquí.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "WARI"
	v.Cursor = m.cursor()
	return v
}

var (
	headFocused = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12"))
	headBlurred = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).Background(lipgloss.Color("236"))
	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).Background(lipgloss.Color("238"))
	// alertStyle pinta la barra mientras hay un choque de archivos sin resolver.
	alertStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("0")).Background(theme.AdaptiveColor{
		Light: lipgloss.Color("#f59e0b"),
		Dark:  lipgloss.Color("#fbbf24"),
	})
	sepStyle = lipgloss.NewStyle().Faint(true)
)

// fit recorta o rellena s a exactamente w celdas.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) != w {
		s = lipgloss.NewStyle().MaxWidth(w).Render(s)
		if d := w - lipgloss.Width(s); d > 0 {
			s += strings.Repeat(" ", d)
		}
	}
	return s
}

// block deja s en exactamente h líneas de w celdas.
func block(s string, w, h int) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range out {
		if i < len(lines) {
			out[i] = fit(lines[i], w)
		} else {
			out[i] = strings.Repeat(" ", w)
		}
	}
	return out
}

// header dibuja el título de un panel.
func header(title string, focused bool, w int) string {
	st := headBlurred
	if focused {
		st = headFocused
	}
	return st.Render(fit(" "+title, w))
}

// editorTitle es el título del panel del editor.
func (m Model) editorTitle() string {
	if m.diffShown() {
		t := m.chat.DiffTitle()
		if m.hasEd {
			t += " · editor: " + m.display(m.ed.Path())
			if m.ed.Dirty() {
				t += " ●"
			}
		}
		return t
	}
	if !m.hasEd {
		return "Editor"
	}
	t := m.display(m.ed.Path())
	if m.ed.Dirty() {
		t += " ●"
	}
	p := m.ed.Path()
	if h := m.fileHolder(p); h > 0 {
		t += fmt.Sprintf(" · [reclamado por agente %d]", h)
	} else if ed := m.fileEditor(p); ed > 0 {
		t += fmt.Sprintf(" · [editado por agente %d]", ed)
	}
	if m.isFileConflict(p) {
		t += " ⚠ [choque]"
	}
	return t
}

func (m Model) supervisor() *orch.Supervisor {
	if m.sup != nil {
		return m.sup
	}
	return m.chat.Supervisor()
}

func (m Model) fileHolder(path string) orch.ID {
	sup := m.supervisor()
	if sup == nil {
		return 0
	}
	return sup.Holder(path)
}

func (m Model) fileEditor(path string) orch.ID {
	sup := m.supervisor()
	if sup == nil {
		return 0
	}
	rel := path
	if filepath.IsAbs(path) {
		if r, err := filepath.Rel(m.root, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = filepath.ToSlash(r)
		}
	}
	for _, s := range sup.Snapshots() {
		for _, e := range sup.Edits(s.ID) {
			if e.Path == rel || e.Path == path || filepath.Join(m.root, e.Path) == path {
				return s.ID
			}
		}
	}
	return 0
}

func (m Model) isFileConflict(path string) bool {
	if m.chat.Alert() == "" {
		return false
	}
	cp := m.chat.ConflictPath()
	if cp == "" {
		return false
	}
	if cp == path || filepath.ToSlash(cp) == filepath.ToSlash(path) {
		return true
	}
	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(m.root, path); err == nil && (rel == cp || filepath.ToSlash(rel) == filepath.ToSlash(cp)) {
			return true
		}
	}
	if filepath.IsAbs(cp) {
		if rel, err := filepath.Rel(m.root, cp); err == nil && (rel == path || filepath.ToSlash(rel) == filepath.ToSlash(path)) {
			return true
		}
	}
	return false
}

func (m *Model) updateTreeMarkers() {
	markers := make(map[string]filetree.Marker)
	sup := m.supervisor()
	if sup != nil {
		snaps := sup.Snapshots()
		for _, s := range snaps {
			id := int(s.ID)
			for _, rel := range sup.Claims(s.ID) {
				mk := markers[rel]
				mk.Agent = id
				mk.Claimed = true
				markers[rel] = mk
				markers[filepath.ToSlash(rel)] = mk
				if !filepath.IsAbs(rel) {
					markers[filepath.Join(m.root, rel)] = mk
				}
			}
			for _, e := range sup.Edits(s.ID) {
				rel := e.Path
				mk := markers[rel]
				if !mk.Claimed {
					mk.Edited = true
					if mk.Agent == 0 {
						mk.Agent = id
					}
					markers[rel] = mk
					markers[filepath.ToSlash(rel)] = mk
					if !filepath.IsAbs(rel) {
						markers[filepath.Join(m.root, rel)] = mk
					}
				}
			}
		}
	}
	if m.chat.Alert() != "" && m.chat.ConflictPath() != "" {
		cPath := m.chat.ConflictPath()
		cAgent := int(m.chat.ConflictAgent())
		addConflict := func(p string) {
			mk := markers[p]
			mk.Conflict = true
			if mk.Agent == 0 && cAgent > 0 {
				mk.Agent = cAgent
			}
			markers[p] = mk
		}
		addConflict(cPath)
		addConflict(filepath.ToSlash(cPath))
		if filepath.IsAbs(cPath) {
			if rel, err := filepath.Rel(m.root, cPath); err == nil && !strings.HasPrefix(rel, "..") {
				addConflict(rel)
				addConflict(filepath.ToSlash(rel))
			}
		} else {
			addConflict(filepath.Join(m.root, cPath))
		}
	}
	m.tree.SetMarkers(markers)
}

// placeholder es el contenido del editor sin archivo abierto.
func (m Model) placeholder() string {
	lines := []string{"", " Sin archivo abierto."}
	if k := m.firstKey("palette"); k != "" {
		lines = append(lines, " "+k+" abre la paleta (buscar archivo).")
	}
	return lipgloss.NewStyle().Faint(true).Render(strings.Join(lines, "\n"))
}

func (m Model) render() string {
	m.updateTreeMarkers()
	g := m.geometry()
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	rows := m.renderTiles(g)
	if g.statH > 0 {
		rows = append(rows, m.statusBar())
	}
	m.formOverlay(rows)

	if m.pal.open {
		for i, l := range m.pal.view(m.w, len(rows)) {
			rows[i] = l
		}
	}
	if m.helpOpen {
		for i, l := range m.helpView(m.w, len(rows)) {
			rows[i] = l
		}
	}
	return strings.Join(rows, "\n")
}

// agentsInfo es lo que la barra de estado muestra siempre: agentes activos y
// en cola, y el atajo de la parada global.
func (m Model) agentsInfo() string {
	active, queued, limit := m.chat.Counts()
	s := fmt.Sprintf("activos %d/%d · en cola %d", active, limit, queued)
	if k := m.firstKey("stop_all"); k != "" {
		s += " · " + k + " parar todo"
	}
	return s
}

// agentsInfoVariants son las formas de agentsInfo de la más completa a la más
// corta: con poco ancho se prefiere una forma abreviada entera a cortar la
// completa a mitad de palabra ("en co").
func (m Model) agentsInfoVariants() []string {
	active, queued, limit := m.chat.Counts()
	out := []string{m.agentsInfo(), fmt.Sprintf("activos %d/%d · en cola %d", active, limit, queued),
		fmt.Sprintf("act %d/%d · cola %d", active, limit, queued), fmt.Sprintf("%d/%d·%d", active, limit, queued)}
	return out
}

// rightVariants antepone right (con la ejecución y la depuración) a las
// variantes abreviadas de la información de agentes.
func (m Model) rightVariants(right string) []string {
	vs := []string{right}
	return append(vs, m.agentsInfoVariants()[1:]...)
}

// fitVariant devuelve la primera variante que cabe en w celdas y, si ninguna,
// la más corta cortada.
func fitVariant(variants []string, w int) string {
	for _, v := range variants {
		if lipgloss.Width(v) <= w {
			return v
		}
	}
	return variants[len(variants)-1]
}

// statusBar es la última fila: archivo, estado y atajos principales. A la
// derecha va siempre agentsInfo; los demás atajos solo si caben, y si no cabe
// ni eso, el lado izquierdo cede el espacio.
func (m Model) statusBar() string {
	left := " (sin archivo)"
	if m.hasEd {
		left = " " + m.display(m.ed.Path())
		if m.ed.Dirty() {
			left += " ● sin guardar"
		}
	}
	st := statusStyle
	if a := m.chat.Alert(); a != "" {
		// El choque va primero y la barra cambia de color: es lo único que
		// debe verse aunque el panel de agentes esté plegado.
		left += " │ ⚠ " + a + " · [" + m.resolveConflictKey() + " resolver]"
		st = alertStyle
	}
	if m.hasEd && m.focus == focusEditor {
		if typ := m.ed.ImportSuggestion(); typ != "" {
			left += " │ importar " + typ
			if k := m.firstKey("import_type"); k != "" {
				left += " (" + k + ")"
			}
		}
	}
	if d := m.cursorDiagnostic(); d != "" {
		left += " │ " + d
	}
	if m.ts.mode {
		left = " " + tileModeHint // el modo manda: nada más cabe junto a su ayuda
		st = alertStyle
	}
	if m.msg != "" && !m.ts.mode {
		left += " │ " + m.msg
	}
	var hints []string
	if info := m.lspInfo(); info != "" {
		hints = append(hints, info)
	}
	for _, h := range []struct{ action, desc string }{
		{"palette", "paleta"}, {"toggle_tree", "árbol"}, {"toggle_agents", "agentes"}, {"save", "guardar"},
	} {
		if k := m.firstKey(h.action); k != "" {
			hints = append(hints, k+" "+h.desc)
		}
	}
	right := m.agentsInfo()
	for _, info := range []string{m.runInfo(), m.debugInfo()} {
		// La parada de la ejecución y de la depuración se ven siempre, como
		// la de los agentes.
		if info != "" {
			right = info + " · " + right
		}
	}
	// Con espacio, los atajos van pegados a la derecha; se descartan desde el
	// último hasta que quepan. La información de agentes se queda siempre.
	for n := len(hints); n >= 0; n-- {
		r := right
		if n > 0 {
			r = strings.Join(hints[:n], " · ") + " · " + right
		}
		if gap := m.w - lipgloss.Width(left) - lipgloss.Width(r) - 1; gap >= 2 {
			return st.Render(fit(left+strings.Repeat(" ", gap)+r, m.w))
		}
	}
	if m.chat.Alert() != "" {
		lw := lipgloss.Width(left) + 1
		if lw >= m.w {
			return st.Render(fit(left, m.w))
		}
		rem := m.w - lw - 1
		if rem > 0 {
			return st.Render(fit(left+" "+fit(fitVariant(m.rightVariants(right), rem), rem), m.w))
		}
		return st.Render(fit(left, m.w))
	}
	rw := lipgloss.Width(right) + 1
	if rw >= m.w {
		return st.Render(fit(" "+right, m.w))
	}
	return st.Render(fit(fit(left, m.w-rw-1)+" "+right, m.w))
}

// cursor devuelve el cursor del componente con foco en coordenadas absolutas.
func (m Model) cursor() *tea.Cursor {
	if m.w <= 0 || m.h <= 0 {
		return nil
	}
	if m.helpOpen {
		return nil
	}
	if m.pal.open {
		x := lipgloss.Width(m.pal.promptText()) + lipgloss.Width(string(m.pal.query))
		if x >= m.w {
			return nil
		}
		return tea.NewCursor(x, 0)
	}
	g := m.geometry()
	var c *tea.Cursor
	var r tile.Rect
	switch m.focus {
	case focusEditor:
		if m.hasEd && !m.diffShown() {
			c, r = m.ed.Cursor(), g.ed
		}
	case focusChat:
		c, r = m.chat.Cursor(), g.chat
	case focusTerm:
		id := m.ts.tree.Focus()
		if a := m.ts.terms[id]; a != nil {
			if x, y, ok := a.Cursor(); ok {
				c, r = tea.NewCursor(x, y), g.body(id)
			}
		}
	}
	if c == nil {
		return nil
	}
	c.X += r.X
	c.Y += r.Y
	if c.X < r.X || c.X >= r.X+r.W || c.Y < r.Y || c.Y >= r.Y+r.H || c.X >= m.w {
		return nil
	}
	return c
}
