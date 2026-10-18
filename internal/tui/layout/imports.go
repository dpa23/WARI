package layout

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/tui/editorview"
)

// Importar el tipo bajo el cursor. El editor sabe calcular e insertar el
// import (editorview.AddImport); aquí se decide qué candidato: si hay uno se
// inserta, si hay varios se pregunta en la franja de la paleta.

const cmdImportPrefix = "imp:"

// importState es el selector de imports en curso.
type importState struct {
	typ   string
	cands []string
}

// importType importa el tipo bajo el cursor del editor.
func (m Model) importType() (Model, tea.Cmd) {
	if !m.hasEd {
		m.msg = "no hay archivo abierto"
		return m, nil
	}
	typ := m.ed.TypeAtCursor()
	if typ == "" {
		m.msg = "pon el cursor sobre un tipo para importarlo"
		return m, nil
	}
	cands := m.ed.ImportCandidates(typ)
	switch {
	case len(cands) == 0:
		m.msg = "no hay import conocido para " + typ
	case m.ed.HasImport(typ):
		m.msg = typ + " ya está importado"
	case len(cands) == 1:
		m.addImport(cands[0])
	default:
		m.openImportPicker(typ, cands)
	}
	return m, nil
}

// addImport inserta el import y lo comunica en la línea de estado.
func (m *Model) addImport(candidate string) {
	kw := "import "
	if editorview.SnippetLang(m.ed.Path()) == "csharp" {
		kw = "using "
	}
	if m.ed.AddImport(candidate) {
		m.msg = kw + candidate + " agregado"
	} else {
		m.msg = candidate + " ya está importado"
	}
}

// openImportPicker abre la paleta con los candidatos de typ.
func (m *Model) openImportPicker(typ string, cands []string) {
	m.imp = importState{typ: typ, cands: cands}
	m.pal = palette{open: true, mode: palImports, prompt: "Importar " + typ + ": "}
	items := make([]item, 0, len(cands))
	for i, c := range cands {
		items = append(items, newItem(fmt.Sprintf("%s%d", cmdImportPrefix, i), c, ""))
	}
	m.pal.setItems(items)
}

// pickImport inserta el candidato seleccionado.
func (m Model) pickImport() (Model, tea.Cmd) {
	p := &m.pal
	if len(p.results) == 0 {
		return m, nil
	}
	var idx int
	if _, err := fmt.Sscanf(strings.TrimPrefix(p.items[p.results[p.sel]].id, cmdImportPrefix), "%d", &idx); err != nil || idx < 0 || idx >= len(m.imp.cands) {
		return m, nil
	}
	m.pal = palette{}
	if m.hasEd {
		m.addImport(m.imp.cands[idx])
	}
	return m, nil
}
