package tui

import (
	"charm.land/bubbles/v2/key"

	"github.com/dpa23/wari/internal/config"
)

// KeyMap reúne los atajos del panel de chat. Se cargan desde la
// configuración (NewKeyMap); Update solo consulta estos bindings, nunca teclas sueltas.
type KeyMap struct {
	// Send envía el mensaje de la entrada.
	Send key.Binding
	// Newline inserta un salto de línea en la entrada.
	Newline key.Binding
	// Cancel cancela el turno en curso; el agente sigue vivo.
	Cancel key.Binding
	// NewSession descarta la sesión actual (y cancela el turno en curso).
	NewSession key.Binding
	// Quit sale del programa cerrando al agente.
	Quit key.Binding
	// PageUp y PageDown desplazan el historial.
	PageUp   key.Binding
	PageDown key.Binding
}

// DefaultKeyMap devuelve los atajos predeterminados (los de config.Default).
func DefaultKeyMap() KeyMap { return NewKeyMap(config.Default()) }

// NewKeyMap construye los atajos del panel a partir del mapa de la
// configuración. Una acción ausente o sin teclas deja el binding sin teclas
// (deshabilitado), salvo que km sea nil: entonces se usan los predeterminados.
func NewKeyMap(km config.Keymap) KeyMap {
	if km == nil {
		km = config.Default()
	}
	b := func(action, desc string) key.Binding {
		keys := km[action]
		if len(keys) == 0 {
			return key.NewBinding(key.WithDisabled())
		}
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], desc))
	}
	return KeyMap{
		Send:       b("send", "enviar"),
		Newline:    b("newline", "nueva línea"),
		Cancel:     b("cancel", "cancelar"),
		NewSession: b("new_session", "sesión nueva"),
		Quit:       b("quit", "salir"),
		PageUp:     b("page_up", "subir"),
		PageDown:   b("page_down", "bajar"),
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
