package editorview

import (
	"charm.land/bubbles/v2/key"

	"github.com/dpa23/wari/internal/config"
)

// KeyMap reúne los atajos del editor. Update solo consulta estos bindings
// (salvo el texto imprimible y el pegado). Cada movimiento tiene una variante
// Sel* que extiende la selección en lugar de colapsarla.
type KeyMap struct {
	// Movimiento del cursor.
	Left, Right, Up, Down    key.Binding
	WordLeft, WordRight      key.Binding
	LineStart, LineEnd       key.Binding
	DocStart, DocEnd         key.Binding
	PageUp, PageDown         key.Binding
	SelLeft, SelRight        key.Binding
	SelUp, SelDown           key.Binding
	SelWordLeft              key.Binding
	SelWordRight             key.Binding
	SelLineStart, SelLineEnd key.Binding
	SelDocStart, SelDocEnd   key.Binding
	SelPageUp, SelPageDown   key.Binding
	// SelectAll selecciona todo el buffer.
	SelectAll key.Binding

	// Edición.
	Newline   key.Binding
	Backspace key.Binding
	Delete    key.Binding
	// Tab expande el snippet que precede al cursor o, si no hay, inserta
	// cuatro espacios; en una sesión de snippet va a la parada siguiente.
	Tab key.Binding
	// SnippetPrev vuelve a la parada anterior de una sesión de snippet y
	// Cancel la termina.
	SnippetPrev key.Binding
	Cancel      key.Binding

	// Save guarda en disco; Undo y Redo recorren el historial del buffer.
	Save key.Binding
	Undo key.Binding
	Redo key.Binding
}

// DefaultKeyMap devuelve los atajos predeterminados.
func DefaultKeyMap() KeyMap {
	b := func(keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], ""))
	}
	return KeyMap{
		Left:  b("left"),
		Right: b("right"),
		Up:    b("up"),
		Down:  b("down"),

		WordLeft:  b("ctrl+left", "alt+left"),
		WordRight: b("ctrl+right", "alt+right"),
		LineStart: b("home"),
		LineEnd:   b("end"),
		DocStart:  b("ctrl+home"),
		DocEnd:    b("ctrl+end"),
		PageUp:    b("pgup"),
		PageDown:  b("pgdown"),

		SelLeft:      b("shift+left"),
		SelRight:     b("shift+right"),
		SelUp:        b("shift+up"),
		SelDown:      b("shift+down"),
		SelWordLeft:  b("ctrl+shift+left", "alt+shift+left"),
		SelWordRight: b("ctrl+shift+right", "alt+shift+right"),
		SelLineStart: b("shift+home"),
		SelLineEnd:   b("shift+end"),
		SelDocStart:  b("ctrl+shift+home"),
		SelDocEnd:    b("ctrl+shift+end"),
		SelPageUp:    b("shift+pgup"),
		SelPageDown:  b("shift+pgdown"),
		SelectAll:    b("ctrl+a"),

		Newline:   b("enter"),
		Backspace: b("backspace"),
		Delete:    b("delete"),
		Tab:       b("tab"),

		SnippetPrev: b("shift+tab"),
		Cancel:      b("esc"),

		Save: key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "guardar")),
		Undo: key.NewBinding(key.WithKeys("ctrl+z"), key.WithHelp("ctrl+z", "deshacer")),
		Redo: key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "rehacer")),
	}
}

// KeyMapFrom parte de DefaultKeyMap y reemplaza Save, Undo, Redo, PageUp,
// PageDown, SnippetPrev y Cancel con las teclas de las acciones save, undo,
// redo, page_up, page_down, focus_prev y cancel de km. Las acciones ausentes conservan el valor predeterminado.
// El resto (movimiento, edición) es propio del componente.
func KeyMapFrom(km config.Keymap) KeyMap {
	k := DefaultKeyMap()
	over := func(dst *key.Binding, action, desc string) {
		if keys := km[action]; len(keys) > 0 {
			*dst = key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], desc))
		}
	}
	over(&k.Save, "save", "guardar")
	over(&k.Undo, "undo", "deshacer")
	over(&k.Redo, "redo", "rehacer")
	over(&k.PageUp, "page_up", "subir")
	over(&k.PageDown, "page_down", "bajar")
	over(&k.SnippetPrev, "focus_prev", "parada anterior")
	over(&k.Cancel, "cancel", "terminar snippet")
	return k
}
