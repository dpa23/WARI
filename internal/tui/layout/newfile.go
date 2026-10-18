package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/templates"
)

// Nuevo archivo desde plantilla. Reutiliza la franja de la paleta como
// entrada en tres pasos: elegir la plantilla (filtrable por lenguaje y
// nombre), escribir la ruta destino relativa a la raíz (prellenada con la
// carpeta del archivo actual o del nodo del árbol) y, si la plantilla declara
// variables, pedir cada una. El archivo se crea sin sobrescribir ninguno
// existente, se abre en el editor y el árbol se refresca.

// palMode es lo que está pidiendo la franja de la paleta.
type palMode int

const (
	palCommands  palMode = iota // acciones y archivos
	palTemplates                // elegir plantilla
	palImports                  // elegir entre varios imports (ver imports.go)
	palPath                     // ruta destino (desde aquí, modos de texto)
	palVar                      // valor de una variable de la plantilla
)

const cmdTemplatePrefix = "tpl:"

// newFileState es el avance del flujo "Nuevo archivo desde plantilla".
type newFileState struct {
	tpl  templates.Template
	raw  string            // ruta escrita por el usuario
	abs  string            // ruta absoluta resuelta
	vars map[string]string // variables ya pedidas
	ask  []string          // variables por pedir
}

func (p palette) promptText() string {
	if p.prompt == "" {
		return "> "
	}
	return p.prompt
}

// openTemplatePicker abre la paleta con la lista de plantillas.
func (m *Model) openTemplatePicker() {
	if len(m.tpls) == 0 {
		m.msg = "no hay plantillas disponibles"
		return
	}
	m.nf = newFileState{}
	m.pal = palette{open: true, mode: palTemplates, prompt: "Plantilla: "}
	items := make([]item, 0, len(m.tpls))
	for i, t := range m.tpls {
		hint := ""
		if t.Ext != "" {
			hint = "." + t.Ext
		}
		items = append(items, newItem(fmt.Sprintf("%s%d", cmdTemplatePrefix, i), t.Lang+" · "+t.Name, hint))
	}
	m.pal.setItems(items)
}

// pickTemplate acepta la plantilla seleccionada y pasa a pedir la ruta.
func (m Model) pickTemplate() (Model, tea.Cmd) {
	p := &m.pal
	if len(p.results) == 0 {
		return m, nil
	}
	var idx int
	if _, err := fmt.Sscanf(strings.TrimPrefix(p.items[p.results[p.sel]].id, cmdTemplatePrefix), "%d", &idx); err != nil || idx < 0 || idx >= len(m.tpls) {
		return m, nil
	}
	m.nf = newFileState{tpl: m.tpls[idx]}
	m.askPath(m.defaultDir(), "")
	return m, nil
}

// askPath pone la paleta en el paso de la ruta.
func (m *Model) askPath(initial, note string) {
	m.pal = palette{
		open:   true,
		mode:   palPath,
		prompt: "Ruta de " + m.nf.tpl.Name + ": ",
		query:  []rune(initial),
		note:   note,
	}
}

// defaultDir es la carpeta con la que se prellena la ruta, relativa a la raíz
// y con "/" final: la del nodo del árbol si el árbol tiene el foco, si no la
// del archivo abierto.
func (m Model) defaultDir() string {
	var dir string
	if m.focus == focusTree {
		if sel := m.tree.Selected(); sel != "" {
			dir = sel
			if st, err := os.Stat(sel); err != nil || !st.IsDir() {
				dir = filepath.Dir(sel)
			}
		}
	}
	if dir == "" && m.hasEd {
		dir = filepath.Dir(m.ed.Path())
	}
	if dir == "" {
		return ""
	}
	rel, err := filepath.Rel(m.root, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel) + "/"
}

// resolveNewPath convierte la ruta escrita en la absoluta del archivo nuevo y
// comprueba que quede dentro de la raíz, que no exista y que sirva de nombre
// de clase si la plantilla lo usa.
func (m Model) resolveNewPath(raw string, tpl templates.Template) (string, error) {
	if raw == "" || strings.HasSuffix(raw, "/") || strings.HasSuffix(raw, string(filepath.Separator)) {
		return "", errors.New("escribe el nombre del archivo")
	}
	abs := filepath.FromSlash(raw)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(m.root, abs)
	}
	abs = filepath.Clean(abs)
	if filepath.Ext(abs) == "" && tpl.Ext != "" {
		abs += "." + tpl.Ext
	}
	rel, err := filepath.Rel(m.root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("la ruta debe quedar dentro de la raíz del proyecto")
	}
	if _, err := os.Stat(abs); err == nil {
		return "", fmt.Errorf("ya existe %s", filepath.ToSlash(rel))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if strings.Contains(tpl.Body, "{{ClassName}}") {
		name := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		if !isIdentifier(name) {
			return "", fmt.Errorf("%q no sirve como nombre de clase", name)
		}
	}
	return abs, nil
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}

// submitPath acepta la ruta y pasa a pedir las variables o a crear.
func (m Model) submitPath() (Model, tea.Cmd) {
	raw := strings.TrimSpace(string(m.pal.query))
	abs, err := m.resolveNewPath(raw, m.nf.tpl)
	if err != nil {
		m.pal.note = err.Error()
		return m, nil
	}
	m.nf.raw, m.nf.abs = raw, abs
	m.nf.vars = map[string]string{}
	m.nf.ask = append([]string(nil), m.nf.tpl.Vars...)
	return m.nextVar()
}

// nextVar pide la siguiente variable o, si no queda ninguna, crea el archivo.
func (m Model) nextVar() (Model, tea.Cmd) {
	if len(m.nf.ask) == 0 {
		return m.createFromTemplate()
	}
	m.pal = palette{open: true, mode: palVar, prompt: m.nf.ask[0] + ": "}
	return m, nil
}

// submitVar guarda el valor de la variable pedida y sigue.
func (m Model) submitVar() (Model, tea.Cmd) {
	m.nf.vars[m.nf.ask[0]] = string(m.pal.query)
	m.nf.ask = m.nf.ask[1:]
	return m.nextVar()
}

// createFromTemplate renderiza la plantilla y crea el archivo sin pisar uno
// existente. Si falla, vuelve al paso de la ruta con el error.
func (m Model) createFromTemplate() (Model, tea.Cmd) {
	nf := m.nf
	fail := func(err error) (Model, tea.Cmd) {
		m.askPath(nf.raw, err.Error())
		return m, nil
	}
	text, err := templates.Render(nf.tpl, templates.Context{Root: m.root, TargetPath: nf.abs, Vars: nf.vars})
	if err != nil {
		return fail(errors.New(strings.TrimPrefix(err.Error(), "templates: ")))
	}
	// Un archivo en el paquete por defecto deriva "" y dejaría "package ;".
	text = strings.Replace(text, "package ;\n\n", "", 1)
	text = strings.Replace(text, "package ;\n", "", 1)

	root, err := os.OpenRoot(m.root)
	if err != nil {
		return fail(err)
	}
	defer root.Close()

	rel, err := filepath.Rel(m.root, nf.abs)
	if err != nil {
		return fail(err)
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		return fail(err)
	}
	// El watcher también ve la escritura: se avisa antes para que no se la
	// atribuya a un agente en marcha.
	m.chat.NoteUserWrite(nf.abs)
	f, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fail(fmt.Errorf("ya existe %s", m.display(nf.abs)))
		}
		return fail(err)
	}
	_, werr := f.WriteString(text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fail(werr)
	}

	m.pal = palette{}
	m.tree.Refresh()
	m.relayout()
	m.openFile(nf.abs)
	m.tree.Reveal(nf.abs)
	if m.hasEd && m.ed.Path() == nf.abs {
		m.msg = "creado " + m.display(nf.abs)
	} else {
		m.msg = "creado " + m.display(nf.abs) + " │ " + m.msg
	}
	return m, nil
}
