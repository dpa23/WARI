package layout

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/imports"
	"github.com/dpa23/wari/internal/index"
)

// Estructura e índice (fase 11). El índice de símbolos del proyecto
// (internal/index) se llena en segundo plano: al iniciar, al guardar y al abrir
// la paleta se lanza un Refresh incremental en una goroutine y su resultado
// llega como indexedMsg, así que la interfaz nunca espera al índice.
//
//   - outline: lista en la paleta los símbolos del archivo abierto (de su
//     texto actual, guardado o no) con sus anotaciones; al elegir, salta.
//   - goto_definition: busca el identificador bajo el cursor en el mismo
//     archivo, el mismo package, los imports y el resto del proyecto, por ese
//     orden; con varios candidatos abre un selector.
//   - complete: nombres del proyecto que empiezan por lo escrito, como lista
//     en la paleta.
//
// Además, los tipos de nivel superior del proyecto se suman a los mapas de
// imports (tipo → package.Tipo) para que import_type también importe clases
// propias.

const (
	cmdOutlinePrefix  = "ol:"
	cmdDefPrefix      = "def:"
	cmdCompletePrefix = "cmp:"

	// maxCompletions es el tope de sugerencias de complete.
	maxCompletions = 200
)

// indexedMsg es el resultado de una pasada de Refresh en segundo plano.
type indexedMsg struct {
	stats index.Stats
	imps  map[string]imports.Map
}

// indexState es lo que el layout recuerda de las listas que muestra la
// paleta en los modos de índice.
type indexState struct {
	outline []index.Symbol
	defs    []index.Symbol
	comps   []index.Completion
	// projImps son los tipos del proyecto como mapas de imports.
	projImps map[string]imports.Map
}

// refreshIndex lanza una pasada del índice en segundo plano (nil sin índice).
func (m Model) refreshIndex() tea.Cmd {
	ix := m.ix
	if ix == nil {
		return nil
	}
	return func() tea.Msg {
		st := ix.Refresh(context.Background())
		if st.Busy {
			return nil
		}
		return indexedMsg{stats: st, imps: ix.ImportMap()}
	}
}

// onIndexed incorpora el resultado de una pasada: los tipos del proyecto se
// suman a los mapas de imports del editor.
func (m *Model) onIndexed(msg indexedMsg) {
	m.xs.projImps = msg.imps
	m.allImps = mergeImports(m.imps, msg.imps)
	if m.hasEd {
		m.ed.SetImports(m.allImps)
	}
}

// mergeImports suma los tipos del proyecto a los mapas base; con base nil
// (imports desactivados) no activa nada.
func mergeImports(base, proj map[string]imports.Map) map[string]imports.Map {
	if base == nil || len(proj) == 0 {
		return base
	}
	out := make(map[string]imports.Map, len(base)+len(proj))
	for lang, m := range base {
		out[lang] = m
	}
	for lang, extra := range proj {
		out[lang] = out[lang].Extend(extra)
	}
	return out
}

// currentFile analiza el texto actual del editor (con lo que no se guardó).
// ok es false sin archivo abierto o si no es de un lenguaje indexable.
func (m *Model) currentFile() (f *index.File, ok bool) {
	if !m.hasEd {
		m.msg = "no hay archivo abierto"
		return nil, false
	}
	path := m.ed.Path()
	lang := index.LangOf(path)
	if lang == "" {
		m.msg = "sin estructura para este tipo de archivo (Java, Kotlin y C#)"
		return nil, false
	}
	return index.Parse(lang, m.relPath(path), []byte(m.ed.Text())), true
}

// relPath devuelve p relativa a la raíz y con "/"; fuera de ella, p tal cual.
func (m Model) relPath(p string) string {
	if rel, err := filepath.Rel(m.root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return p
}

// symAbs devuelve la ruta absoluta del archivo de un símbolo.
func (m Model) symAbs(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(m.root, filepath.FromSlash(rel))
}

// pickID devuelve el número que sigue al prefijo en el id del elemento
// seleccionado de la paleta.
func (m Model) pickID(prefix string) (int, bool) {
	p := m.pal
	if len(p.results) == 0 {
		return 0, false
	}
	var idx int
	if _, err := fmt.Sscanf(strings.TrimPrefix(p.items[p.results[p.sel]].id, prefix), "%d", &idx); err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

// --- Outline ------------------------------------------------------------------

var kindLabels = map[index.Kind]string{
	index.KindPackage: "package", index.KindNamespace: "namespace", index.KindClass: "class",
	index.KindInterface: "interface", index.KindEnum: "enum", index.KindRecord: "record",
	index.KindStruct: "struct", index.KindObject: "object", index.KindAnnotation: "@interface",
	index.KindTypeAlias: "typealias", index.KindDelegate: "delegate", index.KindMethod: "fun",
	index.KindConstructor: "ctor", index.KindField: "field", index.KindProperty: "prop",
	index.KindConstant: "const", index.KindEvent: "event",
}

// outlineTitle es la línea de un símbolo en el outline: sangrada según su
// anidamiento, con su firma y sus anotaciones al final.
func outlineTitle(s index.Symbol) string {
	depth := 0
	if s.Container != "" {
		depth = strings.Count(s.Container, ".") + 1
	}
	var sb strings.Builder
	sb.WriteString(strings.Repeat("  ", depth))
	sb.WriteString(kindLabels[s.Kind])
	sb.WriteByte(' ')
	sb.WriteString(s.Name)
	sb.WriteString(s.Signature)
	for _, a := range s.Annotations {
		sb.WriteString("  ")
		sb.WriteString(a)
	}
	return sb.String()
}

// showOutline abre el outline del archivo actual en la paleta.
func (m Model) showOutline() (Model, tea.Cmd) {
	f, ok := m.currentFile()
	if !ok {
		return m, nil
	}
	if len(f.Symbols) == 0 {
		m.msg = "el archivo no tiene símbolos"
		return m, nil
	}
	m.xs.outline = f.Symbols
	m.pal = palette{open: true, mode: palOutline, prompt: "Outline: "}
	items := make([]item, 0, len(f.Symbols))
	for i, s := range f.Symbols {
		items = append(items, newItem(fmt.Sprintf("%s%d", cmdOutlinePrefix, i), outlineTitle(s), fmt.Sprintf(":%d", s.Line+1)))
	}
	m.pal.setItems(items)
	// Empieza sobre el símbolo que contiene al cursor.
	cur := m.ed.Buffer().Cursor().Line
	for i, s := range f.Symbols {
		if s.Line <= cur {
			m.pal.sel = i
		}
	}
	m.pal.top = max(0, m.pal.sel-maxShown+1)
	return m, nil
}

// pickOutline salta al símbolo seleccionado del outline.
func (m Model) pickOutline() (Model, tea.Cmd) {
	idx, ok := m.pickID(cmdOutlinePrefix)
	if !ok || idx >= len(m.xs.outline) {
		return m, nil
	}
	s := m.xs.outline[idx]
	m.pal = palette{}
	if m.hasEd {
		m.ed.GoTo(s.Line, s.Col)
		m = m.focusPanel(focusEditor)
	}
	return m, nil
}

// --- Ir a definición ------------------------------------------------------------

// gotoDefinition salta a la definición del identificador bajo el cursor.
func (m Model) gotoDefinition(pending string) (Model, tea.Cmd) {
	f, ok := m.currentFile()
	if !ok {
		return m, nil
	}
	word, line, col := m.ed.WordAtCursor()
	if word == "" {
		m.msg = "pon el cursor sobre un identificador"
		return m, nil
	}
	cands := m.ix.Resolve(word, f, line, col)
	refresh := m.refreshIndex()
	switch {
	case len(cands) == 0:
		m.msg = "no se encontró la definición de " + word
		if !m.ix.Ready() {
			m.msg += " (el índice aún se está construyendo)"
		}
	case len(cands) == 1:
		m.pendingOpen = pending // repetir con cambios sin guardar confirma la apertura
		m.jumpTo(cands[0])
	default:
		m.xs.defs = cands
		m.pal = palette{open: true, mode: palDefs, prompt: "Definición de " + word + ": "}
		items := make([]item, 0, len(cands))
		for i, s := range cands {
			title := fmt.Sprintf("%s %s", kindLabels[s.Kind], s.Name+s.Signature)
			if s.Container != "" {
				title = fmt.Sprintf("%s %s.%s", kindLabels[s.Kind], s.Container, s.Name+s.Signature)
			}
			items = append(items, newItem(fmt.Sprintf("%s%d", cmdDefPrefix, i), title, fmt.Sprintf("%s:%d", s.Path, s.Line+1)))
		}
		m.pal.setItems(items)
	}
	return m, refresh
}

// pickDefinition salta a la definición elegida en el selector.
func (m Model) pickDefinition() (Model, tea.Cmd) {
	idx, ok := m.pickID(cmdDefPrefix)
	if !ok || idx >= len(m.xs.defs) {
		return m, nil
	}
	m.pal = palette{}
	m.jumpTo(m.xs.defs[idx])
	return m, nil
}

// jumpTo lleva el editor al símbolo s, abriendo su archivo si hace falta. Si
// el archivo actual tiene cambios sin guardar, openFile avisa en vez de abrir.
func (m *Model) jumpTo(s index.Symbol) {
	path := m.symAbs(s.Path)
	if !m.hasEd || m.ed.Path() != path {
		m.openFile(path)
		if !m.hasEd || m.ed.Path() != path {
			return
		}
	}
	m.ed.GoTo(s.Line, s.Col)
	m.setFocus(focusEditor)
	m.relayout()
	m.msg = fmt.Sprintf("%s:%d", m.display(path), s.Line+1)
}

// --- Completar ------------------------------------------------------------------

// completeName propone nombres del proyecto que empiezan por lo escrito.
func (m Model) completeName() (Model, tea.Cmd) {
	f, ok := m.currentFile()
	if !ok {
		return m, nil
	}
	prefix := m.ed.WordPrefix()
	if prefix == "" {
		m.msg = "escribe al menos una letra para completar"
		return m, nil
	}
	cands := m.ix.Complete(prefix, f, maxCompletions)
	refresh := m.refreshIndex()
	switch {
	case len(cands) == 0:
		m.msg = "sin sugerencias para " + prefix
		if !m.ix.Ready() {
			m.msg += " (el índice aún se está construyendo)"
		}
	case len(cands) == 1:
		m.applyCompletion(cands[0].Name)
	default:
		m.xs.comps = cands
		m.pal = palette{open: true, mode: palComplete, prompt: "Completar " + prefix + ": "}
		items := make([]item, 0, len(cands))
		for i, c := range cands {
			hint := kindLabels[c.Kind]
			if c.Container != "" {
				hint += " · " + c.Container
			} else if c.Package != "" {
				hint += " · " + c.Package
			}
			if c.Count > 1 {
				hint += fmt.Sprintf(" (+%d)", c.Count-1)
			}
			items = append(items, newItem(fmt.Sprintf("%s%d", cmdCompletePrefix, i), c.Name, hint))
		}
		m.pal.setItems(items)
	}
	return m, refresh
}

// pickCompletion inserta el nombre elegido.
func (m Model) pickCompletion() (Model, tea.Cmd) {
	idx, ok := m.pickID(cmdCompletePrefix)
	if !ok || idx >= len(m.xs.comps) {
		return m, nil
	}
	name := m.xs.comps[idx].Name
	m.pal = palette{}
	m.applyCompletion(name)
	return m, nil
}

func (m *Model) applyCompletion(name string) {
	if m.hasEd && m.ed.ReplacePrefix(name) {
		m.msg = name
	}
}
