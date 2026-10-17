package tui

import "charm.land/bubbles/v2/key"

// KeyMap reúne los atajos del panel de chat. En la fase 4 se cargarán desde
// la configuración; Update solo consulta estos bindings, nunca teclas sueltas.
type KeyMap struct {
	// Send envía el mensaje de la entrada.
	Send key.Binding
	// Newline inserta un salto de línea en la entrada.
	Newline key.Binding
	// Cancel cancela el turno en curso.
	Cancel key.Binding
	// NewSession descarta la sesión actual y empieza una nueva.
	NewSession key.Binding
	// Quit sale del programa matando al agente.
	Quit key.Binding
	// PageUp y PageDown desplazan el historial.
	PageUp   key.Binding
	PageDown key.Binding
}

// DefaultKeyMap devuelve los atajos predeterminados.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Send:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "enviar")),
		Newline:    key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"), key.WithHelp("alt+enter", "nueva línea")),
		Cancel:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancelar")),
		NewSession: key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("ctrl+n", "sesión nueva")),
		Quit:       key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "salir")),
		PageUp:     key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "subir")),
		PageDown:   key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "bajar")),
	}
}

// helpLine es la ayuda breve de la línea de estado, construida con los
// bindings vigentes: "enter enviar · esc cancelar · ctrl+n sesión nueva · ctrl+c salir".
func (k KeyMap) helpLine() string {
	s := ""
	for _, b := range []key.Binding{k.Send, k.Cancel, k.NewSession, k.Quit} {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		if s != "" {
			s += " · "
		}
		s += h.Key + " " + h.Desc
	}
	return s
}
