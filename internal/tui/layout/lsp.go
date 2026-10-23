package layout

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/lsp"
	"github.com/dpa23/wari/internal/tui/editorview"
)

// LSP opcional (fase 12). Apagado por omisión: sin él, todo lo de index.go
// funciona igual. Con toggle_lsp (o -lsp), cada archivo .java, .kt o .cs cuyo
// servidor esté en el PATH se abre en él: el gestor (internal/lsp) lo arranca
// la primera vez, bajo demanda; si no está instalado, la barra lo dice y se
// sigue con el índice propio.
//
//   - Sincronización: didOpen al abrir, didChange tras las ediciones (con un
//     debounce de lspSyncDelay) y didClose al cambiar de archivo.
//   - complete: si el servidor responde dentro de lspRequestTimeout, ofrece lo
//     suyo (los métodos del objeto tras '.'); si no, el índice propio.
//     goto_definition: el servidor primero, el índice de respaldo.
//   - Diagnósticos: puntos en el margen, el mensaje de la línea del cursor en
//     la barra y la lista navegable de la acción diagnostics.
//   - Barra: "LSP jdtls · 512 MB" con la memoria del gestor, refrescada cada
//     pocos segundos fuera de Update.
//
// Nada de esto bloquea Update: las peticiones corren en tea.Cmd y su
// resultado vuelve como mensaje. Cada resultado lleva la generación del
// documento para descartar lo que llega tarde (otro archivo, LSP apagado).
// Al salir, quien creó el gestor llama a Manager.Shutdown (cmd/wari).

// lspStartTimeout acota la espera del arranque de un servidor.
const lspStartTimeout = 3 * time.Minute

// Plazos del LSP; variables solo para que los tests los acorten.
var (
	// lspSyncDelay es la quietud tras la última tecla antes de enviar didChange.
	lspSyncDelay = 300 * time.Millisecond
	// lspRequestTimeout es lo que espera complete o goto_definition al servidor
	// antes de caer al índice propio.
	lspRequestTimeout = 800 * time.Millisecond
	// lspStatusEvery es cada cuánto se lee la memoria del gestor.
	lspStatusEvery = 3 * time.Second
)

const (
	cmdLSPCompletePrefix = "lc:"
	cmdLSPDefPrefix      = "ld:"
	cmdDiagPrefix        = "dg:"
)

// docSync ordena los didChange de un documento. Los envíos salen de
// goroutines distintas (el debounce y las peticiones); cada uno lleva el
// número de versión de texto que le asignó Update, y uno más viejo que lo ya
// enviado se descarta, así el servidor nunca ve texto viejo después del nuevo.
type docSync struct {
	mu   sync.Mutex
	sent int
}

func (d *docSync) send(c *lsp.Client, uri string, seq int, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if seq <= d.sent {
		return nil
	}
	d.sent = seq
	return c.DidChange(uri, text)
}

// lspDoc es el documento abierto en el servidor.
type lspDoc struct {
	path string
	uri  string
	def  lsp.ServerDef
	// client es nil mientras el servidor arranca o si no hay servidor.
	client   *lsp.Client
	starting bool
	// err explica por qué no hay servidor ("no instalado", fallo de arranque).
	err string
	// synced es el texto que el servidor tiene; seq, su versión de texto.
	synced string
	seq    int
	snd    *docSync
	// diags llega del servidor; cancel cierra la suscripción.
	diagCh <-chan []lsp.Diagnostic
	cancel func()
}

// lspState es todo lo del LSP que recuerda el layout.
type lspState struct {
	// mgr es el gestor de servidores; nil si el LSP no está disponible.
	mgr *lsp.Manager
	on  bool
	doc lspDoc
	// gen identifica al documento actual; edits cuenta las teclas para el
	// debounce; statusGen identifica el bucle de la barra.
	gen, edits, statusGen int
	diags                 []lsp.Diagnostic
	servers               []lsp.Status

	// Listas que muestra la paleta.
	items []lsp.CompletionItem
	locs  []lsp.Location
	dlist []lsp.Diagnostic
}

// Mensajes del LSP.
type (
	lspBootMsg  struct{}
	lspReadyMsg struct {
		gen    int
		uri    string
		client *lsp.Client
		err    error
	}
	lspTickMsg struct{ gen, edits int }
	lspDiagMsg struct {
		gen   int
		diags []lsp.Diagnostic
	}
	lspDiagEndMsg struct{}
	lspStatusMsg  struct {
		gen     int
		servers []lsp.Status
	}
	lspCompleteMsg struct {
		gen       int
		line, col int
		items     []lsp.CompletionItem
		err       error
	}
	lspDefMsg struct {
		gen     int
		pending string
		locs    []lsp.Location
		err     error
	}
	lspStoppedMsg struct{ err error }
)

// later encola un Cmd para el final de este update (openFile no devuelve
// Cmd: lo usa para cerrar y abrir documentos en el servidor).
func (m *Model) later(c tea.Cmd) {
	if c != nil {
		m.xcmd = tea.Batch(m.xcmd, c)
	}
}

// --- Documento ------------------------------------------------------------------

// lspOpenDoc abre en el servidor el archivo del editor. Se llama con el LSP
// encendido y el editor ya apuntando al archivo. Sin servidor para el tipo de
// archivo, o sin él instalado, avisa y no hace nada más.
func (m *Model) lspOpenDoc() tea.Cmd {
	if !m.lsp.on || m.lsp.mgr == nil || !m.hasEd {
		return nil
	}
	path := m.ed.Path()
	m.lsp.gen++
	m.lsp.doc = lspDoc{path: path}
	def, ok := lsp.ServerForPath(path)
	if !ok {
		m.msg = "LSP: sin servidor para este tipo de archivo (Java, Kotlin y C#); se usa el índice propio"
		return nil
	}
	m.lsp.doc.def = def
	if _, err := def.Detect(); err != nil {
		m.lsp.doc.err = "no instalado"
		m.msg = def.Name + " no está instalado; se sigue con el índice propio"
		return nil
	}
	text := m.ed.Text()
	d := &m.lsp.doc
	d.uri, d.starting, d.synced, d.snd = lsp.FileURI(path), true, text, &docSync{}
	m.msg = "LSP: iniciando " + def.Name + "…"

	mgr, root, gen, uri := m.lsp.mgr, m.root, m.lsp.gen, d.uri
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), lspStartTimeout)
		defer cancel()
		c, err := mgr.Get(ctx, def, root)
		if err == nil {
			err = c.DidOpen(uri, def.LanguageID, text)
		}
		if err != nil {
			return lspReadyMsg{gen: gen, uri: uri, err: err}
		}
		return lspReadyMsg{gen: gen, uri: uri, client: c}
	}
}

// onLSPReady incorpora el resultado del arranque del servidor.
func (m *Model) onLSPReady(msg lspReadyMsg) tea.Cmd {
	if msg.gen != m.lsp.gen || !m.lsp.on {
		// Llegó tarde: el documento ya no es el actual. Si quedó abierto en
		// el servidor, se cierra.
		if msg.client != nil {
			c, uri := msg.client, msg.uri
			return func() tea.Msg { _ = c.DidClose(uri); return nil }
		}
		return nil
	}
	d := &m.lsp.doc
	d.starting = false
	if msg.err != nil {
		d.err = "error"
		m.msg = "LSP " + d.def.Name + ": " + msg.err.Error() + "; se usa el índice propio"
		return nil
	}
	d.client = msg.client
	m.msg = "LSP " + d.def.Name + " listo"
	ch, cancel := msg.client.SubscribeDiagnostics(d.uri)
	d.diagCh, d.cancel = ch, cancel
	return tea.Batch(
		m.lspWaitDiag(),
		m.lspSyncNow(),
		m.lspStatusCmd(0),
	)
}

// lspCloseDoc cierra el documento actual en el servidor (didClose) y limpia
// sus diagnósticos. Seguro sin documento.
func (m *Model) lspCloseDoc() tea.Cmd {
	d := m.lsp.doc
	m.lsp.doc = lspDoc{}
	m.lsp.gen++
	m.lsp.diags = nil
	if d.cancel != nil {
		d.cancel()
	}
	if d.client == nil {
		return nil
	}
	c, uri := d.client, d.uri
	return func() tea.Msg { _ = c.DidClose(uri); return nil }
}

// usable indica que el documento actual está abierto en un servidor vivo y
// ok(capacidades) lo permite.
func (m Model) lspUsable(ok func(lsp.ServerCapabilities) bool) bool {
	d := m.lsp.doc
	return m.lsp.on && m.hasEd && d.client != nil && d.path == m.ed.Path() &&
		d.client.Err() == nil && ok(d.client.Capabilities())
}

// lspStage decide si hay que enviar el texto antes de una petición: devuelve
// su versión y el texto si cambió desde el último envío.
func (m *Model) lspStage() (seq int, text string, need bool) {
	d := &m.lsp.doc
	text = m.ed.Text()
	if text == d.synced {
		return 0, "", false
	}
	d.synced = text
	d.seq++
	return d.seq, text, true
}

// lspSyncNow envía el texto actual si el servidor no lo tiene.
func (m *Model) lspSyncNow() tea.Cmd {
	d := m.lsp.doc
	if d.client == nil || d.path != m.ed.Path() {
		return nil
	}
	seq, text, need := m.lspStage()
	if !need {
		return nil
	}
	c, uri, snd := d.client, d.uri, d.snd
	return func() tea.Msg { _ = snd.send(c, uri, seq, text); return nil }
}

// lspEdited arma el debounce tras una tecla o un pegado.
func (m *Model) lspEdited() tea.Cmd {
	if !m.lsp.on || m.lsp.doc.client == nil {
		return nil
	}
	m.lsp.edits++
	msg := lspTickMsg{gen: m.lsp.gen, edits: m.lsp.edits}
	return tea.Tick(lspSyncDelay, func(time.Time) tea.Msg { return msg })
}

// --- Diagnósticos ---------------------------------------------------------------

// lspWaitDiag espera la siguiente publicación de diagnósticos.
func (m Model) lspWaitDiag() tea.Cmd {
	ch, gen := m.lsp.doc.diagCh, m.lsp.gen
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		d, ok := <-ch
		if !ok {
			return lspDiagEndMsg{}
		}
		return lspDiagMsg{gen: gen, diags: d}
	}
}

// onDiagnostics guarda una publicación y marca el margen del editor.
func (m *Model) onDiagnostics(msg lspDiagMsg) tea.Cmd {
	if msg.gen != m.lsp.gen {
		return nil
	}
	m.lsp.diags = msg.diags
	m.applyMarks()
	return m.lspWaitDiag()
}

// applyMarks pone en el margen una marca por línea con error o aviso.
func (m *Model) applyMarks() {
	if !m.hasEd {
		return
	}
	var marks map[int]editorview.LineMark
	for _, d := range m.lsp.diags {
		mk := markOf(d)
		if mk == editorview.MarkNone {
			continue
		}
		if marks == nil {
			marks = map[int]editorview.LineMark{}
		}
		if mk > marks[d.Range.Start.Line] {
			marks[d.Range.Start.Line] = mk
		}
	}
	m.ed.SetLineMarks(marks)
}

// markOf traduce la gravedad de un diagnóstico; las informaciones y las
// pistas no marcan el margen. Sin gravedad, el servidor deja que el cliente
// decida: se trata como error.
func markOf(d lsp.Diagnostic) editorview.LineMark {
	switch d.Severity {
	case 0, lsp.SeverityError:
		return editorview.MarkError
	case lsp.SeverityWarning:
		return editorview.MarkWarning
	}
	return editorview.MarkNone
}

// cursorDiagnostic es el mensaje del diagnóstico más grave de la línea del
// cursor, con su símbolo ("" si no hay).
func (m Model) cursorDiagnostic() string {
	if !m.lsp.on || !m.hasEd {
		return ""
	}
	line := m.ed.Buffer().Cursor().Line
	var best *lsp.Diagnostic
	rank := func(d lsp.Diagnostic) int {
		r := int(markOf(d)) * 2
		if d.Range.Start.Line == line {
			r++ // el que empieza en la línea gana al que solo la cubre
		}
		return r
	}
	for i := range m.lsp.diags {
		d := m.lsp.diags[i]
		if markOf(d) == editorview.MarkNone || line < d.Range.Start.Line || line > d.Range.End.Line {
			continue
		}
		if best == nil || rank(d) > rank(*best) {
			best = &m.lsp.diags[i]
		}
	}
	if best == nil {
		return ""
	}
	return diagSymbol(*best) + " " + firstLine(best.Message)
}

func diagSymbol(d lsp.Diagnostic) string {
	if markOf(d) == editorview.MarkWarning {
		return "⚠"
	}
	return "✖"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// showDiagnostics abre la lista de diagnósticos del archivo actual.
func (m Model) showDiagnostics() (Model, tea.Cmd) {
	switch {
	case !m.lsp.on:
		m.msg = "LSP apagado: " + m.keyHint("toggle_lsp", "enciéndelo con") + " para ver diagnósticos"
		return m, nil
	case !m.hasEd:
		m.msg = "no hay archivo abierto"
		return m, nil
	case len(m.lsp.diags) == 0:
		m.msg = "sin diagnósticos"
		return m, nil
	}
	list := slices.Clone(m.lsp.diags)
	slices.SortStableFunc(list, func(a, b lsp.Diagnostic) int {
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line - b.Range.Start.Line
		}
		return a.Range.Start.Character - b.Range.Start.Character
	})
	m.lsp.dlist = list
	m.pal = palette{open: true, mode: palDiags, prompt: "Diagnósticos: "}
	items := make([]item, 0, len(list))
	cur := m.ed.Buffer().Cursor().Line
	first := -1
	for i, d := range list {
		title := fmt.Sprintf("%s %d: %s", diagSymbol(d), d.Range.Start.Line+1, firstLine(d.Message))
		items = append(items, newItem(fmt.Sprintf("%s%d", cmdDiagPrefix, i), title, d.Source))
		if first < 0 && d.Range.Start.Line >= cur {
			first = i
		}
	}
	m.pal.setItems(items)
	if first > 0 {
		m.pal.sel = first
		m.pal.top = max(0, first-maxShown+1)
	}
	return m, nil
}

// keyHint devuelve "<prefijo> <tecla>" (o solo la acción si no tiene tecla).
func (m Model) keyHint(action, prefix string) string {
	if k := m.firstKey(action); k != "" {
		return prefix + " " + k
	}
	return prefix + " la acción " + action
}

// pickDiagnostic salta a la línea del diagnóstico elegido.
func (m Model) pickDiagnostic() (Model, tea.Cmd) {
	idx, ok := m.pickID(cmdDiagPrefix)
	if !ok || idx >= len(m.lsp.dlist) {
		return m, nil
	}
	p := m.lsp.dlist[idx].Range.Start
	m.pal = palette{}
	if m.hasEd {
		line, col := m.bufPos(p)
		m.ed.GoTo(line, col)
		m = m.focusPanel(focusEditor)
		m.msg = firstLine(m.lsp.dlist[idx].Message)
	}
	return m, nil
}

// --- Posiciones -----------------------------------------------------------------

// lspPosAt convierte una posición del buffer (línea y columna en runas) en
// posición LSP (columna en unidades UTF-16).
func (m Model) lspPosAt(line, col int) lsp.Position {
	runes := m.ed.Buffer().Line(line)
	col = max(0, min(col, len(runes)))
	return lsp.Position{
		Line:      line,
		Character: lsp.ByteToUTF16(string(runes), len(string(runes[:col]))),
	}
}

// bufPos es la inversa de lspPosAt, acotada al buffer.
func (m Model) bufPos(p lsp.Position) (line, col int) {
	b := m.ed.Buffer()
	line = max(0, min(p.Line, b.LineCount()-1))
	s := string(b.Line(line))
	return line, utf8.RuneCountInString(s[:lsp.UTF16ToByte(s, p.Character)])
}

// --- Completar ------------------------------------------------------------------

// complete es la acción complete: el servidor si responde a tiempo; si no,
// los nombres del proyecto.
func (m Model) complete() (Model, tea.Cmd) {
	if cmd := m.lspComplete(); cmd != nil {
		m.msg = "LSP: consultando…"
		return m, cmd
	}
	return m.completeName()
}

// lspComplete pide las sugerencias al servidor; nil si no puede atender.
func (m *Model) lspComplete() tea.Cmd {
	if !m.lspUsable(func(c lsp.ServerCapabilities) bool { return c.Completion }) {
		return nil
	}
	cur := m.ed.Buffer().Cursor()
	pos := m.lspPosAt(cur.Line, cur.Col)
	seq, text, need := m.lspStage()
	d := m.lsp.doc
	c, uri, snd, gen := d.client, d.uri, d.snd, m.lsp.gen
	return func() tea.Msg {
		if need {
			_ = snd.send(c, uri, seq, text)
		}
		ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
		defer cancel()
		items, err := c.Complete(ctx, uri, pos)
		return lspCompleteMsg{gen: gen, line: cur.Line, col: cur.Col, items: items, err: err}
	}
}

// onLSPComplete muestra lo que respondió el servidor o, si no respondió a
// tiempo o no tenía nada, cae al índice propio.
func (m Model) onLSPComplete(msg lspCompleteMsg) (Model, tea.Cmd) {
	cur := m.ed.Buffer().Cursor()
	if msg.gen != m.lsp.gen || !m.hasEd || m.pal.open || cur.Line != msg.line || cur.Col != msg.col {
		return m, nil // el usuario siguió escribiendo: la respuesta ya no sirve
	}
	if msg.err != nil || len(msg.items) == 0 {
		why := "LSP sin sugerencias"
		if msg.err != nil {
			why = "LSP no respondió"
		}
		m, cmd := m.completeName()
		note := why + ", índice propio"
		if m.msg != "" {
			note += ": " + m.msg
		}
		m.msg = note
		return m, cmd
	}
	m.showLSPCompletions(msg.items)
	return m, nil
}

// showLSPCompletions lista las sugerencias del servidor en la paleta, con lo
// ya escrito de la palabra como filtro; una sola se inserta sin lista.
func (m *Model) showLSPCompletions(items []lsp.CompletionItem) {
	items = slices.Clone(items)
	slices.SortStableFunc(items, func(a, b lsp.CompletionItem) int {
		return strings.Compare(orStr(a.SortText, a.Label), orStr(b.SortText, b.Label))
	})
	if len(items) > maxCompletions {
		items = items[:maxCompletions]
	}
	if len(items) == 1 {
		m.applyLSPCompletion(items[0])
		return
	}
	m.lsp.items = items
	prefix := m.ed.WordPrefix()
	m.pal = palette{open: true, mode: palLSPComplete, prompt: "Completar (LSP): ", query: []rune(prefix)}
	pi := make([]item, 0, len(items))
	for i, it := range items {
		hint := completionKind(it.Kind)
		if it.Detail != "" {
			hint += " · " + truncate(it.Detail, 40)
		}
		pi = append(pi, newItem(fmt.Sprintf("%s%d", cmdLSPCompletePrefix, i), it.Label, hint))
	}
	m.pal.setItems(pi)
	if len(m.pal.results) == 0 && prefix != "" {
		// El servidor no filtró por el prefijo y nada coincide: se ve todo.
		m.pal.query = nil
		m.pal.refilter()
	}
}

// orStr devuelve a, o b si a está vacío.
func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// completionKind es el nombre corto de un CompletionItemKind de LSP.
func completionKind(k int) string {
	switch k {
	case 2:
		return "método"
	case 3:
		return "función"
	case 4:
		return "ctor"
	case 5:
		return "campo"
	case 6:
		return "variable"
	case 7:
		return "clase"
	case 8:
		return "interface"
	case 9:
		return "módulo"
	case 10:
		return "propiedad"
	case 13:
		return "enum"
	case 14:
		return "palabra clave"
	case 15:
		return "snippet"
	case 20:
		return "miembro enum"
	case 21:
		return "const"
	case 22:
		return "struct"
	}
	return "símbolo"
}

// pickLSPCompletion inserta la sugerencia elegida.
func (m Model) pickLSPCompletion() (Model, tea.Cmd) {
	idx, ok := m.pickID(cmdLSPCompletePrefix)
	if !ok || idx >= len(m.lsp.items) {
		return m, nil
	}
	it := m.lsp.items[idx]
	m.pal = palette{}
	m.applyLSPCompletion(it)
	return m, nil
}

var snippetField = regexp.MustCompile(`\$\{\d+:([^}]*)\}|\$\{\d+\}|\$\d+`)

// plainSnippet quita la sintaxis de snippets de LSP ($1, ${2:x}); el cliente
// no declara soporte, así que es raro, pero algunos servidores la mandan.
func plainSnippet(s string) string {
	return snippetField.ReplaceAllString(s, "$1")
}

// applyLSPCompletion inserta la sugerencia en el rango que indica el
// servidor o, si no lo indica, en el lugar del prefijo de palabra.
func (m *Model) applyLSPCompletion(it lsp.CompletionItem) {
	if !m.hasEd {
		return
	}
	text := it.InsertText
	if it.Snippet {
		text = plainSnippet(text)
	}
	cur := m.ed.Buffer().Cursor()
	sl, sc := cur.Line, cur.Col-utf8.RuneCountInString(m.ed.WordPrefix())
	el, ec := cur.Line, cur.Col
	if r := it.Replace; r != nil {
		sl, sc = m.bufPos(r.Start)
		el, ec = m.bufPos(r.End)
	}
	m.ed.ReplaceText(sl, sc, el, ec, text)
	m.msg = it.Label
}

// --- Ir a definición ------------------------------------------------------------

// gotoDef es la acción goto_definition: el servidor primero y el índice de
// respaldo.
func (m Model) gotoDef(pending string) (Model, tea.Cmd) {
	if cmd := m.lspDefinition(pending); cmd != nil {
		m.msg = "LSP: buscando la definición…"
		return m, cmd
	}
	return m.gotoDefinition(pending)
}

// lspDefinition pide la definición al servidor; nil si no puede atender o no
// hay identificador bajo el cursor.
func (m *Model) lspDefinition(pending string) tea.Cmd {
	if !m.lspUsable(func(c lsp.ServerCapabilities) bool { return c.Definition }) {
		return nil
	}
	word, line, col := m.ed.WordAtCursor()
	if word == "" {
		return nil
	}
	pos := m.lspPosAt(line, col)
	seq, text, need := m.lspStage()
	d := m.lsp.doc
	c, uri, snd, gen := d.client, d.uri, d.snd, m.lsp.gen
	return func() tea.Msg {
		if need {
			_ = snd.send(c, uri, seq, text)
		}
		ctx, cancel := context.WithTimeout(context.Background(), lspRequestTimeout)
		defer cancel()
		locs, err := c.Definition(ctx, uri, pos)
		return lspDefMsg{gen: gen, pending: pending, locs: locs, err: err}
	}
}

// onLSPDefinition salta a lo que respondió el servidor; sin respuesta útil,
// usa el índice propio.
func (m Model) onLSPDefinition(msg lspDefMsg) (Model, tea.Cmd) {
	if msg.gen != m.lsp.gen || !m.hasEd || m.pal.open {
		return m, nil
	}
	var locs []lsp.Location
	for _, l := range msg.locs {
		if _, ok := lsp.URIToPath(l.URI); ok { // jar:, jdt:... no se abren
			locs = append(locs, l)
		}
	}
	switch {
	case len(locs) == 0:
		return m.gotoDefinition(msg.pending)
	case len(locs) == 1:
		m.jumpToLocation(locs[0], msg.pending)
		return m, nil
	}
	m.lsp.locs = locs
	m.pal = palette{open: true, mode: palLSPDefs, prompt: "Definición (LSP): "}
	items := make([]item, 0, len(locs))
	for i, l := range locs {
		p, _ := lsp.URIToPath(l.URI)
		items = append(items, newItem(fmt.Sprintf("%s%d", cmdLSPDefPrefix, i),
			fmt.Sprintf("%s:%d", m.display(p), l.Range.Start.Line+1), ""))
	}
	m.pal.setItems(items)
	return m, nil
}

// pickLSPDefinition salta a la definición elegida en el selector.
func (m Model) pickLSPDefinition() (Model, tea.Cmd) {
	idx, ok := m.pickID(cmdLSPDefPrefix)
	if !ok || idx >= len(m.lsp.locs) {
		return m, nil
	}
	m.pal = palette{}
	m.jumpToLocation(m.lsp.locs[idx], "")
	return m, nil
}

// jumpToLocation lleva el editor a una ubicación del servidor, abriendo su
// archivo si hace falta (con cambios sin guardar, openFile avisa en vez de
// abrir; repetir la acción confirma).
func (m *Model) jumpToLocation(l lsp.Location, pending string) {
	path, ok := lsp.URIToPath(l.URI)
	if !ok {
		return
	}
	if !m.hasEd || m.ed.Path() != path {
		m.pendingOpen = pending
		m.openFile(path)
		if !m.hasEd || m.ed.Path() != path {
			return
		}
	}
	line, col := m.bufPos(l.Range.Start)
	m.ed.GoTo(line, col)
	m.setFocus(focusEditor)
	m.relayout()
	m.msg = fmt.Sprintf("%s:%d", m.display(path), line+1)
}

// --- Encender y apagar ------------------------------------------------------------

// toggleLSP enciende o apaga el LSP. Apagar cierra todos los servidores (en
// segundo plano; sin huérfanos) y el editor sigue con el índice propio.
func (m Model) toggleLSP() (Model, tea.Cmd) {
	if m.lsp.mgr == nil {
		m.msg = "LSP no disponible"
		return m, nil
	}
	if m.lsp.on {
		m.lsp.on = false
		m.lsp.statusGen++
		m.lsp.servers = nil
		// El Shutdown cierra los documentos: basta con soltar la
		// suscripción y descartar lo que llegue después.
		if d := m.lsp.doc; d.cancel != nil {
			d.cancel()
		}
		m.lsp.doc = lspDoc{}
		m.lsp.gen++
		m.lsp.diags = nil
		if m.hasEd {
			m.ed.SetLineMarks(nil)
		}
		m.msg = "LSP apagado; se usa el índice propio"
		mgr := m.lsp.mgr
		return m, func() tea.Msg { return lspStoppedMsg{err: mgr.Shutdown()} }
	}
	return m.startLSP(true)
}

// startLSP enciende el LSP y abre el archivo actual en su servidor.
func (m Model) startLSP(announce bool) (Model, tea.Cmd) {
	m.lsp.on = true
	m.lsp.statusGen++
	cmds := []tea.Cmd{m.lspStatusCmd(lspStatusEvery)}
	if m.hasEd {
		cmds = append(cmds, m.lspOpenDoc())
	} else if announce {
		m.msg = "LSP encendido: se arranca al abrir un archivo .java, .kt o .cs"
	}
	return m, tea.Batch(cmds...)
}

// lspStatusCmd lee el estado de los servidores (memoria incluida) tras
// delay, fuera de Update.
func (m Model) lspStatusCmd(delay time.Duration) tea.Cmd {
	mgr, gen := m.lsp.mgr, m.lsp.statusGen
	if mgr == nil {
		return nil
	}
	return func() tea.Msg {
		if delay > 0 {
			time.Sleep(delay)
		}
		return lspStatusMsg{gen: gen, servers: mgr.Status()}
	}
}

// lspUpdate atiende los mensajes del LSP. handled es false para los demás.
func (m Model) lspUpdate(msg tea.Msg) (_ Model, _ tea.Cmd, handled bool) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case lspBootMsg:
		m, cmd = m.startLSP(false)
	case lspReadyMsg:
		cmd = m.onLSPReady(msg)
	case lspTickMsg:
		if msg.gen == m.lsp.gen && msg.edits == m.lsp.edits {
			cmd = m.lspSyncNow()
		}
	case lspDiagMsg:
		cmd = m.onDiagnostics(msg)
	case lspDiagEndMsg:
	case lspStatusMsg:
		if msg.gen == m.lsp.statusGen && m.lsp.on {
			m.lsp.servers = msg.servers
			cmd = m.lspStatusCmd(lspStatusEvery)
		}
	case lspCompleteMsg:
		m, cmd = m.onLSPComplete(msg)
	case lspDefMsg:
		m, cmd = m.onLSPDefinition(msg)
	case lspStoppedMsg:
		if msg.err != nil {
			m.msg = "LSP: " + msg.err.Error()
		}
	default:
		return m, nil, false
	}
	return m, cmd, true
}

// --- Barra de estado --------------------------------------------------------------

// lspInfo es el texto de la barra: servidor y memoria ("LSP jdtls · 512 MB");
// "" con el LSP apagado.
func (m Model) lspInfo() string {
	if !m.lsp.on {
		return ""
	}
	d := m.lsp.doc
	name := d.def.Name
	if name == "" && len(m.lsp.servers) > 0 {
		name = m.lsp.servers[0].Name
	}
	switch {
	case name == "":
		return "LSP encendido"
	case d.err != "":
		return "LSP " + name + " · " + d.err
	case d.starting:
		return "LSP " + name + " · iniciando"
	case d.client != nil && d.client.Err() != nil:
		return "LSP " + name + " · caído"
	}
	var rss int64
	for _, s := range m.lsp.servers {
		rss += s.RSS
	}
	if rss <= 0 {
		return "LSP " + name
	}
	return "LSP " + name + " · " + formatMem(rss)
}

// formatMem escribe una cantidad de bytes en MB (o GB desde 1 GB).
func formatMem(b int64) string {
	const mb = 1 << 20
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MB", (b+mb/2)/mb)
}

// lspToggleTitle es el título de la acción de la paleta según el estado.
func lspToggleTitle(on bool) string {
	if on {
		return "Apagar el LSP (cierra los servidores)"
	}
	return "Encender el LSP (jdtls, kotlin-language-server, csharp-ls)"
}
