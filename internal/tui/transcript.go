package tui

import (
	"errors"
	"strings"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/workspace/owner"
)

// transcript es el historial de un agente y su consumo acumulado. Lo comparten
// el chat de un solo agente (Model) y el panel multiagente (Panel).
type transcript struct {
	entries []entry
	// asstOpen indica que el último entry es el mensaje del asistente en
	// curso y los TextDelta se agregan a él; asst acumula su texto.
	asstOpen bool
	asst     *strings.Builder
	cache    historyCache

	busy      bool
	sessionID string
	model     string
	tokensIn  int64
	tokensOut int64
	costUSD   float64

	// newSessionKey es la tecla que empieza una sesión nueva, para el aviso
	// de sesión no encontrada.
	newSessionKey string
	// root es la raíz del proyecto: las rutas editadas dentro de ella se
	// muestran relativas.
	root string
}

// applyEvent aplica un evento del agente al historial y al estado. No vuelca
// al viewport: quien llama hace un solo refresh por lote.
func (m *transcript) applyEvent(ev agent.Event) {
	switch ev := ev.(type) {
	case agent.Started:
		if ev.SessionID != "" {
			m.sessionID = ev.SessionID
		}
		if ev.Model != "" {
			m.model = ev.Model
		}
	case agent.TextDelta:
		if !m.asstOpen || len(m.entries) == 0 || m.asst == nil {
			m.entries = append(m.entries, entry{kind: kindAssistant})
			m.asst = &strings.Builder{}
			m.asstOpen = true
		}
		m.asst.WriteString(ev.Text)
		m.entries[len(m.entries)-1].text = m.asst.String()
	case agent.ToolCall:
		m.add(kindTool, "⚙ "+toolSummary(ev.Name, ev.Input))
	case agent.FileEditProposed:
		m.add(kindEdit, "✎ "+m.display(ev.Path))
	case agent.NeedsApproval:
		if ev.Denied {
			m.add(kindDenied, "⛔ denegado: "+toolSummary(ev.Tool, ev.Input))
		}
	case agent.Usage:
		m.tokensIn += ev.InputTokens
		m.tokensOut += ev.OutputTokens
		m.costUSD += ev.CostUSD
	case agent.Error:
		msg := "error desconocido"
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		m.add(kindError, "error: "+msg)
		if errors.Is(ev.Err, agent.ErrSessionNotFound) {
			m.add(kindInfo, "no se pudo reanudar la sesión; "+m.newSessionKey+" empieza una nueva")
		}
	case agent.Done:
		if ev.SessionID != "" {
			m.sessionID = ev.SessionID
		}
		m.busy = false
		m.asstOpen = false
		if ev.IsError {
			text := "el turno terminó con error"
			if ev.Result != "" {
				text += ": " + ev.Result
			}
			m.add(kindError, text)
		}
	}
}

// display muestra p relativa a la raíz si está dentro de ella.
func (m *transcript) display(p string) string {
	if m.root == "" {
		return p
	}
	if rel, inside := owner.Rel(m.root, p); inside {
		return rel
	}
	return p
}

// add agrega una entrada y cierra el mensaje del asistente en curso.
func (m *transcript) add(k entryKind, text string) {
	m.entries = append(m.entries, entry{kind: k, text: text})
	m.asstOpen = false
}
