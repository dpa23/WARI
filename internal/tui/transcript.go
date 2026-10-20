package tui

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/workspace/owner"
)

// heavyTurnTokens es el gasto de un turno a partir del cual se avisa.
const heavyTurnTokens = 100_000

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
	// turnTokens cuenta los tokens del turno en curso, para avisar de los caros.
	turnTokens int64
	// lastTool y lastToolN colapsan las llamadas idénticas consecutivas a una
	// herramienta en una sola línea ("⚙ read_file x ×2").
	lastTool  string
	lastToolN int
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
		m.entries[len(m.entries)-1].text = m.relPaths(stripFileLinks(m.asst.String()))
	case agent.ToolCall:
		text := "⚙ " + m.relPaths(toolSummary(ev.Name, ev.Input))
		if last := len(m.entries) - 1; last >= 0 && m.entries[last].kind == kindTool && m.lastTool == text {
			m.lastToolN++
			m.entries[last].text = text + " ×" + strconv.Itoa(m.lastToolN)
			break
		}
		m.add(kindTool, text)
		m.lastTool, m.lastToolN = text, 1
	case agent.FileEditProposed:
		m.add(kindEdit, "✎ "+m.display(ev.Path))
	case agent.NeedsApproval:
		if ev.Denied {
			m.add(kindDenied, "⛔ denegado: "+m.relPaths(toolSummary(ev.Tool, ev.Input)))
		}
	case agent.Usage:
		m.tokensIn += ev.InputTokens
		m.tokensOut += ev.OutputTokens
		m.turnTokens += ev.InputTokens + ev.OutputTokens
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
		if m.turnTokens >= heavyTurnTokens {
			m.add(kindInfo, fmt.Sprintf("este turno gastó %s tokens (el contexto inicial del agente cuenta); con una suscripción pesa en tu límite de uso", compactTokens(m.turnTokens)))
		}
		m.turnTokens = 0
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

// relPaths quita el prefijo de la raíz del proyecto de las rutas que aparecen
// en un texto (también en enlaces file://), porque ocupaban dos o tres líneas.
func (m *transcript) relPaths(s string) string {
	if m.root == "" || m.root == "/" {
		return s
	}
	root := strings.TrimSuffix(m.root, "/") + "/"
	return strings.ReplaceAll(strings.ReplaceAll(s, "file://"+root, ""), root, "")
}

var fileLink = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)

// stripFileLinks deja solo el texto de los enlaces Markdown a archivos locales
// (`[`a.go`](a.go#L3)` → `a.go`): en una columna estrecha el destino se partía
// a mitad de la ruta y ocupaba tres líneas. Los enlaces http(s) se conservan.
func stripFileLinks(s string) string {
	return fileLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := fileLink.FindStringSubmatch(m)
		if strings.HasPrefix(sub[2], "http://") || strings.HasPrefix(sub[2], "https://") {
			return m
		}
		return sub[1]
	})
}

// add agrega una entrada y cierra el mensaje del asistente en curso.
func (m *transcript) add(k entryKind, text string) {
	m.entries = append(m.entries, entry{kind: k, text: text})
	m.asstOpen = false
}
