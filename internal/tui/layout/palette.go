package layout

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/tui/filetree"
)

// maxFiles limita los archivos que se listan en la paleta.
const maxFiles = 20000

// maxShown es el máximo de resultados visibles en la paleta.
const maxShown = 10

// Identificadores de las acciones de la paleta; los archivos usan
// "open:<ruta relativa>".
const (
	cmdSave            = "save"
	cmdToggleTree      = "toggle_tree"
	cmdToggleAgents    = "toggle_agents"
	cmdNewSession      = "new_session"
	cmdAgentPause      = "agent_pause"
	cmdAgentStop       = "agent_stop"
	cmdStopAll         = "stop_all"
	cmdDiffFile        = "diff_file"
	cmdRevertFile      = "revert_file"
	cmdRevertTurn      = "revert_turn"
	cmdRevertAgent     = "revert_agent"
	cmdFocusTree       = "focus_tree"
	cmdFocusEditor     = "focus_editor"
	cmdFocusChat       = "focus_chat"
	cmdFocusDock       = "focus_dock"
	cmdHelp            = "help"
	cmdNewFile         = "new_file"
	cmdImportType      = "import_type"
	cmdOutline         = "outline"
	cmdGotoDef         = "goto_definition"
	cmdComplete        = "complete"
	cmdToggleLSP       = "toggle_lsp"
	cmdDiagnostics     = "diagnostics"
	cmdRunProject      = "run_project"
	cmdRunStop         = "run_stop"
	cmdPromote         = "promote_artifact"
	cmdToggleBP        = "toggle_breakpoint"
	cmdDebugStart      = "debug_start"
	cmdDebugCont       = "debug_continue"
	cmdDebugNext       = "debug_next"
	cmdDebugIn         = "debug_step_in"
	cmdDebugOut        = "debug_step_out"
	cmdDebugStop       = "debug_stop"
	cmdCloseDock       = "close_dock"
	cmdResolveConflict = "resolve_conflict"
	cmdQuit            = "quit"
	cmdOpenPrefix      = "open:"
)

// item es una entrada de la paleta.
type item struct {
	id     string
	title  string
	hint   string // atajo o etiqueta, a la derecha
	folded []rune // title plegado con fold, calculado una vez
}

func newItem(id, title, hint string) item {
	return item{id: id, title: title, hint: hint, folded: fold(title)}
}

// palette es el estado de la paleta de comandos.
type palette struct {
	open    bool
	mode    palMode
	prompt  string // vacío: "> "
	note    string // aviso bajo la entrada en los modos de texto
	query   []rune
	items   []item
	results []int // índices en items, ya filtrados y ordenados
	sel     int
	top     int
}

// palKeys son las teclas propias de la edición de la paleta; las demás
// (cancelar, aceptar, paginar) salen de la configuración.
var palKeys = struct{ up, down, back key.Binding }{
	up:   key.NewBinding(key.WithKeys("up")),
	down: key.NewBinding(key.WithKeys("down")),
	back: key.NewBinding(key.WithKeys("backspace")),
}

// paletteItems arma las entradas de la paleta: las acciones y los archivos
// de la última lista conocida.
func (m Model) paletteItems() []item {
	hint := func(action string) string { return m.firstKey(action) }
	var items []item
	if m.chat.Alert() != "" {
		items = append(items, newItem(cmdResolveConflict, "Resolver choque de archivos", m.resolveConflictKey()))
	}
	items = append(items,
		newItem(cmdSave, "Guardar archivo", hint("save")),
		newItem(cmdToggleTree, "Alternar árbol de archivos", hint("toggle_tree")),
		newItem(cmdToggleAgents, "Alternar panel de agentes", hint("toggle_agents")),
		newItem(cmdNewSession, "Agente nuevo", hint("agent_new")),
		newItem(cmdStopAll, "Detener todos los agentes", hint("stop_all")),
		newItem(cmdFocusTree, "Ir al árbol de archivos", hint("focus_tree")),
		newItem(cmdFocusEditor, "Ir al editor", hint("focus_editor")),
		newItem(cmdFocusChat, "Ir al panel de agentes", hint("focus_agents")),
		newItem(cmdFocusDock, "Ir al panel de salida y depuración (pila y variables)", hint("focus_dock")),
		newItem(cmdQuit, "Salir", hint("quit")),
		newItem(cmdAgentPause, "Pausar o reanudar agente", hint("agent_pause")),
		newItem(cmdAgentStop, "Detener agente", hint("agent_stop")),
		newItem(cmdDiffFile, "Ver diff", hint("diff_file")),
		newItem(cmdRevertFile, "Revertir archivo", hint("revert_file")),
		newItem(cmdRevertTurn, "Revertir turno", hint("revert_turn")),
		newItem(cmdRevertAgent, "Revertir agente", hint("revert_agent")),
		newItem(cmdNewFile, "Nuevo archivo desde plantilla", hint("new_file")),
		newItem(cmdImportType, "Importar el tipo bajo el cursor", hint("import_type")),
		newItem(cmdOutline, "Outline del archivo (clases y métodos)", hint("outline")),
		newItem(cmdGotoDef, "Ir a la definición", hint("goto_definition")),
		newItem(cmdComplete, "Completar (LSP si está activo; si no, nombres del proyecto)", hint("complete")),
		newItem(cmdToggleLSP, lspToggleTitle(m.lsp.on), hint("toggle_lsp")),
		newItem(cmdDiagnostics, "Diagnósticos del LSP del archivo", hint("diagnostics")),
		newItem(cmdRunProject, "Ejecutar el proyecto en el sandbox", hint("run_project")),
		newItem(cmdRunStop, "Detener la ejecución del sandbox", hint("run_stop")),
		newItem(cmdPromote, "Promover un artefacto de la ejecución al proyecto", hint("promote_artifact")),
		newItem(cmdToggleBP, "Poner o quitar breakpoint en la línea", hint("toggle_breakpoint")),
		newItem(cmdDebugStart, "Depurar: iniciar (o continuar si está detenido)", hint("debug_start")),
		newItem(cmdDebugCont, "Depurar: continuar", hint("debug_continue")),
		newItem(cmdDebugNext, "Depurar: siguiente línea (next)", hint("debug_next")),
		newItem(cmdDebugIn, "Depurar: entrar (step in)", hint("debug_step_in")),
		newItem(cmdDebugOut, "Depurar: salir (step out)", hint("debug_step_out")),
		newItem(cmdDebugStop, "Depurar: detener", hint("debug_stop")),
		newItem(cmdCloseDock, "Cerrar el panel de salida y depuración", ""),
	)
	for _, f := range m.files {
		items = append(items, newItem(cmdOpenPrefix+f, f, "archivo"))
	}
	return items
}

// openPalette abre la paleta al instante con las acciones y los archivos que
// ya se conocen, y devuelve un Cmd que recorre el proyecto en segundo plano;
// su resultado (filesMsg) actualiza la lista sin bloquear Update.
func (m *Model) openPalette() tea.Cmd {
	m.pal = palette{open: true}
	m.pal.setItems(m.paletteItems())
	root := m.root
	return tea.Batch(func() tea.Msg {
		return filesMsg{files: filetree.ListFiles(root, maxFiles)}
	}, m.refreshIndex())
}

// setItems reemplaza las entradas y vuelve a filtrar con la consulta actual.
func (p *palette) setItems(items []item) {
	p.items = items
	p.refilter()
}

// refilter recalcula los resultados para la consulta actual.
func (p *palette) refilter() {
	q := fold(string(p.query))
	type scored struct{ idx, score int }
	var res []scored
	for i, it := range p.items {
		if s, ok := fuzzyScoreFolded(q, it.folded); ok {
			res = append(res, scored{i, s})
		}
	}
	if len(q) > 0 {
		sort.SliceStable(res, func(a, b int) bool { return res[a].score > res[b].score })
	}
	p.results = p.results[:0]
	for _, r := range res {
		p.results = append(p.results, r.idx)
	}
	p.sel, p.top = 0, 0
}

// move mueve la selección delta posiciones, sin salirse de la lista.
func (p *palette) move(delta int) {
	if len(p.results) == 0 {
		return
	}
	p.sel = max(0, min(p.sel+delta, len(p.results)-1))
	if p.sel < p.top {
		p.top = p.sel
	}
	if p.sel >= p.top+maxShown {
		p.top = p.sel - maxShown + 1
	}
}

// paletteKey procesa una tecla con la paleta abierta.
func (m Model) paletteKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	p := &m.pal
	switch {
	case m.is(k, "cancel"), m.is(k, "palette"):
		p.open = false
		m.quitArmed = false
	case m.is(k, "send"):
		switch p.mode {
		case palTemplates:
			return m.pickTemplate()
		case palPath:
			return m.submitPath()
		case palVar:
			return m.submitVar()
		case palImports:
			return m.pickImport()
		case palOutline:
			return m.pickOutline()
		case palDefs:
			return m.pickDefinition()
		case palComplete:
			return m.pickCompletion()
		case palLSPComplete:
			return m.pickLSPCompletion()
		case palLSPDefs:
			return m.pickLSPDefinition()
		case palDiags:
			return m.pickDiagnostic()
		case palPromote:
			return m.submitPromote()
		}
		if len(p.results) == 0 {
			return m, nil
		}
		id := p.items[p.results[p.sel]].id
		p.open = false
		return m.runCommand(id)
	case key.Matches(k, palKeys.up):
		p.move(-1)
	case key.Matches(k, palKeys.down):
		p.move(1)
	case m.is(k, "page_up"):
		p.move(-maxShown)
	case m.is(k, "page_down"):
		p.move(maxShown)
	case key.Matches(k, palKeys.back):
		if n := len(p.query); n > 0 {
			p.query = p.query[:n-1]
			p.refilter()
		}
	default:
		// Texto imprimible con cualquier modificador: en Windows, AltGr
		// llega como Text con ctrl+alt ("@", "{", "[").
		if k.Text != "" {
			p.addText(k.Text)
		}
	}
	return m, nil
}

// addText agrega texto imprimible (una sola línea) a la consulta.
func (p *palette) addText(s string) {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	changed := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			continue
		}
		p.query = append(p.query, r)
		changed = true
	}
	if changed {
		p.refilter()
	}
}

// runCommand ejecuta una acción de la paleta.
func (m Model) runCommand(id string) (Model, tea.Cmd) {
	armed := m.quitArmed
	m.quitArmed = false
	switch {
	case id == cmdSave:
		return m.save()
	case id == cmdResolveConflict:
		return m.resolveConflict()
	case id == cmdToggleTree:
		m.treeOn = !m.treeOn
		m.relayout()
	case id == cmdToggleAgents:
		m.chatOn = !m.chatOn
		m.relayout()
	case id == cmdNewSession:
		var cmd tea.Cmd
		m.chat, cmd = m.chat.NewSession()
		return m, cmd
	case id == cmdAgentPause:
		if !m.chatOn {
			m.chatOn = true
			m.relayout()
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Pause()
		return m, cmd
	case id == cmdAgentStop:
		if !m.chatOn {
			m.chatOn = true
			m.relayout()
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Stop()
		return m, cmd
	case id == cmdDiffFile:
		if !m.chatOn {
			m.chatOn = true
			m.relayout()
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Diff()
		return m, cmd
	case id == cmdRevertFile:
		if !m.chatOn {
			m.chatOn = true
			m.relayout()
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.RevertFile()
		return m, cmd
	case id == cmdRevertTurn:
		if !m.chatOn {
			m.chatOn = true
			m.relayout()
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.RevertTurn()
		return m, cmd
	case id == cmdRevertAgent:
		if !m.chatOn {
			m.chatOn = true
			m.relayout()
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.RevertAgent()
		return m, cmd
	case id == cmdStopAll:
		return m.stopAll()
	case id == cmdNewFile:
		m.openTemplatePicker()
		return m, nil
	case id == cmdImportType:
		return m.importType()
	case id == cmdOutline:
		return m.showOutline()
	case id == cmdGotoDef:
		return m.gotoDef("")
	case id == cmdComplete:
		return m.complete()
	case id == cmdToggleLSP:
		return m.toggleLSP()
	case id == cmdDiagnostics:
		return m.showDiagnostics()
	case id == cmdRunProject:
		return m.runProject()
	case id == cmdRunStop:
		return m.runStop()
	case id == cmdPromote:
		return m.promoteStart()
	case id == cmdToggleBP:
		return m.toggleBreakpoint()
	case id == cmdDebugStart:
		return m.debugStart()
	case id == cmdDebugCont:
		return m.debugResume("continue")
	case id == cmdDebugNext:
		return m.debugResume("next")
	case id == cmdDebugIn:
		return m.debugResume("in")
	case id == cmdDebugOut:
		return m.debugResume("out")
	case id == cmdDebugStop:
		return m.debugStop()
	case id == cmdCloseDock:
		if m.busyDock() {
			m.msg = "hay una ejecución o una depuración en curso: deténla antes de cerrar el panel"
		} else {
			m.closeDock()
		}
		return m, nil
	case id == cmdQuit:
		return m.requestQuit(armed)
	case id == cmdFocusTree:
		return m.focusTreePanel(), nil
	case id == cmdFocusEditor:
		return m.focusEditorPanel(), nil
	case id == cmdFocusChat:
		return m.focusAgentsPanel(), nil
	case id == cmdFocusDock:
		return m.focusDockPanel(), nil
	case id == cmdHelp:
		m.helpOpen = true
		m.helpScroll = 0
		return m, nil
	case strings.HasPrefix(id, cmdOpenPrefix):
		m.openFile(filepath.Join(m.root, filepath.FromSlash(strings.TrimPrefix(id, cmdOpenPrefix))))
	}
	return m, nil
}

// view dibuja la paleta como una franja de n líneas de w celdas que cubre la
// parte superior de la pantalla. Devuelve también la altura máxima disponible
// ya aplicada.
func (p palette) view(w, maxH int) []string {
	bar := lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("252"))
	selSt := lipgloss.NewStyle().Reverse(true)

	lines := []string{bar.Bold(true).Render(fit(p.promptText()+string(p.query), w))}
	if p.mode >= palPath {
		// Entrada de texto: sin lista, solo la ayuda o el último error.
		note := p.note
		if note == "" {
			note = "enter acepta · esc cancela"
		}
		lines = append(lines, bar.Faint(true).Render(fit("  "+note, w)))
		if len(lines) > maxH {
			lines = lines[:maxH]
		}
		return lines
	}
	shown := min(maxShown, maxH-1)
	if len(p.results) == 0 && shown > 0 {
		lines = append(lines, bar.Faint(true).Render(fit("  sin coincidencias", w)))
	}
	for i := 0; i < shown && p.top+i < len(p.results); i++ {
		idx := p.top + i
		it := p.items[p.results[idx]]
		text := "  " + it.title
		if it.hint != "" {
			if gap := w - lipgloss.Width(text) - lipgloss.Width(it.hint) - 2; gap >= 2 {
				text += strings.Repeat(" ", gap) + it.hint
			}
		}
		text = fit(text, w)
		if idx == p.sel {
			lines = append(lines, selSt.Render(text))
		} else {
			lines = append(lines, bar.Render(text))
		}
	}
	if len(lines) > maxH {
		lines = lines[:maxH]
	}
	return lines
}
