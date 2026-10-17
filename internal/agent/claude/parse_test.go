package claude

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

const fixtureDir = "../../../testdata/claude"

// parseFixture parsea todas las líneas de un fixture y devuelve los eventos
// en orden. Falla el test si alguna línea produce error.
func parseFixture(t *testing.T, name string) []agent.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var events []agent.Event
	for i, line := range bytes.Split(data, []byte("\n")) {
		evs, err := ParseLine(line)
		if err != nil {
			t.Fatalf("%s línea %d: %v", name, i+1, err)
		}
		events = append(events, evs...)
	}
	return events
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

// sequence devuelve los tipos de evento en orden, colapsando las repeticiones
// consecutivas de TextDelta en una sola entrada.
func sequence(events []agent.Event) []string {
	var seq []string
	for _, e := range events {
		k := kind(e)
		if k == "TextDelta" && len(seq) > 0 && seq[len(seq)-1] == "TextDelta" {
			continue
		}
		seq = append(seq, k)
	}
	return seq
}

func joinText(events []agent.Event) string {
	var sb strings.Builder
	for _, e := range events {
		if td, ok := e.(agent.TextDelta); ok {
			sb.WriteString(td.Text)
		}
	}
	return sb.String()
}

// find devuelve el primer evento de tipo T.
func find[T agent.Event](t *testing.T, events []agent.Event) T {
	t.Helper()
	for _, e := range events {
		if v, ok := e.(T); ok {
			return v
		}
	}
	var zero T
	t.Fatalf("no hay evento %T en %v", zero, sequence(events))
	return zero
}

func TestFixtures(t *testing.T) {
	// El sanitizador numera los UUID en orden de aparición; el ID de sesión
	// es el primero de cada fixture.
	const sid = "00000000-0000-0000-0000-000000000001"
	tests := []struct {
		file       string
		seq        []string
		sessionID  string
		model      string
		tools      []string
		turns      int
		isError    bool
		usageTotal bool // tokens de entrada/salida > 0
		textIsDone bool // la concatenación de TextDelta es igual a Done.Result
	}{
		{
			file: "simple.jsonl", seq: []string{"Started", "TextDelta", "Usage", "Done"},
			sessionID: sid, model: "claude-haiku-5-5", turns: 1, usageTotal: true, textIsDone: true,
		},
		{
			file: "resume.jsonl", seq: []string{"Started", "TextDelta", "Usage", "Done"},
			sessionID: sid, model: "claude-haiku-5-5", turns: 1, usageTotal: true, textIsDone: true,
		},
		{
			file: "tool_read.jsonl", seq: []string{"Started", "ToolCall", "TextDelta", "Usage", "Done"},
			sessionID: sid, model: "claude-haiku-5-5", tools: []string{"Read"}, turns: 2, usageTotal: true, textIsDone: true,
		},
		{
			file: "bash_write_auto.jsonl", seq: []string{"Started", "ToolCall", "TextDelta", "Usage", "Done"},
			sessionID: sid, model: "claude-haiku-5-5", tools: []string{"Bash"}, turns: 2, usageTotal: true, textIsDone: true,
		},
		{
			file:      "write_denied.jsonl",
			seq:       []string{"Started", "ToolCall", "FileEditProposed", "TextDelta", "Usage", "NeedsApproval", "Done"},
			sessionID: sid, model: "claude-haiku-5-5", tools: []string{"Write"}, turns: 2, usageTotal: true, textIsDone: true,
		},
		{
			file: "error_model.jsonl", seq: []string{"Started", "Usage", "Error", "Done"},
			sessionID: sid, model: "modelo-inexistente", turns: 1, isError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			events := parseFixture(t, tc.file)

			got := sequence(events)
			if strings.Join(got, ",") != strings.Join(tc.seq, ",") {
				t.Fatalf("secuencia = %v, quiero %v", got, tc.seq)
			}

			started := find[agent.Started](t, events)
			if started.SessionID != tc.sessionID || started.Model != tc.model {
				t.Errorf("Started = %+v, quiero sesión %q modelo %q", started, tc.sessionID, tc.model)
			}
			if _, ok := events[0].(agent.Started); !ok {
				t.Errorf("el primer evento es %s, quiero Started", kind(events[0]))
			}

			var tools []string
			for _, e := range events {
				if tc, ok := e.(agent.ToolCall); ok {
					tools = append(tools, tc.Name)
					if tc.ID == "" || !json.Valid(tc.Input) {
						t.Errorf("ToolCall mal formado: %+v", tc)
					}
				}
			}
			if strings.Join(tools, ",") != strings.Join(tc.tools, ",") {
				t.Errorf("herramientas = %v, quiero %v", tools, tc.tools)
			}

			done := find[agent.Done](t, events)
			if done.SessionID != tc.sessionID || done.Turns != tc.turns || done.IsError != tc.isError {
				t.Errorf("Done = %+v, quiero sesión %q turnos %d isError %v", done, tc.sessionID, tc.turns, tc.isError)
			}
			if done.Result == "" {
				t.Error("Done.Result vacío")
			}
			if _, ok := events[len(events)-1].(agent.Done); !ok {
				t.Errorf("el último evento es %s, quiero Done", kind(events[len(events)-1]))
			}

			usage := find[agent.Usage](t, events)
			if tc.usageTotal && (usage.InputTokens <= 0 || usage.OutputTokens <= 0 || usage.CostUSD <= 0) {
				t.Errorf("Usage sin consumo: %+v", usage)
			}
			if !tc.usageTotal && (usage != agent.Usage{}) {
				t.Errorf("Usage = %+v, quiero cero", usage)
			}

			if tc.textIsDone {
				if text := joinText(events); text != done.Result {
					t.Errorf("TextDelta concatenado = %q, Done.Result = %q", text, done.Result)
				}
			}
		})
	}
}

func TestFixtureSpecifics(t *testing.T) {
	t.Run("simple: texto hola", func(t *testing.T) {
		events := parseFixture(t, "simple.jsonl")
		if got := joinText(events); got != "hola" {
			t.Errorf("texto = %q, quiero %q", got, "hola")
		}
	})

	t.Run("tool_read: Read no propone edición", func(t *testing.T) {
		events := parseFixture(t, "tool_read.jsonl")
		for _, e := range events {
			if _, ok := e.(agent.FileEditProposed); ok {
				t.Fatal("Read no debe producir FileEditProposed")
			}
		}
		tc := find[agent.ToolCall](t, events)
		var in struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(tc.Input, &in); err != nil || !strings.HasSuffix(in.FilePath, "nota.txt") {
			t.Errorf("input de Read = %s (%v)", tc.Input, err)
		}
	})

	t.Run("bash_write_auto: sin denegación", func(t *testing.T) {
		events := parseFixture(t, "bash_write_auto.jsonl")
		for _, e := range events {
			switch e.(type) {
			case agent.NeedsApproval, agent.FileEditProposed, agent.Error:
				t.Fatalf("evento inesperado %s", kind(e))
			}
		}
	})

	t.Run("write_denied: edición y denegación", func(t *testing.T) {
		events := parseFixture(t, "write_denied.jsonl")
		call := find[agent.ToolCall](t, events)
		edit := find[agent.FileEditProposed](t, events)
		if edit.ToolCallID != call.ID || edit.Tool != "Write" || edit.Path != "/tmp/wari-fixture/hola.txt" {
			t.Errorf("FileEditProposed = %+v (llamada %s)", edit, call.ID)
		}
		na := find[agent.NeedsApproval](t, events)
		if na.Tool != "Write" || !na.Denied || na.ToolCallID != call.ID {
			t.Errorf("NeedsApproval = %+v", na)
		}
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(na.Input, &in); err != nil || in.FilePath != edit.Path || in.Content != "hola" {
			t.Errorf("input de NeedsApproval = %s (%v)", na.Input, err)
		}
	})

	t.Run("error_model: Error antes de Done", func(t *testing.T) {
		events := parseFixture(t, "error_model.jsonl")
		e := find[agent.Error](t, events)
		done := find[agent.Done](t, events)
		if e.Err == nil || e.Err.Error() != done.Result || !done.IsError {
			t.Errorf("Error = %v, Done = %+v", e.Err, done)
		}
		if !strings.Contains(e.Err.Error(), "modelo-inexistente") {
			t.Errorf("el error no menciona el modelo: %v", e.Err)
		}
	})
}

func TestParseLineSynthetic(t *testing.T) {
	noEvents := []struct {
		name string
		line string
	}{
		{"vacía", ""},
		{"solo espacios", "  \t \r"},
		{"type desconocido", `{"type":"algo_nuevo","x":1}`},
		{"system status", `{"type":"system","subtype":"status","status":"requesting"}`},
		{"system subtype desconocido", `{"type":"system","subtype":"nuevo"}`},
		{"system permission_denied", `{"type":"system","subtype":"permission_denied"}`},
		{"user", `{"type":"user","message":{"content":[{"type":"tool_result"}]}}`},
		{"rate_limit_event", `{"type":"rate_limit_event"}`},
		{"stream_event message_start", `{"type":"stream_event","event":{"type":"message_start"},"parent_tool_use_id":null}`},
		{"stream_event input_json_delta", `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}}`},
		{"stream_event thinking_delta", `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"x"}}}`},
		{"text_delta de subagente", `{"type":"stream_event","parent_tool_use_id":"toolu_abc","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"sub"}}}`},
		{"assistant solo texto", `{"type":"assistant","message":{"content":[{"type":"text","text":"hola"}]}}`},
	}
	for _, tc := range noEvents {
		t.Run(tc.name, func(t *testing.T) {
			events, err := ParseLine([]byte(tc.line))
			if err != nil || events != nil {
				t.Errorf("ParseLine = %v, %v; quiero nil, nil", events, err)
			}
		})
	}

	t.Run("text_delta con parent_tool_use_id null o ausente", func(t *testing.T) {
		for _, line := range []string{
			`{"type":"stream_event","parent_tool_use_id":null,"event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}}}`,
			`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"a"}}}`,
		} {
			events, err := ParseLine([]byte(line))
			if err != nil || len(events) != 1 || events[0] != (agent.TextDelta{Text: "a"}) {
				t.Errorf("ParseLine(%s) = %v, %v", line, events, err)
			}
		}
	})

	t.Run("línea con espacios alrededor", func(t *testing.T) {
		events, err := ParseLine([]byte(" \t" + `{"type":"system","subtype":"init","session_id":"s1","model":"m"}` + "\r\n"))
		if err != nil || len(events) != 1 || events[0] != (agent.Started{SessionID: "s1", Model: "m"}) {
			t.Errorf("ParseLine = %v, %v", events, err)
		}
	})

	t.Run("tool_use de subagente también propone edición", func(t *testing.T) {
		line := `{"type":"assistant","parent_tool_use_id":"toolu_p","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/a/b.go","old_string":"x","new_string":"y"}}]}}`
		events, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if got := sequence(events); strings.Join(got, ",") != "ToolCall,FileEditProposed" {
			t.Fatalf("secuencia = %v", got)
		}
		if edit := events[1].(agent.FileEditProposed); edit != (agent.FileEditProposed{ToolCallID: "t1", Tool: "Edit", Path: "/a/b.go"}) {
			t.Errorf("FileEditProposed = %+v", edit)
		}
	})

	t.Run("herramientas de edición y su ruta", func(t *testing.T) {
		cases := []struct{ tool, input, path string }{
			{"Write", `{"file_path":"/w"}`, "/w"},
			{"Edit", `{"file_path":"/e"}`, "/e"},
			{"MultiEdit", `{"file_path":"/m","edits":[]}`, "/m"},
			{"NotebookEdit", `{"notebook_path":"/n.ipynb","new_source":"x"}`, "/n.ipynb"},
		}
		for _, c := range cases {
			line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"` + c.tool + `","input":` + c.input + `}]}}`
			events, err := ParseLine([]byte(line))
			if err != nil || len(events) != 2 {
				t.Fatalf("%s: %v, %v", c.tool, events, err)
			}
			if edit := events[1].(agent.FileEditProposed); edit.Path != c.path || edit.Tool != c.tool {
				t.Errorf("%s: FileEditProposed = %+v", c.tool, edit)
			}
		}
	})

	t.Run("varios tool_use en un assistant", func(t *testing.T) {
		line := `{"type":"assistant","message":{"content":[{"type":"text","text":"x"},{"type":"tool_use","id":"a","name":"Read","input":{}},{"type":"tool_use","id":"b","name":"Write","input":{"file_path":"/f"}}]}}`
		events, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if got := sequence(events); strings.Join(got, ",") != "ToolCall,ToolCall,FileEditProposed" {
			t.Errorf("secuencia = %v", got)
		}
	})

	t.Run("Write sin file_path conserva el ToolCall y da error", func(t *testing.T) {
		line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Write","input":{}}]}}`
		events, err := ParseLine([]byte(line))
		if err == nil || len(events) != 1 {
			t.Fatalf("ParseLine = %v, %v", events, err)
		}
		if _, ok := events[0].(agent.ToolCall); !ok {
			t.Errorf("evento = %s, quiero ToolCall", kind(events[0]))
		}
	})

	t.Run("result con is_error sin texto", func(t *testing.T) {
		line := `{"type":"result","subtype":"error_max_turns","session_id":"s","is_error":true,"num_turns":3,"usage":{}}`
		events, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if got := sequence(events); strings.Join(got, ",") != "Usage,Error,Done" {
			t.Fatalf("secuencia = %v", got)
		}
		if msg := events[1].(agent.Error).Err.Error(); !strings.Contains(msg, "error_max_turns") {
			t.Errorf("mensaje de error poco claro: %q", msg)
		}
	})

	t.Run("result con subtype success e is_error false no emite Error", func(t *testing.T) {
		line := `{"type":"result","subtype":"success","session_id":"s","is_error":false,"result":"ok","num_turns":1,"total_cost_usd":0.5,"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}`
		events, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		want := []agent.Event{
			agent.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4, CostUSD: 0.5},
			agent.Done{SessionID: "s", Result: "ok", Turns: 1},
		}
		if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
			t.Errorf("eventos = %+v", events)
		}
	})
}

func TestResultWithBadDenialKeepsDone(t *testing.T) {
	line := `{"type":"result","session_id":"s","result":"ok","num_turns":1,"permission_denials":[` +
		`{"tool_name":"Write"},` +
		`{"tool_name":"Edit","tool_use_id":"t2","tool_input":{}}]}`
	events, err := ParseLine([]byte(line))
	if err == nil {
		t.Error("quiero un error por la denegación incompleta")
	}
	got := sequence(events)
	if want := []string{"Usage", "NeedsApproval", "Done"}; !slices.Equal(got, want) {
		t.Fatalf("eventos = %v, quiero %v", got, want)
	}
	if a := events[1].(agent.NeedsApproval); a.ToolCallID != "t2" || a.Tool != "Edit" {
		t.Errorf("NeedsApproval = %+v, quiero la denegación válida", a)
	}
}

func TestParseLineErrors(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"JSON inválido", `{"type":`},
		{"texto plano", `esto no es json`},
		{"JSON que no es objeto", `[1,2,3]`},
		{"sin type", `{"subtype":"init"}`},
		{"type vacío", `{"type":""}`},
		{"null", `null`},
		{"init sin session_id", `{"type":"system","subtype":"init","model":"m"}`},
		{"init con session_id vacío", `{"type":"system","subtype":"init","session_id":""}`},
		{"result sin session_id", `{"type":"result","is_error":false,"result":"x"}`},
		{"result con denial incompleto", `{"type":"result","session_id":"s","permission_denials":[{"tool_name":"Write"}]}`},
		{"stream_event sin event", `{"type":"stream_event"}`},
		{"text_delta sin text", `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta"}}}`},
		{"assistant sin message", `{"type":"assistant"}`},
		{"tool_use sin id", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events, err := ParseLine([]byte(tc.line))
			if err == nil {
				t.Fatalf("quiero error, obtuve eventos %v", events)
			}
			if !strings.Contains(err.Error(), "claude "+TestedVersion) {
				t.Errorf("el error no nombra la CLI y la versión: %v", err)
			}
		})
	}
}
