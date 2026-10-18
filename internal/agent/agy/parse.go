package agy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dpa23/wari/internal/agent"
)

// TestedVersion es la versión de la CLI con la que se grabaron los fixtures.
const TestedVersion = "1.4.2"

// MinVersion es la versión mínima soportada: la probada. denied_actions y el
// resto del formato no se verificaron en versiones anteriores.
const MinVersion = TestedVersion

// ParseLine traduce una línea de `agy -p --output-format stream-json` a
// eventos normalizados. Es pura y sin estado. Devuelve (nil, nil) para líneas
// conocidas que no producen eventos.
//
// Si una línea de tipo conocido es defectuosa pero ya se pudieron traducir
// parte de sus eventos (p. ej. una edición sin TargetFile), se devuelven esos
// eventos junto con el error.
func ParseLine(line []byte) ([]agent.Event, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, nil
	}

	var head struct {
		Event string `json:"event"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, formatErr("línea inválida: %v", err)
	}
	if head.Event == "" {
		return nil, formatErr("línea sin campo \"event\"")
	}

	switch head.Event {
	case "init":
		return parseInit(line)
	case "step_update":
		return parseStep(line)
	case "result":
		return parseResult(line)
	default:
		// Evento desconocido pero JSON válido: se tolera para que una CLI
		// más nueva que TestedVersion no rompa el turno.
		return nil, nil
	}
}

// formatErr construye un error que nombra la CLI y la versión probada.
func formatErr(format string, args ...any) error {
	return fmt.Errorf("agy %s: %s", TestedVersion, fmt.Sprintf(format, args...))
}

func notFoundErr(want, got string) error {
	return fmt.Errorf("%w: %s (agy abrió la conversación %s)", agent.ErrSessionNotFound, want, got)
}

func joinErr(a, b error) error { return errors.Join(a, b) }

func parseInit(line []byte) ([]agent.Event, error) {
	var m struct {
		ConversationID string `json:"conversation_id"`
		Init           *struct {
			Model string `json:"model"`
		} `json:"init"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, formatErr("init inválido: %v", err)
	}
	if m.ConversationID == "" {
		return nil, formatErr("init sin conversation_id")
	}
	var model string
	if m.Init != nil {
		model = m.Init.Model
	}
	return []agent.Event{agent.Started{SessionID: m.ConversationID, Model: model}}, nil
}

// fileEditTools son las herramientas que editan un archivo del proyecto y la
// clave de sus parámetros con la ruta. write_to_file y replace_file_content
// están verificadas con la CLI real. multi_replace_file_content no se pudo
// provocar en modo print (el modelo contesta que no la tiene); se incluye por
// ser de la misma familia, y sin TargetFile se reporta un error en lugar de
// perder el checkpoint en silencio.
var fileEditTools = map[string]string{
	"write_to_file":              "TargetFile",
	"replace_file_content":       "TargetFile",
	"multi_replace_file_content": "TargetFile",
}

// ownStore reconoce los archivos que agy guarda en su propio directorio de
// trabajo (planes, walkthroughs): ~/.gemini/antigravity-cli/brain/<conversación>/.
// No son del proyecto y no deben contar como edición.
func ownStore(path string) bool {
	p := strings.ReplaceAll(path, `\`, "/")
	return strings.Contains(p, "/.gemini/antigravity-cli/brain/")
}

func parseStep(line []byte) ([]agent.Event, error) {
	var m struct {
		StepUpdate *struct {
			StepIndex *int      `json:"step_index"`
			State     string    `json:"state"`
			StepType  string    `json:"step_type"`
			TextDelta string    `json:"text_delta"`
			ToolName  string    `json:"tool_name"`
			ToolInfo  *toolInfo `json:"tool_info"`
		} `json:"step_update"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, formatErr("step_update inválido: %v", err)
	}
	s := m.StepUpdate
	if s == nil || s.StepType == "" {
		return nil, formatErr("step_update sin step_update.step_type")
	}

	switch s.StepType {
	case "agent_response":
		// El texto llega en deltas tanto en ACTIVE como en el DONE final
		// (que trae el salto de línea de cierre); concatenados dan
		// result.response.
		if s.TextDelta == "" {
			return nil, nil
		}
		return []agent.Event{agent.TextDelta{Text: s.TextDelta}}, nil

	case "tool":
		// Cada herramienta llega dos veces (ACTIVE y DONE); solo ACTIVE
		// produce eventos. El DONE de una herramienta denegada no se
		// distingue del normal: la denegación llega en result.denied_actions.
		if s.State != "ACTIVE" {
			return nil, nil
		}
		return parseTool(s.StepIndex, s.ToolName, s.ToolInfo)

	default:
		// user_input, system_message y los tipos futuros: sin evento.
		return nil, nil
	}
}

// toolInfo es step_update.tool_info.
type toolInfo struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
}

func parseTool(index *int, name string, info *toolInfo) ([]agent.Event, error) {
	if name == "" && info != nil {
		name = info.Name
	}
	if index == nil || name == "" {
		return nil, formatErr("herramienta sin step_index o tool_name")
	}
	id := fmt.Sprintf("step-%d", *index)
	var input json.RawMessage
	if info != nil {
		input = info.Parameters
	}
	events := []agent.Event{agent.ToolCall{ID: id, Name: name, Input: input}}

	key, isEdit := fileEditTools[name]
	if !isEdit {
		return events, nil
	}
	path, err := stringParam(input, key)
	if err != nil || path == "" {
		return events, formatErr("herramienta %s (%s) sin parameters.%s", name, id, key)
	}
	if ownStore(path) {
		return events, nil
	}
	return append(events, agent.FileEditProposed{ToolCallID: id, Tool: name, Path: path}), nil
}

// stringParam lee un parámetro de texto de la herramienta. Decodifica solo ese
// campo: los parámetros de una edición pueden traer el archivo entero.
func stringParam(raw json.RawMessage, key string) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("sin parámetros")
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	v, ok := in[key]
	if !ok {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", err
	}
	return s, nil
}

func parseResult(line []byte) ([]agent.Event, error) {
	var m struct {
		Result *struct {
			ConversationID string `json:"conversation_id"`
			Status         string `json:"status"`
			Response       string `json:"response"`
			Error          string `json:"error"`
			NumTurns       int    `json:"num_turns"`
			Usage          struct {
				InputTokens     int64 `json:"input_tokens"`
				OutputTokens    int64 `json:"output_tokens"`
				CacheReadTokens int64 `json:"cache_read_tokens"`
			} `json:"usage"`
			DeniedActions []struct {
				Action      string `json:"action"`
				DisplayName string `json:"display_name"`
			} `json:"denied_actions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, formatErr("result inválido: %v", err)
	}
	r := m.Result
	if r == nil {
		return nil, formatErr("result sin result")
	}
	if r.Status == "" {
		return nil, formatErr("result sin status")
	}

	events := []agent.Event{agent.Usage{
		InputTokens:     r.Usage.InputTokens,
		OutputTokens:    r.Usage.OutputTokens,
		CacheReadTokens: r.Usage.CacheReadTokens,
	}}
	// La CLI deniega sola los permisos que no puede preguntar en modo print.
	// No dice qué herramienta ni con qué argumentos: solo la acción
	// ("command") y su nombre para mostrar.
	var errs []error
	for _, d := range r.DeniedActions {
		name := d.DisplayName
		if name == "" {
			name = d.Action
		}
		if name == "" {
			errs = append(errs, formatErr("denied_actions con elemento sin action ni display_name"))
			continue
		}
		events = append(events, agent.NeedsApproval{Tool: name, Denied: true})
	}
	// status manda: con un modelo inexistente la CLI responde ERROR, sin init
	// ni conversation_id, con el texto en result.error.
	isErr := r.Status != "SUCCESS"
	if isErr {
		text := r.Error
		if text == "" {
			text = r.Response
		}
		if text == "" {
			text = fmt.Sprintf("la CLI terminó con status %q", r.Status)
		}
		events = append(events, agent.Error{Err: errors.New(text)})
	}
	events = append(events, agent.Done{
		SessionID: r.ConversationID,
		Result:    r.Response,
		IsError:   isErr,
		Turns:     r.NumTurns,
	})
	return events, errors.Join(errs...)
}
