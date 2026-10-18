package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dpa23/wari/internal/agent"
)

// TestedVersion es la versión de la CLI contra la que se verificó el
// adaptador. Solo error_unauthenticated.jsonl es una grabación real; el
// resto del parser se escribió contra el esquema oficial (docs/codex-events.md)
// y sus fixtures son sintéticos (testdata/codex/SYNTHETIC.md).
const TestedVersion = "0.163.0"

// MinVersion es la versión mínima soportada: la probada.
const MinVersion = TestedVersion

// parser traduce las líneas de un turno. Lleva estado porque un mismo item
// llega varias veces (started/updated/completed) y porque Done necesita el
// hilo y el último mensaje. Se crea uno por turno.
type parser struct {
	thread   string
	seen     map[string]bool // items cuyo ToolCall ya se emitió
	messages int             // agent_message ya entregados
	last     string          // texto del último agent_message
	lastErr  string          // último mensaje de error ya emitido
	unknown  map[string]int  // tipos desconocidos pero bien formados
}

func newParser() *parser {
	return &parser{seen: map[string]bool{}, unknown: map[string]int{}}
}

// ParseLine traduce una línea suelta con un parser nuevo (sin memoria de
// líneas anteriores). Un turno real usa Driver.Parse.
func ParseLine(line []byte) ([]agent.Event, error) { return newParser().parse(line) }

func formatErr(format string, args ...any) error {
	return fmt.Errorf("codex %s: %s", TestedVersion, fmt.Sprintf(format, args...))
}

// item es el campo item de item.started/updated/completed. Solo se decodifica
// lo que el adaptador usa; los demás campos del esquema se ignoran.
type item struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Command   string          `json:"command"`
	Status    string          `json:"status"`
	Changes   json.RawMessage `json:"changes"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Query     string          `json:"query"`
	Message   string          `json:"message"`
}

type usage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
}

func (p *parser) parse(line []byte) ([]agent.Event, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, nil
	}
	var m struct {
		Type     string `json:"type"`
		ThreadID string `json:"thread_id"`
		Message  string `json:"message"`
		Usage    *usage `json:"usage"`
		Error    *struct {
			Message string `json:"message"`
		} `json:"error"`
		Item *item `json:"item"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, formatErr("línea inválida: %v", err)
	}
	if m.Type == "" {
		return nil, formatErr("línea sin campo \"type\"")
	}

	switch m.Type {
	case "thread.started":
		if m.ThreadID == "" {
			return nil, formatErr("thread.started sin thread_id")
		}
		p.thread = m.ThreadID
		return []agent.Event{agent.Started{SessionID: m.ThreadID}}, nil

	case "turn.started":
		return nil, nil

	case "turn.completed":
		return p.completed(m.Usage), nil

	case "turn.failed":
		msg := "el turno falló sin mensaje"
		if m.Error != nil && m.Error.Message != "" {
			msg = m.Error.Message
		}
		var events []agent.Event
		// El error de nivel superior que precede a turn.failed suele traer
		// el mismo mensaje: no se repite.
		if msg != p.lastErr {
			events = append(events, errEvent(msg))
		}
		return append(events, agent.Done{SessionID: p.thread, Result: p.last, IsError: true, Turns: 1}), nil

	case "error":
		// "Reconnecting... n/5 (...)" es un aviso de reintento: si el
		// reintento falla del todo, turn.failed lleva la causa.
		if strings.HasPrefix(m.Message, "Reconnecting...") {
			return nil, nil
		}
		if m.Message == "" {
			return nil, formatErr("error sin message")
		}
		p.lastErr = m.Message
		return []agent.Event{errEvent(m.Message)}, nil

	case "item.started", "item.updated", "item.completed":
		if m.Item == nil || m.Item.Type == "" || m.Item.ID == "" {
			return nil, formatErr("%s sin item.id o item.type", m.Type)
		}
		return p.item(strings.TrimPrefix(m.Type, "item."), m.Item), nil

	default:
		// Evento desconocido pero JSON válido: se cuenta y se ignora para
		// que una CLI más nueva que TestedVersion no rompa el turno.
		p.unknown[m.Type]++
		return nil, nil
	}
}

func errEvent(msg string) agent.Event {
	return agent.Error{Err: errors.New("codex: " + msg)}
}

func (p *parser) completed(u *usage) []agent.Event {
	var events []agent.Event
	if u != nil {
		events = append(events, agent.Usage{
			InputTokens:      u.Input,
			OutputTokens:     u.Output + u.Reasoning,
			CacheReadTokens:  u.Cached,
			CacheWriteTokens: u.CacheWrite,
		})
	}
	return append(events, agent.Done{SessionID: p.thread, Result: p.last, Turns: 1})
}

// item traduce un item. phase es "started", "updated" o "completed".
func (p *parser) item(phase string, it *item) []agent.Event {
	switch it.Type {
	case "agent_message":
		if phase != "completed" {
			return nil
		}
		text := it.Text
		p.last = text
		// Varios mensajes del mismo turno se separan con una línea en blanco
		// para que el texto concatenado de los TextDelta siga legible.
		if p.messages > 0 {
			text = "\n\n" + text
		}
		p.messages++
		return []agent.Event{agent.TextDelta{Text: text}}

	case "command_execution":
		events := p.toolCall(it, "shell", marshal(map[string]string{"command": it.Command}))
		if phase == "completed" && it.Status == "declined" {
			events = append(events, agent.NeedsApproval{
				ToolCallID: it.ID,
				Tool:       "shell",
				Input:      marshal(map[string]string{"command": it.Command}),
				Denied:     true,
			})
		}
		return events

	case "file_change":
		// Llega ya ejecutado: ToolCall sí, FileEditProposed no (ver la nota
		// del paquete).
		if phase != "completed" {
			return nil
		}
		changes := it.Changes
		if len(changes) == 0 {
			changes = json.RawMessage("[]")
		}
		return p.toolCall(it, "apply_patch", marshal(map[string]json.RawMessage{"changes": changes}))

	case "mcp_tool_call":
		args := it.Arguments
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		return p.toolCall(it, "mcp__"+it.Server+"__"+it.Tool, args)

	case "web_search":
		if phase != "completed" && it.Query == "" {
			return nil // la consulta aún no se conoce
		}
		return p.toolCall(it, "web_search", marshal(map[string]string{"query": it.Query}))

	case "error":
		if phase != "completed" {
			return nil
		}
		if it.Message == "" {
			return nil
		}
		p.lastErr = it.Message
		return []agent.Event{errEvent(it.Message)}

	case "reasoning", "todo_list", "collab_tool_call":
		// Sin evento: no hay equivalente normalizado (ver docs/codex-events.md).
		return nil

	default:
		p.unknown["item:"+it.Type]++
		return nil
	}
}

// toolCall emite el ToolCall de un item la primera vez que se ve, aunque sea
// en item.completed (si el started no llegó).
func (p *parser) toolCall(it *item, name string, input json.RawMessage) []agent.Event {
	if p.seen[it.ID] {
		return nil
	}
	p.seen[it.ID] = true
	return []agent.Event{agent.ToolCall{ID: it.ID, Name: name, Input: input}}
}

func marshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
