package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dpa23/wari/internal/agent"
)

// TestedVersion es la versión de la CLI con la que se grabaron los fixtures.
const TestedVersion = "2.1.302"

// MinVersion es la versión mínima soportada: la probada. Las anteriores
// pueden no tener --permission-prompts ni --permission-mode manual.
const MinVersion = TestedVersion

// ParseLine traduce una línea de `claude -p --output-format stream-json
// --verbose --include-partial-messages` a eventos normalizados. Es pura y sin
// estado. Devuelve (nil, nil) para líneas conocidas que no producen eventos.
//
// Si una línea de tipo conocido es defectuosa pero ya se pudieron traducir
// parte de sus eventos (p. ej. un Write sin file_path), se devuelven esos
// eventos junto con el error.
func ParseLine(line []byte) ([]agent.Event, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, nil
	}

	var head struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, formatErr("línea inválida: %v", err)
	}
	if head.Type == "" {
		return nil, formatErr("línea sin campo \"type\"")
	}

	switch head.Type {
	case "system":
		return parseSystem(line, head.Subtype)
	case "stream_event":
		return parseStreamEvent(line)
	case "assistant":
		return parseAssistant(line)
	case "result":
		return parseResult(line)

	case "user", "rate_limit_event":
		// Conocidos, sin evento: los resultados de herramientas y los límites
		// de tasa no se exponen.
		return nil, nil
	default:
		// Tipo desconocido pero JSON válido: se tolera para que una CLI más
		// nueva que TestedVersion no rompa el turno.
		return nil, nil
	}
}

// formatErr construye un error que nombra la CLI y la versión probada.
func formatErr(format string, args ...any) error {
	return fmt.Errorf("claude %s: %s", TestedVersion, fmt.Sprintf(format, args...))
}

func parseSystem(line []byte, subtype string) ([]agent.Event, error) {
	switch subtype {
	case "init":
		var m struct {
			SessionID *string `json:"session_id"`
			Model     string  `json:"model"`
		}
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, formatErr("system/init inválido: %v", err)
		}
		if m.SessionID == nil || *m.SessionID == "" {
			return nil, formatErr("system/init sin session_id")
		}
		return []agent.Event{agent.Started{SessionID: *m.SessionID, Model: m.Model}}, nil
	case "status", "permission_denied", "thinking_tokens":
		// Conocidos, sin evento. Las denegaciones llegan completas en
		// result.permission_denials.
		return nil, nil
	default:
		return nil, nil
	}
}

func parseStreamEvent(line []byte) ([]agent.Event, error) {
	var m struct {
		ParentToolUseID *string `json:"parent_tool_use_id"`
		Event           *struct {
			Type  string `json:"type"`
			Delta *struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			} `json:"delta"`
		} `json:"event"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, formatErr("stream_event inválido: %v", err)
	}
	if m.Event == nil || m.Event.Type == "" {
		return nil, formatErr("stream_event sin event.type")
	}
	if m.Event.Type != "content_block_delta" || m.Event.Delta == nil || m.Event.Delta.Type != "text_delta" {
		// message_start, content_block_start/stop, message_delta, message_stop,
		// thinking_delta, input_json_delta, signature_delta...: sin evento.
		return nil, nil
	}
	if isSubagent(m.ParentToolUseID) {
		return nil, nil
	}
	if m.Event.Delta.Text == nil {
		return nil, formatErr("text_delta sin text")
	}
	return []agent.Event{agent.TextDelta{Text: *m.Event.Delta.Text}}, nil
}

func isSubagent(parentToolUseID *string) bool {
	return parentToolUseID != nil && *parentToolUseID != ""
}

// fileEditTools son las herramientas que editan un archivo y la clave del
// input que contiene su ruta.
var fileEditTools = map[string]string{
	"Write":        "file_path",
	"Edit":         "file_path",
	"MultiEdit":    "file_path",
	"NotebookEdit": "notebook_path",
}

func parseAssistant(line []byte) ([]agent.Event, error) {
	var m struct {
		Message *struct {
			Content []struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, formatErr("assistant inválido: %v", err)
	}
	if m.Message == nil {
		return nil, formatErr("assistant sin message")
	}

	var events []agent.Event
	var errs []error
	for _, b := range m.Message.Content {
		if b.Type != "tool_use" {
			// text (ya llegó como deltas), thinking y demás: sin evento.
			continue
		}
		if b.ID == "" || b.Name == "" {
			errs = append(errs, formatErr("tool_use sin id o name"))
			continue
		}
		events = append(events, agent.ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})

		key, isEdit := fileEditTools[b.Name]
		if !isEdit {
			continue
		}
		path, err := stringField(b.Input, key)
		if err != nil || path == "" {
			errs = append(errs, formatErr("tool_use %s (%s) sin input.%s", b.Name, b.ID, key))
			continue
		}
		events = append(events, agent.FileEditProposed{ToolCallID: b.ID, Tool: b.Name, Path: path})
	}
	return events, errors.Join(errs...)
}

// stringField lee la ruta (file_path o notebook_path) del input de una
// herramienta. Decodifica solo esos campos: el input de un Write puede traer
// el archivo entero.
func stringField(raw json.RawMessage, key string) (string, error) {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	if key == "notebook_path" {
		return in.NotebookPath, nil
	}
	return in.FilePath, nil
}

func parseResult(line []byte) ([]agent.Event, error) {
	var m struct {
		SessionID      *string `json:"session_id"`
		Subtype        string  `json:"subtype"`
		IsError        bool    `json:"is_error"`
		Result         string  `json:"result"`
		NumTurns       int     `json:"num_turns"`
		TotalCostUSD   float64 `json:"total_cost_usd"`
		TerminalReason string  `json:"terminal_reason"`
		Usage          struct {
			InputTokens      int64 `json:"input_tokens"`
			OutputTokens     int64 `json:"output_tokens"`
			CacheReadTokens  int64 `json:"cache_read_input_tokens"`
			CacheWriteTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		PermissionDenials []struct {
			ToolName  string          `json:"tool_name"`
			ToolUseID string          `json:"tool_use_id"`
			ToolInput json.RawMessage `json:"tool_input"`
		} `json:"permission_denials"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, formatErr("result inválido: %v", err)
	}
	if m.SessionID == nil || *m.SessionID == "" {
		return nil, formatErr("result sin session_id")
	}

	events := []agent.Event{agent.Usage{
		InputTokens:      m.Usage.InputTokens,
		OutputTokens:     m.Usage.OutputTokens,
		CacheReadTokens:  m.Usage.CacheReadTokens,
		CacheWriteTokens: m.Usage.CacheWriteTokens,
		CostUSD:          m.TotalCostUSD,
	}}
	// Una denegación defectuosa se reporta pero no tira el resultado: sin
	// Done, el turno parecería no haber terminado.
	var errs []error
	for _, d := range m.PermissionDenials {
		if d.ToolName == "" || d.ToolUseID == "" {
			errs = append(errs, formatErr("permission_denials con elemento sin tool_name o tool_use_id"))
			continue
		}
		events = append(events, agent.NeedsApproval{
			ToolCallID: d.ToolUseID,
			Tool:       d.ToolName,
			Input:      d.ToolInput,
			Denied:     true,
		})
	}
	// is_error es la fuente de verdad: con un modelo inexistente la CLI
	// reporta subtype "success" pero is_error true.
	if m.IsError {
		text := m.Result
		if text == "" {
			text = fmt.Sprintf("la CLI terminó con error (subtype %q, terminal_reason %q)", m.Subtype, m.TerminalReason)
		}
		events = append(events, agent.Error{Err: errors.New(text)})
	}
	events = append(events, agent.Done{
		SessionID: *m.SessionID,
		Result:    m.Result,
		IsError:   m.IsError,
		Turns:     m.NumTurns,
	})
	return events, errors.Join(errs...)
}
