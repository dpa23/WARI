package editorview

import "charm.land/bubbles/v2/key"

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
	// Tab inserta cuatro espacios.
	Tab key.Binding

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

		Save: key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "guardar")),
		Undo: key.NewBinding(key.WithKeys("ctrl+z"), key.WithHelp("ctrl+z", "deshacer")),
		Redo: key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "rehacer")),
	}
}
