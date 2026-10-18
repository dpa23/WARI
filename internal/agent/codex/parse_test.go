package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dpa23/wari/internal/agent"
)

const fixtureDir = "../../../testdata/codex"

// Tanto la grabación real como los sintéticos usan este thread_id.
const tid = "00000000-0000-0000-0000-000000000001"

// parseFixture parsea un fixture entero con un parser de turno y devuelve los
// eventos, los errores de formato y el parser (para ver los tipos ignorados).
func parseFixture(t *testing.T, name string) ([]agent.Event, []error, *parser) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	p := newParser()
	var events []agent.Event
	var errs []error
	for i, line := range bytes.Split(data, []byte("\n")) {
		evs, err := p.parse(line)
		if err != nil {
			errs = append(errs, err)
			t.Logf("%s línea %d: %v", name, i+1, err)
		}
		events = append(events, evs...)
	}
	return events, errs, p
}

func mustParse(t *testing.T, name string) ([]agent.Event, *parser) {
	t.Helper()
	events, errs, p := parseFixture(t, name)
	if len(errs) > 0 {
		t.Fatalf("%s: %v", name, errs)
	}
	return events, p
}

func kind(e agent.Event) string {
	switch e.(type) {
	case agent.Started:
		return "Started"
	case agent.TextDelta:
		return "TextDelta"
	case agent.ToolCall:
		return "ToolCall"
	case agent.FileEditProposed:
		return "FileEditProposed"
	case agent.Usage:
		return "Usage"
	case agent.NeedsApproval:
		return "NeedsApproval"
	case agent.Done:
		return "Done"
	case agent.Error:
		return "Error"
	}
	return "?"
}

func sequence(events []agent.Event) []string {
	var seq []string
	for _, e := range events {
		seq = append(seq, kind(e))
	}
	return seq
}

func joinText(events []agent.Event) string {
	var b strings.Builder
	for _, e := range events {
		if d, ok := e.(agent.TextDelta); ok {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

func find[T agent.Event](t *testing.T, events []agent.Event) T {
	t.Helper()
	for _, e := range events {
		if v, ok := e.(T); ok {
			return v
		}
	}
	var zero T
	t.Fatalf("no hay un %T en %v", zero, sequence(events))
	return zero
}

func TestRealUnauthenticated(t *testing.T) {
	// Única grabación real: sin login, solo avisos de reconexión (que no
	// producen eventos), un item de error y el fallo del turno.
	events, p := mustParse(t, "error_unauthenticated.jsonl")
	if got := sequence(events); !slices.Equal(got, []string{"Started", "Error", "Error", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	if s := events[0].(agent.Started); s.SessionID != tid {
		t.Errorf("Started = %+v", s)
	}
	// El item de error trae el aviso del cambio de transporte; el mensaje
	// final llega una sola vez aunque aparezca en error y en turn.failed.
	if e := events[1].(agent.Error); !strings.Contains(e.Err.Error(), "Falling back") {
		t.Errorf("primer Error = %v", e.Err)
	}
	e := events[2].(agent.Error)
	if !strings.Contains(e.Err.Error(), "401 Unauthorized") || !strings.HasPrefix(e.Err.Error(), "codex: ") {
		t.Errorf("segundo Error = %v", e.Err)
	}
	if d := events[3].(agent.Done); !d.IsError || d.SessionID != tid || d.Turns != 1 {
		t.Errorf("Done = %+v", d)
	}
	if len(p.unknown) != 0 {
		t.Errorf("tipos desconocidos en la grabación real: %v", p.unknown)
	}
}

func TestSimple(t *testing.T) {
	events, _ := mustParse(t, "synthetic_simple.jsonl")
	if got := sequence(events); !slices.Equal(got, []string{"Started", "TextDelta", "Usage", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	// OutputTokens suma el razonamiento; la caché se reporta aparte.
	want := agent.Usage{InputTokens: 1200, OutputTokens: 50, CacheReadTokens: 800, CacheWriteTokens: 50}
	if u := find[agent.Usage](t, events); u != want {
		t.Errorf("Usage = %+v, quiero %+v", u, want)
	}
	if d := find[agent.Done](t, events); d != (agent.Done{SessionID: tid, Result: "hola", Turns: 1}) {
		t.Errorf("Done = %+v", d)
	}
}

func TestUsageWithoutCacheWrite(t *testing.T) {
	// cache_write_input_tokens tiene valor por defecto 0 en el esquema.
	events, _ := mustParse(t, "synthetic_file_change_accept.jsonl")
	want := agent.Usage{InputTokens: 2500, OutputTokens: 80}
	if u := find[agent.Usage](t, events); u != want {
		t.Errorf("Usage = %+v, quiero %+v", u, want)
	}
}

func TestCommand(t *testing.T) {
	events, _ := mustParse(t, "synthetic_command.jsonl")
	// started y completed del mismo comando dan un solo ToolCall.
	want := []string{"Started", "ToolCall", "TextDelta", "TextDelta", "Usage", "Done"}
	if got := sequence(events); !slices.Equal(got, want) {
		t.Fatalf("secuencia = %v", got)
	}
	call := find[agent.ToolCall](t, events)
	if call.ID != "item_0" || call.Name != "shell" {
		t.Errorf("ToolCall = %+v", call)
	}
	var in struct{ Command string }
	if err := json.Unmarshal(call.Input, &in); err != nil || in.Command != "/bin/bash -lc 'cat nota.txt'" {
		t.Errorf("Input = %s (%v)", call.Input, err)
	}
	// Dos mensajes del mismo turno se separan; Result es el último.
	if text := joinText(events); text != "Leí nota.txt.\n\ncontenido de prueba 42" {
		t.Errorf("texto = %q", text)
	}
	if d := find[agent.Done](t, events); d.Result != "contenido de prueba 42" {
		t.Errorf("Result = %q", d.Result)
	}
}

func TestFileChangeIsToolCallWithoutEditProposed(t *testing.T) {
	events, _ := mustParse(t, "synthetic_file_change_accept.jsonl")
	if got := sequence(events); !slices.Equal(got, []string{"Started", "ToolCall", "TextDelta", "Usage", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	call := find[agent.ToolCall](t, events)
	if call.Name != "apply_patch" || call.ID != "item_0" {
		t.Errorf("ToolCall = %+v", call)
	}
	var in struct {
		Changes []struct{ Path, Kind string }
	}
	if err := json.Unmarshal(call.Input, &in); err != nil || len(in.Changes) != 1 ||
		in.Changes[0].Path != "/tmp/wari-codex-fixture/hola.txt" || in.Changes[0].Kind != "add" {
		t.Errorf("Input = %s (%v)", call.Input, err)
	}
	for _, e := range events {
		if _, ok := e.(agent.FileEditProposed); ok {
			t.Error("file_change no debe emitir FileEditProposed: llega ya ejecutado")
		}
	}
}

func TestDeclinedCommand(t *testing.T) {
	events, _ := mustParse(t, "synthetic_declined.jsonl")
	want := []string{"Started", "ToolCall", "NeedsApproval", "TextDelta", "Usage", "Done"}
	if got := sequence(events); !slices.Equal(got, want) {
		t.Fatalf("secuencia = %v", got)
	}
	n := find[agent.NeedsApproval](t, events)
	if !n.Denied || n.ToolCallID != "item_0" || n.Tool != "shell" || !strings.Contains(string(n.Input), "rm -rf build") {
		t.Errorf("NeedsApproval = %+v", n)
	}
}

func TestTodoAndReasoningProduceNoEvents(t *testing.T) {
	events, p := mustParse(t, "synthetic_todo_reasoning.jsonl")
	if got := sequence(events); !slices.Equal(got, []string{"Started", "TextDelta", "Usage", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	if len(p.unknown) != 0 {
		t.Errorf("reasoning/todo_list cuentan como desconocidos: %v", p.unknown)
	}
}

func TestMcpWebSearchAndUnknownTypes(t *testing.T) {
	events, p := mustParse(t, "synthetic_tools_misc.jsonl")
	want := []string{"Started", "ToolCall", "ToolCall", "TextDelta", "Usage", "Done"}
	if got := sequence(events); !slices.Equal(got, want) {
		t.Fatalf("secuencia = %v", got)
	}
	mcp := events[1].(agent.ToolCall)
	if mcp.Name != "mcp__docs__search" || string(mcp.Input) != `{"q":"wari"}` {
		t.Errorf("mcp ToolCall = %+v (%s)", mcp, mcp.Input)
	}
	// web_search sin consulta (started) se omite; el completed la trae.
	web := events[2].(agent.ToolCall)
	if web.Name != "web_search" || web.ID != "item_1" || string(web.Input) != `{"query":"codex exec json"}` {
		t.Errorf("web_search ToolCall = %+v (%s)", web, web.Input)
	}
	// Los tipos nuevos se ignoran y se cuentan.
	if !slices.Equal(mapKeys(p.unknown), []string{"future.event", "item:future_item"}) ||
		p.unknown["future.event"] != 1 || p.unknown["item:future_item"] != 1 {
		t.Errorf("desconocidos = %v", p.unknown)
	}
}

func mapKeys(m map[string]int) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func TestTurnFailed(t *testing.T) {
	events, _ := mustParse(t, "synthetic_turn_failed.jsonl")
	// error + turn.failed con el mismo mensaje: un solo Error.
	if got := sequence(events); !slices.Equal(got, []string{"Started", "Error", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
	if e := events[1].(agent.Error); !strings.Contains(e.Err.Error(), "model overloaded") {
		t.Errorf("Error = %v", e.Err)
	}
	if d := events[2].(agent.Done); !d.IsError {
		t.Errorf("Done = %+v, quiero IsError", d)
	}
}

func TestTurnFailedWithoutPriorError(t *testing.T) {
	events, err := ParseLine([]byte(`{"type":"turn.failed","error":{"message":"boom"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := sequence(events); !slices.Equal(got, []string{"Error", "Done"}) {
		t.Fatalf("secuencia = %v", got)
	}
}

func TestToolCallWithoutStartedIsEmittedOnCompleted(t *testing.T) {
	events, err := ParseLine([]byte(`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"ls","status":"declined"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := sequence(events); !slices.Equal(got, []string{"ToolCall", "NeedsApproval"}) {
		t.Errorf("secuencia = %v", got)
	}
}

func TestReconnectingIsInformational(t *testing.T) {
	events, err := ParseLine([]byte(`{"type":"error","message":"Reconnecting... 2/5 (x)"}`))
	if events != nil || err != nil {
		t.Errorf("Reconnecting = %v, %v; quiero nada", events, err)
	}
}

func TestMalformedLines(t *testing.T) {
	for name, line := range map[string]string{
		"no es json":             `no es json`,
		"sin type":               `{"foo":1}`,
		"thread sin id":          `{"type":"thread.started"}`,
		"item sin id":            `{"type":"item.completed","item":{"type":"agent_message","text":"x"}}`,
		"item ausente":           `{"type":"item.started"}`,
		"usage con tipo erróneo": `{"type":"turn.completed","usage":{"input_tokens":"x"}}`,
		"error sin mensaje":      `{"type":"error"}`,
		"item de tipo no objeto": `{"type":"item.completed","item":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			events, err := ParseLine([]byte(line))
			if err == nil || !strings.Contains(err.Error(), TestedVersion) || !strings.HasPrefix(err.Error(), "codex ") {
				t.Errorf("err = %v, quiero un error que nombre codex y la versión", err)
			}
			if len(events) != 0 {
				t.Errorf("eventos = %v", events)
			}
		})
	}
	if events, err := ParseLine([]byte("  \n")); events != nil || err != nil {
		t.Errorf("línea vacía = %v, %v", events, err)
	}
}
