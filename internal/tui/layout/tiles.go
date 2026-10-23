package layout

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/tui"
	"github.com/dpa23/wari/internal/tui/tile"
)

// El área de trabajo es un árbol de tiles libres (internal/tui/tile): el
// árbol de archivos, el editor, el panel de chat y cada agente en terminal son
// hojas que el usuario parte, mueve, redimensiona y maximiza como quiera. Este
// archivo une ese motor con los paneles; layout.go dibuja y enruta.

// Identificadores fijos de los tiles que no son agentes; los terminales usan
// desde idTermBase.
const (
	idTree     tile.ID = 1
	idEditor   tile.ID = 2
	idChat     tile.ID = 3
	idTermBase tile.ID = 10
)

// Mínimos de cada tipo de tile (ancho, alto contando su título). Una CLI real
// se usa bien desde unas 50 columnas; si no cabe, el tile se maximiza solo.
const (
	treeMinW, treeMinH = 14, 4
	chatMinW, chatMinH = 36, 8
	termMinW, termMinH = 50, 10
	// Prioridades: con poca pantalla se ocultan primero el chat, luego el árbol
	// y luego los agentes; el editor nunca.
	prioChat, prioTree, prioTerm, prioEditor = 10, 20, 50, 100
	// wideSplit es el ancho desde el que un tile nuevo va al lado y no debajo.
	wideSplit = 150
)

// tiling es el estado compartido del área de trabajo. Es un puntero: las
// copias del modelo (receptor de valor) ven siempre el mismo árbol, y solo el
// bucle de Bubble Tea lo toca.
type tiling struct {
	tree  *tile.Tree
	terms map[tile.ID]*tui.TermAgent
	next  tile.ID
	count int // cuántos agentes se lanzaron, para nombrarlos

	// ch avisa de que la pantalla de algún terminal cambió; listening indica
	// que ya hay un Cmd esperándolo (solo hace falta uno).
	ch        chan struct{}
	listening bool

	// mode es el modo tiles (atajos de una letra); closeArmed, el tile cuyo
	// cierre espera confirmación (tiene una CLI viva).
	mode       bool
	closeArmed tile.ID
	// place es dónde irá el próximo agente: se decide al pedirlo.
	place placement
	// drag es el divisor que se está arrastrando con el ratón.
	drag *tile.Divider
}

// placement es el sitio del próximo tile de agente.
type placement struct {
	target tile.ID
	axis   tile.Axis
	after  bool
	set    bool
}

func newTiling() *tiling {
	t := &tiling{
		tree:  tile.New(idEditor),
		terms: make(map[tile.ID]*tui.TermAgent),
		next:  idTermBase,
		ch:    make(chan struct{}, 1),
	}
	_ = t.tree.Split(idEditor, idTree, tile.Cols, false, 0.2)
	_ = t.tree.SetFocus(idTree)
	t.tree.SetMin(idTree, treeMinW, treeMinH)
	t.tree.SetMin(idEditor, editorMin, 4)
	t.tree.SetPriority(idEditor, prioEditor)
	t.tree.SetPriority(idTree, prioTree)
	return t
}

// treeOn y chatOn dicen si esos paneles tienen tile.
func (m Model) treeOn() bool { return m.ts.tree.Has(idTree) }
func (m Model) chatOn() bool { return m.ts.tree.Has(idChat) }

// setTreeOn muestra u oculta el árbol de archivos.
func (m *Model) setTreeOn(on bool) {
	t := m.ts.tree
	switch {
	case on && !t.Has(idTree):
		_ = t.Split(idEditor, idTree, tile.Cols, false, 0.2)
		t.SetMin(idTree, treeMinW, treeMinH)
		t.SetPriority(idTree, prioTree)
	case !on && t.Has(idTree):
		_ = t.Close(idTree)
	}
	m.syncFocus()
	m.relayout()
}

// setChatOn muestra u oculta el panel de chat.
func (m *Model) setChatOn(on bool) {
	t := m.ts.tree
	switch {
	case on && !t.Has(idChat):
		_ = t.Split(idEditor, idChat, tile.Cols, true, 0.45)
		t.SetMin(idChat, chatMinW, chatMinH)
		t.SetPriority(idChat, prioChat)
	case !on && t.Has(idChat):
		_ = t.Close(idChat)
	}
	m.syncFocus()
	m.relayout()
}

// kindOf dice qué panel es el tile id.
func (m Model) kindOf(id tile.ID) focus {
	switch id {
	case idTree:
		return focusTree
	case idEditor:
		return focusEditor
	case idChat:
		return focusChat
	}
	return focusTerm
}

// syncFocus hace que el panel con foco sea el del tile con foco del árbol. Se
// llama tras cerrar tiles o hacer clic. En el dock (que vive en el tile del
// editor) conserva el foco.
func (m *Model) syncFocus() {
	f := m.kindOf(m.ts.tree.Focus())
	if f == focusEditor && m.focus == focusDock {
		f = focusDock
	}
	m.focus = f
	m.applyFocus()
}

// setFocus cambia el panel con foco y lleva el foco del árbol de tiles con él.
// Para un terminal el foco del árbol ya está en su tile.
func (m *Model) setFocus(f focus) {
	if f != focusTerm {
		_ = m.ts.tree.SetFocus(treeFocusFor(f))
	}
	m.focus = f
}

// focusTile da el foco al tile id.
func (m Model) focusTile(id tile.ID) Model {
	if m.ts.tree.SetFocus(id) != nil {
		return m
	}
	m.syncFocus()
	return m
}

// treeFocusFor es el tile que corresponde a un foco de panel.
func treeFocusFor(f focus) tile.ID {
	switch f {
	case focusTree:
		return idTree
	case focusChat:
		return idChat
	}
	return idEditor
}

// term devuelve el agente en terminal del tile con foco, si lo es.
func (m Model) focusedTerm() (*tui.TermAgent, tile.ID) {
	id := m.ts.tree.Focus()
	a := m.ts.terms[id]
	return a, id
}

// launchTerm coloca un agente nuevo en un tile y lo lanza.
func (m Model) launchTerm(req tui.LaunchTerminalMsg) (Model, tea.Cmd) {
	ts := m.ts
	id := ts.next
	ts.next++
	ts.count++
	name := fmt.Sprintf("%s %d", req.CLI, ts.count)

	// Por omisión el agente va junto al editor, no junto al árbol.
	target, axis, after := idEditor, tile.Cols, true
	if _, isTerm := m.focusedTerm(); isTerm != 0 && ts.terms[isTerm] != nil {
		target = isTerm // desde un agente, el siguiente va junto a él
	}
	if m.w < wideSplit {
		axis = tile.Rows
	}
	if ts.place.set {
		target, axis, after = ts.place.target, ts.place.axis, ts.place.after
		ts.place = placement{}
	}
	if !ts.tree.Has(target) {
		target = idEditor
	}
	if err := ts.tree.Split(target, id, axis, after, 0.5); err != nil {
		m.msg = "no se pudo colocar el agente: " + err.Error()
		return m, nil
	}
	ts.tree.SetMin(id, termMinW, termMinH)
	ts.tree.SetPriority(id, prioTerm)
	// Si no cabe junto a lo demás, ocupa toda la pantalla (se vuelve con la
	// misma tecla).
	if g := m.geometry(); g.body(id).W == 0 {
		_ = ts.tree.Zoom(id)
	}
	g := m.geometry()
	r := g.body(id)
	a, err := tui.StartTermAgent(req, name, max(r.W, 1), max(r.H, 1), func() {
		select {
		case ts.ch <- struct{}{}:
		default:
		}
	})
	if err != nil {
		_ = ts.tree.Close(id)
		m.syncFocus()
		m.msg = "no se pudo lanzar " + req.CLI + ": " + err.Error()
		m.relayout()
		return m, nil
	}
	ts.terms[id] = a
	m = m.focusTile(id)
	m.relayout()
	var cmd tea.Cmd
	if !ts.listening {
		ts.listening = true
		cmd = tea.Batch(listenTerms(ts.ch), tui.TickPanes())
	}
	return m, cmd
}

// termChangedMsg avisa de que la pantalla de algún terminal cambió.
type termChangedMsg struct{}

func listenTerms(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		<-ch
		return termChangedMsg{}
	}
}

// anyTermLive indica que algún terminal sigue corriendo.
func (m Model) anyTermLive() bool {
	for _, a := range m.ts.terms {
		select {
		case <-a.Done():
		default:
			return true
		}
	}
	return false
}

// closeTile cierra el tile id. Los de árbol y chat solo se ocultan; el editor
// no se cierra. Un terminal con la CLI viva pide repetir la orden.
func (m Model) closeTile(id tile.ID) Model {
	ts := m.ts
	switch id {
	case idEditor:
		m.msg = "el editor no se cierra"
		return m
	case idTree:
		m.setTreeOn(false)
		return m
	case idChat:
		m.setChatOn(false)
		return m
	}
	a := ts.terms[id]
	if a == nil {
		return m
	}
	select {
	case <-a.Done():
	default:
		if ts.closeArmed != id {
			ts.closeArmed = id
			m.msg = "la CLI sigue viva: repite para cerrarla y matar su grupo de procesos"
			return m
		}
	}
	ts.closeArmed = 0
	_ = a.Close()
	delete(ts.terms, id)
	_ = ts.tree.Close(id)
	m.syncFocus()
	m.relayout()
	return m
}

// CloseTerms mata las CLIs de todos los terminales con su grupo. Es idempotente.
func (m Model) closeTerms() {
	for _, a := range m.ts.terms {
		_ = a.Close()
	}
}

// --- Geometría ---------------------------------------------------------------

// geom es el reparto del espacio: el layout de tiles y, por tipo, el rectángulo
// de contenido (bajo el título de cada tile).
type geom struct {
	statH, bodyH int
	lay          tile.Layout
	tree, ed     tile.Rect
	chat         tile.Rect
	showTree     bool
	showChat     bool
	showEd       bool
	// dockH es la altura del dock bajo el editor (0 si no se muestra); el
	// editor ocupa el resto de ed.H.
	dockH int
}

// body es el rectángulo de contenido del tile id (sin su título); cero si no
// se ve.
func (g geom) body(id tile.ID) tile.Rect {
	r, ok := g.lay.Rects[id]
	if !ok || r.W <= 0 || r.H <= 0 {
		return tile.Rect{}
	}
	if r.H >= 2 {
		r.Y++
		r.H--
	}
	return r
}

// geometry reparte m.w x m.h. Con poca altura se omite la barra de estado.
func (m Model) geometry() geom {
	var g geom
	w, h := max(m.w, 0), max(m.h, 0)
	if h >= 2 {
		g.statH = 1
	}
	g.bodyH = max(h-g.statH, 0)
	g.lay = m.ts.tree.Layout(w, g.bodyH)
	g.tree, g.ed, g.chat = g.body(idTree), g.body(idEditor), g.body(idChat)
	g.showTree, g.showEd, g.showChat = g.tree.W > 0, g.ed.W > 0, g.chat.W > 0
	if m.dock.open && g.showEd {
		g.dockH = dockHeight(g.ed.H)
	}
	return g
}

// visible indica si el panel f se está mostrando.
func (g geom) visible(f focus, termID tile.ID) bool {
	switch f {
	case focusTree:
		return g.showTree
	case focusChat:
		return g.showChat
	case focusDock:
		return g.dockH > 0
	case focusEditor:
		return g.showEd
	case focusTerm:
		return g.body(termID).W > 0
	}
	return true
}

// --- Vista -------------------------------------------------------------------

type piece struct {
	x int
	s string
}

// title es el título del tile id.
func (m Model) title(id tile.ID) string {
	switch id {
	case idTree:
		return "Archivos"
	case idEditor:
		return m.editorTitle()
	case idChat:
		return "Agentes"
	}
	a := m.ts.terms[id]
	if a == nil {
		return ""
	}
	word, _ := a.Chip()
	t := fmt.Sprintf("%s %s · %s · %s", a.Glyph(), a.Name, a.Label(), word)
	if x := a.Title(); x != "" {
		t += " · " + x
	}
	return t
}

// renderTiles dibuja el área de trabajo: los tiles y los divisores entre ellos.
func (m Model) renderTiles(g geom) []string {
	rows := make([][]piece, g.bodyH)
	add := func(r tile.Rect, lines []string) {
		for i, l := range lines {
			if y := r.Y + i; y >= 0 && y < g.bodyH {
				rows[y] = append(rows[y], piece{r.X, l})
			}
		}
	}
	focusID := m.ts.tree.Focus()
	for _, id := range m.ts.tree.IDs() {
		r, ok := g.lay.Rects[id]
		if !ok || r.W <= 0 || r.H <= 0 {
			continue
		}
		var lines []string
		bodyR := g.body(id)
		if r.H >= 2 {
			lines = append(lines, header(m.title(id), id == focusID, r.W))
		}
		lines = append(lines, m.tileBody(id, g, bodyR)...)
		add(r, lines)
	}
	sep := sepStyle.Render("│")
	for _, d := range g.lay.Dividers {
		lines := make([]string, d.Rect.H)
		for i := range lines {
			if d.Axis == tile.Cols {
				lines[i] = sep
			} else {
				lines[i] = sepStyle.Render(strings.Repeat("─", d.Rect.W))
			}
		}
		add(d.Rect, lines)
	}
	out := make([]string, g.bodyH)
	for y, ps := range rows {
		sort.Slice(ps, func(i, j int) bool { return ps[i].x < ps[j].x })
		var sb strings.Builder
		x := 0
		for _, p := range ps {
			if p.x > x {
				sb.WriteString(strings.Repeat(" ", p.x-x))
			}
			sb.WriteString(p.s)
			x = p.x + lipgloss.Width(p.s)
		}
		if x < m.w {
			sb.WriteString(strings.Repeat(" ", m.w-x))
		}
		out[y] = sb.String()
	}
	return out
}

// tileBody es el contenido de un tile en exactamente r.H líneas de r.W celdas.
func (m Model) tileBody(id tile.ID, g geom, r tile.Rect) []string {
	switch id {
	case idTree:
		return block(m.tree.View(), r.W, r.H)
	case idEditor:
		body := m.placeholder()
		if m.hasEd {
			body = m.ed.View()
		}
		edH := r.H - g.dockH
		lines := block(body, r.W, edH)
		if m.diffShown() {
			lines = block(strings.Join(m.chat.DiffLines(r.W, edH), "\n"), r.W, edH)
		}
		if g.dockH > 0 {
			lines = append(lines, m.dockView(r.W, g.dockH)...)
		}
		return lines
	case idChat:
		return block(m.chat.Body(), r.W, r.H)
	}
	a := m.ts.terms[id]
	if a == nil {
		return block("", r.W, r.H)
	}
	out := a.Rows(r.W, r.H)
	for i, l := range out {
		out[i] = fit(l, r.W)
	}
	return out
}

// --- Ratón -------------------------------------------------------------------

// mouse atiende un evento del ratón: clic enfoca, arrastrar un divisor lo
// mueve, la rueda se entrega al tile bajo el puntero.
func (m Model) mouse(msg tea.Msg) (Model, tea.Cmd) {
	ts := m.ts
	if m.helpOpen || m.pal.open {
		return m, nil
	}
	switch e := msg.(type) {
	case tea.MouseClickMsg:
		if e.Button != tea.MouseLeft {
			return m, nil
		}
		g := m.geometry()
		hit, ok := g.lay.Hit(e.X, e.Y)
		if !ok {
			return m, nil
		}
		if hit.Divider >= 0 {
			d := g.lay.Dividers[hit.Divider]
			ts.drag = &d
			return m, nil
		}
		ts.closeArmed = 0
		return m.focusTile(hit.Tile), nil
	case tea.MouseMotionMsg:
		if ts.drag == nil {
			return m, nil
		}
		ts.tree.DragDivider(*ts.drag, e.X, e.Y)
		// El divisor se movió: el siguiente evento usa su nueva posición.
		g := m.geometry()
		for _, d := range g.lay.Dividers {
			if d.Handle == ts.drag.Handle {
				*ts.drag = d
			}
		}
		m.relayout()
		return m, nil
	case tea.MouseReleaseMsg:
		ts.drag = nil
		return m, nil
	case tea.MouseWheelMsg:
		g := m.geometry()
		hit, ok := g.lay.Hit(e.X, e.Y)
		if !ok || hit.Divider >= 0 {
			return m, nil
		}
		if a := ts.terms[hit.Tile]; a != nil {
			r := g.body(hit.Tile)
			a.Wheel(e.X-r.X, e.Y-r.Y, e.Button == tea.MouseWheelUp)
		}
	}
	return m, nil
}

// --- Teclas de tiles -----------------------------------------------------------

// isTileKey indica que k es de WARI aunque una CLI tenga el foco: pasar el
// foco entre tiles y paneles, el modo tiles, maximizar, agente nuevo y la
// tecla líder. Todo lo demás es de la CLI.
func (m Model) isTileKey(k tea.KeyPressMsg) bool {
	for _, a := range []string{
		"focus_tree", "focus_editor", "focus_agents", "tile_mode", "tile_zoom",
		"tile_left", "tile_right", "tile_up", "tile_down", "agent_new", "leader",
	} {
		if m.is(k, a) {
			return true
		}
	}
	return false
}

// tileZoom maximiza el tile con foco o lo restaura.
func (m Model) tileZoom() Model {
	t := m.ts.tree
	if t.Zoomed() != 0 {
		_ = t.Zoom(t.Zoomed())
	} else {
		_ = t.Zoom(t.Focus())
	}
	m.syncFocus()
	m.relayout()
	return m
}

// dirOf traduce una tecla de foco entre tiles a su dirección.
func (m Model) dirOf(k tea.KeyPressMsg) (tile.Dir, bool) {
	switch {
	case m.is(k, "tile_left"):
		return tile.Left, true
	case m.is(k, "tile_right"):
		return tile.Right, true
	case m.is(k, "tile_up"):
		return tile.Up, true
	case m.is(k, "tile_down"):
		return tile.Down, true
	}
	return 0, false
}

// tileFocusDir mueve el foco al tile vecino en la dirección de k.
func (m Model) tileFocusDir(k tea.KeyPressMsg) Model {
	d, ok := m.dirOf(k)
	if !ok {
		return m
	}
	return m.moveTileFocus(d)
}

func (m Model) moveTileFocus(d tile.Dir) Model {
	g := m.geometry()
	if id, ok := m.ts.tree.Neighbor(d, g.lay.Rects); ok {
		return m.focusTile(id)
	}
	return m
}

// enterTileMode activa el modo tiles: las teclas de una letra son de WARI
// hasta pulsar Esc o Enter.
func (m Model) enterTileMode() Model {
	m.ts.mode = true
	m.ts.closeArmed = 0
	return m
}

// tileModeHint es la ayuda que la barra de estado muestra en modo tiles.
const tileModeHint = "TILES hjkl foco · HJKL mover · ←↑↓→ tamaño · v|s partir · n nuevo · z zoom · x cerrar · esc"

// tileModeKey atiende una tecla en modo tiles.
func (m Model) tileModeKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	ts := m.ts
	t := ts.tree
	g := m.geometry()
	total := tile.Rect{W: m.w, H: g.bodyH}
	s := k.String()
	if s != "x" {
		ts.closeArmed = 0
	}
	focusDirs := map[string]tile.Dir{"h": tile.Left, "j": tile.Down, "k": tile.Up, "l": tile.Right}
	moveDirs := map[string]tile.Dir{"H": tile.Left, "J": tile.Down, "K": tile.Up, "L": tile.Right}
	sizeDirs := map[string]tile.Dir{"left": tile.Left, "down": tile.Down, "up": tile.Up, "right": tile.Right}
	switch {
	case s == "esc" || s == "enter" || s == "q":
		ts.mode = false
	case focusDirs[s] != 0 || s == "h" || s == "l":
		if d, ok := focusDirs[s]; ok {
			return m.moveTileFocus(d), nil
		}
	case moveDirs[s] != 0 || s == "H" || s == "L":
		if d, ok := moveDirs[s]; ok {
			if id, ok := t.Neighbor(d, g.lay.Rects); ok {
				_ = t.Move(t.Focus(), id, d)
				m.relayout()
			}
		}
	case sizeDirs[s] != 0 || s == "left" || s == "right":
		if d, ok := sizeDirs[s]; ok {
			t.Resize(t.Focus(), d, 4, total)
			m.relayout()
		}
	case s == "v", s == "s":
		axis := tile.Cols
		if s == "s" {
			axis = tile.Rows
		}
		ts.place = placement{target: t.Focus(), axis: axis, after: true, set: true}
		ts.mode = false
		return m.newAgent()
	case s == "n":
		ts.mode = false
		return m.newAgent()
	case s == "z":
		return m.tileZoom(), nil
	case s == "x":
		return m.closeTile(t.Focus()), nil
	case s == "e":
		ts.mode = false
		return m.focusEditorPanel(), nil
	}
	return m, nil
}

// newAgent abre el formulario de agente nuevo (superpuesto: el panel de chat
// puede estar oculto).
func (m Model) newAgent() (Model, tea.Cmd) {
	var cmd tea.Cmd
	m.chat, cmd = m.chat.NewSession()
	return m, cmd
}

// --- Formulario superpuesto -----------------------------------------------------

// formOverlay dibuja el formulario de agente nuevo centrado sobre rows.
func (m Model) formOverlay(rows []string) {
	if !m.chat.FormActive() || len(rows) == 0 {
		return
	}
	w := min(78, m.w-4)
	h := min(13, len(rows)-2)
	if w < 30 || h < 6 {
		return
	}
	body := m.chat.FormLines(w-2, h-2)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Render(strings.Join(body, "\n"))
	lines := strings.Split(box, "\n")
	x0, y0 := (m.w-w)/2, (len(rows)-len(lines))/2
	for i, l := range lines {
		y := y0 + i
		if y < 0 || y >= len(rows) {
			continue
		}
		// Como la paleta, sustituye la fila entera.
		rows[y] = fit(strings.Repeat(" ", x0)+l, m.w)
	}
}
