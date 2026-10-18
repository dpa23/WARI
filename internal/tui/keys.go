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

	// Panel multiagente: crear, seleccionar, pausar/reanudar y detener
	// agentes, y la parada global.
	AgentNew   key.Binding
	AgentNext  key.Binding
	AgentPrev  key.Binding
	AgentPause key.Binding
	AgentStop  key.Binding
	StopAll    key.Binding

	// Seguridad de cambios (fase 6): ver el diff de un archivo editado contra
	// su checkpoint y revertirlo (archivo, turno o agente completo).
	DiffFile    key.Binding
	RevertFile  key.Binding
	RevertTurn  key.Binding
	RevertAgent key.Binding
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
		AgentNew:   b("agent_new", "agente nuevo"),
		AgentNext:  b("agent_next", "siguiente"),
		AgentPrev:  b("agent_prev", "anterior"),
		AgentPause: b("agent_pause", "pausar"),
		AgentStop:  b("agent_stop", "detener"),
		StopAll:    b("stop_all", "parar todo"),

		DiffFile:    b("diff_file", "ver diff"),
		RevertFile:  b("revert_file", "revertir archivo"),
		RevertTurn:  b("revert_turn", "revertir turno"),
		RevertAgent: b("revert_agent", "revertir agente"),
	}
}

// helpLine es la ayuda breve de la línea de estado, construida con los
// bindings vigentes: "enter enviar · esc cancelar · ctrl+n sesión nueva · ctrl+c salir".
func (k KeyMap) helpLine() string {
	return joinHelp(k.Send, k.Cancel, k.NewSession, k.Quit)
}

// panelHelpLine es la ayuda breve del panel multiagente.
func (k KeyMap) panelHelpLine() string {
	return joinHelp(k.Send, k.Cancel, k.AgentNew, k.AgentPause, k.AgentStop, k.Quit)
}

func joinHelp(bs ...key.Binding) string {
	s := ""
	for _, b := range bs {
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
