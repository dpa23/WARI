package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dpa23/wari/internal/agent"
)

// parser traduce las líneas JSON de un turno de opencode y mantiene el estado
// del turno (sesión, texto acumulado, contador de pasos, errores). Se crea uno por turno.
type parser struct {
	sessionID  string
	started    bool
	stepStarts int
	hasError   bool
	lastError  string
	text       strings.Builder
	unknown    map[string]int
}

func newParser() *parser {
	return &parser{
		unknown: make(map[string]int),
	}
}

// ParseLine traduce una línea suelta con un parser nuevo (sin estado previo).
func ParseLine(line []byte) ([]agent.Event, error) {
	return newParser().parse(line)
}

func formatErr(format string, args ...any) error {
	return fmt.Errorf("opencode %s: %s", TestedVersion, fmt.Sprintf(format, args...))
}

type eventHead struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	SessionID string          `json:"sessionID"`
	Part      json.RawMessage `json:"part"`
	Error     *errorInfo      `json:"error"`
}

type errorInfo struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type textPart struct {
	Text string `json:"text"`
}

type toolPart struct {
	ID    string     `json:"id"`
	Tool  string     `json:"tool"`
	State *toolState `json:"state"`
}

type toolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
}

type stepFinishPart struct {
	Cost   float64     `json:"cost"`
	Tokens *tokensInfo `json:"tokens"`
}

type tokensInfo struct {
	Input     int64     `json:"input"`
	Output    int64     `json:"output"`
	Reasoning int64     `json:"reasoning"`
	Cache     cacheInfo `json:"cache"`
}

type cacheInfo struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

func (p *parser) parse(line []byte) ([]agent.Event, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, nil
	}

	var head eventHead
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, formatErr("línea inválida: %v", err)
	}
	if head.Type == "" {
		return nil, formatErr("línea sin campo \"type\"")
	}

	var events []agent.Event

	// Started{SessionID} se emite una sola vez por turno con el primer sessionID no vacío.
	if head.SessionID != "" {
		p.sessionID = head.SessionID
		if !p.started {
			p.started = true
			events = append(events, agent.Started{SessionID: head.SessionID})
		}
	}

	switch head.Type {
	case "step_start":
		p.stepStarts++
		return events, nil

	case "text":
		if len(head.Part) == 0 {
			return events, formatErr("text sin part")
		}
		var part textPart
		if err := json.Unmarshal(head.Part, &part); err != nil {
			return events, formatErr("text inválido: %v", err)
		}
		p.text.WriteString(part.Text)
		events = append(events, agent.TextDelta{Text: part.Text})
		return events, nil

	case "tool_use":
		if len(head.Part) == 0 {
			return events, formatErr("tool_use sin part")
		}
		var part toolPart
		if err := json.Unmarshal(head.Part, &part); err != nil {
			return events, formatErr("tool_use inválido: %v", err)
		}
		if part.ID == "" || part.Tool == "" {
			return events, formatErr("tool_use sin id o tool")
		}
		if part.State == nil {
			return events, formatErr("tool_use sin state")
		}
		// tool_use con part.state.status completed -> ToolCall
		// NO emite FileEditProposed: la herramienta ya se ejecutó y un checkpoint capturaría
		// el archivo ya editado.
		if part.State.Status == "completed" {
			events = append(events, agent.ToolCall{
				ID:    part.ID,
				Name:  part.Tool,
				Input: part.State.Input,
			})
		}
		return events, nil

	case "step_finish":
		if len(head.Part) == 0 {
			return events, formatErr("step_finish sin part")
		}
		var part stepFinishPart
		if err := json.Unmarshal(head.Part, &part); err != nil {
			return events, formatErr("step_finish inválido: %v", err)
		}
		if part.Tokens == nil {
			return events, formatErr("step_finish sin tokens")
		}
		// OutputTokens = output + reasoning
		events = append(events, agent.Usage{
			InputTokens:      part.Tokens.Input,
			OutputTokens:     part.Tokens.Output + part.Tokens.Reasoning,
			CacheReadTokens:  part.Tokens.Cache.Read,
			CacheWriteTokens: part.Tokens.Cache.Write,
			CostUSD:          part.Cost,
		})
		return events, nil

	case "error":
		p.hasError = true
		var msg string
		if head.Error != nil && head.Error.Message != "" {
			msg = head.Error.Message
		} else if head.Error != nil && head.Error.Type != "" {
			msg = head.Error.Type
		} else {
			msg = "error desconocido"
		}
		p.lastError = msg
		events = append(events, agent.Error{Err: errors.New(msg)})
		return events, nil

	default:
		// Evento desconocido pero JSON bien formado: se ignora para tolerar versiones futuras.
		p.unknown[head.Type]++
		return events, nil
	}
}

func (p *parser) finish(exitErr error, stderr string) *agent.Done {
	if p.hasError {
		return &agent.Done{
			SessionID: p.sessionID,
			Result:    p.lastError,
			IsError:   true,
		}
	}
	if exitErr != nil {
		return nil
	}
	return &agent.Done{
		SessionID: p.sessionID,
		Result:    p.text.String(),
		Turns:     p.stepStarts,
	}
}
