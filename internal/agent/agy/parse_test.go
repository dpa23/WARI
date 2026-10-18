package agy

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

const fixtureDir = "../../../testdata/agy"

// El sanitizador numera los UUID en orden de aparición; el ID de la
// conversación es el primero de cada fixture.
const sid = "00000000-0000-0000-0000-000000000001"

// parseFixture parsea todas las líneas de un fixture y devuelve los eventos
// en orden. Falla el test si alguna línea produce error.
func parseFixture(t *testing.T, name string) []agent.Event {
	t.Helper()
	events, errs := parseFixtureErrs(t, name)
	if len(errs) > 0 {
		t.Fatalf("%s: %v", name, errs)
	}
	return events
}

func parseFixtureErrs(t *testing.T, name string) ([]agent.Event, []error) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var events []agent.Event
	var errs []error
	for i, line := range bytes.Split(data, []byte("\n")) {
		evs, err := ParseLine(line)
		if err != nil {
			errs = append(errs, err)
			t.Logf("%s línea %d: %v", name, i+1, err)
		}
		events = append(events, evs...)
	}
	return events, errs
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
	tests := []struct {
		file    string
		seq     []string
		tools   []string
		turns   int
		isError bool
		// textIsDone: la concatenación de TextDelta es igual a Done.Result.
		textIsDone bool
	}{
		{file: "simple.jsonl", seq: []string{"Started", "TextDelta", "Usage", "Done"}, turns: 1, textIsDone: true},
		{file: "resume.jsonl", seq: []string{"Started", "TextDelta", "Usage", "Done"}, turns: 2, textIsDone: true},
		{
			file: "tool_read.jsonl", seq: []string{"Started", "ToolCall", "TextDelta", "Usage", "Done"},
			tools: []string{"view_file"}, turns: 1, textIsDone: true,
		},
		{
			file:  "write_accept.jsonl",
			seq:   []string{"Started", "ToolCall", "FileEditProposed", "TextDelta", "Usage", "Done"},
			tools: []string{"write_to_file"}, turns: 1, textIsDone: true,
		},
		{
			file:  "edit_accept.jsonl",
			seq:   []string{"Started", "ToolCall", "ToolCall", "FileEditProposed", "TextDelta", "Usage", "Done"},
			tools: []string{"view_file", "replace_file_content"}, turns: 1, textIsDone: true,
		},
		{
			file:  "shell_denied.jsonl",
			seq:   []string{"Started", "ToolCall", "Usage", "NeedsApproval", "Done"},
			tools: []string{"run_command"}, turns: 1,
		},
		{
			file:  "write_plan.jsonl",
			seq:   []string{"Started", "ToolCall", "ToolCall", "Usage", "NeedsApproval", "Done"},
			tools: []string{"view_file", "run_command"}, turns: 1,
		},
		{
			// El plan y el walkthrough que agy guarda en su propio
			// directorio no cuentan como edición (solo uno de los tres
			// write_to_file propone edición: el del proyecto).
			file: "write_plan_explicit.jsonl",
			seq: []string{
				"Started", "ToolCall", "ToolCall", "FileEditProposed", "ToolCall", "ToolCall", "TextDelta", "Usage", "Done",
			},
			tools: []string{"write_to_file", "write_to_file", "view_file", "write_to_file"}, turns: 1, textIsDone: true,
		},
		{file: "resume_missing.jsonl", seq: []string{"Started", "ToolCall", "Usage", "NeedsApproval", "Done"}, tools: []string{"run_command"}, turns: 1},
		{file: "error_model.jsonl", seq: []string{"Usage", "Error", "Done"}, isError: true},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			events := parseFixture(t, tc.file)

			got := sequence(events)
			if !slices.Equal(got, tc.seq) {
				t.Fatalf("secuencia = %v, quiero %v", got, tc.seq)
			}

			var tools []string
			for _, e := range events {
				if c, ok := e.(agent.ToolCall); ok {
					tools = append(tools, c.Name)
					if c.ID == "" || !json.Valid(c.Input) {
						t.Errorf("ToolCall mal formado: %+v", c)
					}
				}
			}
			if !slices.Equal(tools, tc.tools) {
				t.Errorf("herramientas = %v, quiero %v", tools, tc.tools)
			}

			done := find[agent.Done](t, events)
			if done.Turns != tc.turns || done.IsError != tc.isError {
				t.Errorf("Done = %+v, quiero turnos %d isError %v", done, tc.turns, tc.isError)
			}
			if _, ok := events[len(events)-1].(agent.Done); !ok {
				t.Errorf("el último evento es %s, quiero Done", kind(events[len(events)-1]))
			}
			if !tc.isError {
				s := find[agent.Started](t, events)
				if s.SessionID != sid || done.SessionID != sid || s.Model != "gemini-3.8-flash-low" {
					t.Errorf("Started = %+v, Done = %+v, quiero sesión %q", s, done, sid)
				}
				if _, ok := events[0].(agent.Started); !ok {
					t.Errorf("el primer evento es %s, quiero Started", kind(events[0]))
				}
				u := find[agent.Usage](t, events)
				if u.InputTokens <= 0 || u.OutputTokens <= 0 {
					t.Errorf("Usage sin consumo: %+v", u)
				}
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
	t.Run("simple: texto y uso", func(t *testing.T) {
		events := parseFixture(t, "simple.jsonl")
		if got := joinText(events); got != "hola\n" {
			t.Errorf("texto = %q, quiero %q", got, "hola\n")
		}
		u := find[agent.Usage](t, events)
		want := agent.Usage{InputTokens: 12625, OutputTokens: 1}
		if u != want {
			t.Errorf("Usage = %+v, quiero %+v", u, want)
		}
	})

	t.Run("tool_read: view_file no propone edición", func(t *testing.T) {
		events := parseFixture(t, "tool_read.jsonl")
		for _, e := range events {
			if _, ok := e.(agent.FileEditProposed); ok {
				t.Fatal("view_file no debe producir FileEditProposed")
			}
		}
		c := find[agent.ToolCall](t, events)
		var in struct{ AbsolutePath string }
		if err := json.Unmarshal(c.Input, &in); err != nil || in.AbsolutePath != "/tmp/wari-agy-fixture/nota.txt" {
			t.Errorf("input de view_file = %s (%v)", c.Input, err)
		}
		if c.ID != "step-2" {
			t.Errorf("ToolCall.ID = %q, quiero step-2", c.ID)
		}
		if u := find[agent.Usage](t, events); u.CacheReadTokens != 8135 {
			t.Errorf("CacheReadTokens = %d, quiero 8135", u.CacheReadTokens)
		}
	})

	t.Run("write_accept y edit_accept: FileEditProposed con TargetFile", func(t *testing.T) {
		for file, tc := range map[string]struct{ tool, path string }{
			"write_accept.jsonl": {"write_to_file", "/tmp/wari-agy-fixture/hola.txt"},
			"edit_accept.jsonl":  {"replace_file_content", "/tmp/wari-agy-fixture/nota.txt"},
		} {
			events := parseFixture(t, file)
			edit := find[agent.FileEditProposed](t, events)
			var call agent.ToolCall
			for _, e := range events {
				if c, ok := e.(agent.ToolCall); ok && c.Name == tc.tool {
					call = c
				}
			}
			if edit.Tool != tc.tool || edit.Path != tc.path || edit.ToolCallID != call.ID || call.ID == "" {
				t.Errorf("%s: FileEditProposed = %+v, llamada %+v", file, edit, call)
			}
		}
	})

	t.Run("shell_denied: la denegación llega en el result", func(t *testing.T) {
		events := parseFixture(t, "shell_denied.jsonl")
		na := find[agent.NeedsApproval](t, events)
		if na.Tool != "RunCommand" || !na.Denied {
			t.Errorf("NeedsApproval = %+v", na)
		}
		// Done sigue siendo un éxito: la CLI terminó bien sin hacer nada.
		if d := find[agent.Done](t, events); d.IsError || d.Result != "" {
			t.Errorf("Done = %+v", d)
		}
	})

	t.Run("write_plan_explicit: --mode plan no impide escribir en el proyecto", func(t *testing.T) {
		events := parseFixture(t, "write_plan_explicit.jsonl")
		edit := find[agent.FileEditProposed](t, events)
		if edit.Path != "/tmp/wari-agy-fixture/plan_explicito.txt" {
			t.Errorf("FileEditProposed = %+v", edit)
		}
		// El plan y el walkthrough en el directorio de agy son ToolCall pero no ediciones.
		var writes int
		for _, e := range events {
			if c, ok := e.(agent.ToolCall); ok && c.Name == "write_to_file" {
				writes++
			}
		}
		if writes != 3 {
			t.Errorf("write_to_file = %d, quiero 3 (plan, proyecto y walkthrough)", writes)
		}
	})

	t.Run("resume: Started con la misma conversación", func(t *testing.T) {
		events := parseFixture(t, "resume.jsonl")
		if s := find[agent.Started](t, events); s.SessionID != sid {
			t.Errorf("Started = %+v", s)
		}
		if got := joinText(events); got != "hola\n" {
			t.Errorf("texto = %q", got)
		}
	})

	t.Run("error_model: Error antes de Done, sin sesión", func(t *testing.T) {
		events := parseFixture(t, "error_model.jsonl")
		e := find[agent.Error](t, events)
		done := find[agent.Done](t, events)
		if !done.IsError || done.SessionID != "" || done.Turns != 0 {
			t.Errorf("Done = %+v", done)
		}
		if !strings.Contains(e.Err.Error(), "modelo-inexistente") {
			t.Errorf("el error no menciona el modelo: %v", e.Err)
		}
	})
}

// TestFixtureEventCensus cuenta los eventos y tipos de step que el parser
// ignora a propósito, para que un tipo nuevo en los fixtures obligue a
// decidirlo.
func TestFixtureEventCensus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("sin fixtures: %v", err)
	}
	ignored := map[string]int{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var m struct {
				Event      string `json:"event"`
				StepUpdate struct {
					StepType string `json:"step_type"`
					State    string `json:"state"`
				} `json:"step_update"`
			}
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			switch {
			case m.Event == "init", m.Event == "result":
			case m.Event != "step_update":
				ignored["event "+m.Event]++
			case m.StepUpdate.StepType == "agent_response":
			case m.StepUpdate.StepType == "tool" && m.StepUpdate.State == "ACTIVE":
			case m.StepUpdate.StepType == "tool" && m.StepUpdate.State == "DONE":
				ignored["tool DONE"]++
			default:
				ignored["step "+m.StepUpdate.StepType]++
			}
		}
	}
	for k := range ignored {
		switch k {
		case "tool DONE", "step user_input", "step system_message":
		default:
			t.Errorf("tipo ignorado sin revisar: %s", k)
		}
	}
	if ignored["tool DONE"] == 0 || ignored["step user_input"] == 0 || ignored["step system_message"] == 0 {
		t.Errorf("los fixtures ya no cubren los tipos ignorados: %v", ignored)
	}
}

func TestParseLineSynthetic(t *testing.T) {
	noEvents := []struct {
		name string
		line string
	}{
		{"vacía", ""},
		{"solo espacios", "  \t \r"},
		{"event desconocido", `{"event":"algo_nuevo","x":1}`},
		{"step user_input", `{"event":"step_update","step_update":{"step_index":0,"state":"DONE","step_type":"user_input"}}`},
		{"step system_message", `{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"system_message"}}`},
		{"step de tipo desconocido", `{"event":"step_update","step_update":{"step_type":"nuevo","state":"ACTIVE","text_delta":"x"}}`},
		{"agent_response sin texto", `{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":1}}}`},
		{"tool DONE", `{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"write_to_file","tool_info":{"parameters":{}}}}`},
	}
	for _, tc := range noEvents {
		t.Run(tc.name, func(t *testing.T) {
			events, err := ParseLine([]byte(tc.line))
			if err != nil || events != nil {
				t.Errorf("ParseLine = %v, %v; quiero nil, nil", events, err)
			}
		})
	}

	t.Run("línea con espacios alrededor", func(t *testing.T) {
		events, err := ParseLine([]byte(" \t" + `{"event":"init","conversation_id":"c1","init":{"model":"m"}}` + "\r\n"))
		if err != nil || len(events) != 1 || events[0] != (agent.Started{SessionID: "c1", Model: "m"}) {
			t.Errorf("ParseLine = %v, %v", events, err)
		}
	})

	t.Run("init sin objeto init conserva la sesión", func(t *testing.T) {
		events, err := ParseLine([]byte(`{"event":"init","conversation_id":"c1"}`))
		if err != nil || len(events) != 1 || events[0] != (agent.Started{SessionID: "c1"}) {
			t.Errorf("ParseLine = %v, %v", events, err)
		}
	})

	t.Run("text_delta en ACTIVE y en DONE", func(t *testing.T) {
		for _, state := range []string{"ACTIVE", "DONE"} {
			line := `{"event":"step_update","step_update":{"step_index":1,"state":"` + state + `","step_type":"agent_response","text_delta":"a"}}`
			events, err := ParseLine([]byte(line))
			if err != nil || len(events) != 1 || events[0] != (agent.TextDelta{Text: "a"}) {
				t.Errorf("%s: ParseLine = %v, %v", state, events, err)
			}
		}
	})

	t.Run("herramienta sin tool_info solo da ToolCall", func(t *testing.T) {
		line := `{"event":"step_update","step_update":{"step_index":4,"state":"ACTIVE","step_type":"tool","tool_name":"list_dir"}}`
		events, err := ParseLine([]byte(line))
		if err != nil || len(events) != 1 {
			t.Fatalf("ParseLine = %v, %v", events, err)
		}
		if c := events[0].(agent.ToolCall); c.Name != "list_dir" || c.ID != "step-4" || c.Input != nil {
			t.Errorf("ToolCall = %+v", c)
		}
	})

	t.Run("edición dentro del directorio propio de agy no es edición del proyecto", func(t *testing.T) {
		line := `{"event":"step_update","step_update":{"step_index":4,"state":"ACTIVE","step_type":"tool","tool_name":"write_to_file","tool_info":{"name":"write_to_file","parameters":{"TargetFile":"/home/u/.gemini/antigravity-cli/brain/c1/plan.md"}}}}`
		events, err := ParseLine([]byte(line))
		if err != nil || !slices.Equal(sequence(events), []string{"ToolCall"}) {
			t.Errorf("ParseLine = %v, %v", events, err)
		}
	})

	t.Run("herramientas de edición y su ruta", func(t *testing.T) {
		for _, tool := range []string{"write_to_file", "replace_file_content", "multi_replace_file_content"} {
			line := `{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"` + tool +
				`","tool_info":{"name":"` + tool + `","parameters":{"TargetFile":"/a/b.go","Otro":1}}}}`
			events, err := ParseLine([]byte(line))
			if err != nil || len(events) != 2 {
				t.Fatalf("%s: %v, %v", tool, events, err)
			}
			want := agent.FileEditProposed{ToolCallID: "step-2", Tool: tool, Path: "/a/b.go"}
			if events[1] != want {
				t.Errorf("%s: FileEditProposed = %+v", tool, events[1])
			}
		}
	})

	t.Run("edición sin TargetFile conserva el ToolCall y da error", func(t *testing.T) {
		line := `{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool","tool_name":"write_to_file","tool_info":{"parameters":{}}}}`
		events, err := ParseLine([]byte(line))
		if err == nil || len(events) != 1 {
			t.Fatalf("ParseLine = %v, %v", events, err)
		}
		if _, ok := events[0].(agent.ToolCall); !ok {
			t.Errorf("evento = %s, quiero ToolCall", kind(events[0]))
		}
	})

	t.Run("result con status distinto de SUCCESS sin texto", func(t *testing.T) {
		line := `{"event":"result","result":{"conversation_id":"c","status":"TIMEOUT","num_turns":3,"usage":{}}}`
		events, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if got := sequence(events); !slices.Equal(got, []string{"Usage", "Error", "Done"}) {
			t.Fatalf("secuencia = %v", got)
		}
		if msg := events[1].(agent.Error).Err.Error(); !strings.Contains(msg, "TIMEOUT") {
			t.Errorf("mensaje de error poco claro: %q", msg)
		}
		if d := events[2].(agent.Done); !d.IsError || d.SessionID != "c" || d.Turns != 3 {
			t.Errorf("Done = %+v", d)
		}
	})

	t.Run("result SUCCESS no emite Error", func(t *testing.T) {
		line := `{"event":"result","result":{"conversation_id":"s","status":"SUCCESS","response":"ok","num_turns":1,"usage":{"input_tokens":1,"output_tokens":2,"thinking_tokens":9,"cache_read_tokens":3,"total_tokens":3}}}`
		events, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		want := []agent.Event{
			agent.Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3},
			agent.Done{SessionID: "s", Result: "ok", Turns: 1},
		}
		if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
			t.Errorf("eventos = %+v", events)
		}
	})

	t.Run("denied_actions sin display_name usa action", func(t *testing.T) {
		line := `{"event":"result","result":{"conversation_id":"s","status":"SUCCESS","denied_actions":[{"action":"command"}]}}`
		events, err := ParseLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if na := events[1].(agent.NeedsApproval); na.Tool != "command" || !na.Denied {
			t.Errorf("NeedsApproval = %+v", na)
		}
	})
}

func TestResultWithBadDenialKeepsDone(t *testing.T) {
	line := `{"event":"result","result":{"conversation_id":"s","status":"SUCCESS","response":"ok","num_turns":1,"denied_actions":[{},{"action":"command","display_name":"RunCommand"}]}}`
	events, err := ParseLine([]byte(line))
	if err == nil {
		t.Error("quiero un error por la denegación incompleta")
	}
	if got, want := sequence(events), []string{"Usage", "NeedsApproval", "Done"}; !slices.Equal(got, want) {
		t.Fatalf("eventos = %v, quiero %v", got, want)
	}
}

func TestParseLineErrors(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"JSON inválido", `{"event":`},
		{"texto plano", `esto no es json`},
		{"JSON que no es objeto", `[1,2,3]`},
		{"sin event", `{"conversation_id":"c"}`},
		{"event vacío", `{"event":""}`},
		{"null", `null`},
		{"init sin conversation_id", `{"event":"init","init":{"model":"m"}}`},
		{"init con conversation_id vacío", `{"event":"init","conversation_id":""}`},
		{"result sin result", `{"event":"result"}`},
		{"result sin status", `{"event":"result","result":{"conversation_id":"c"}}`},
		{"step_update sin objeto", `{"event":"step_update"}`},
		{"step_update sin step_type", `{"event":"step_update","step_update":{"state":"ACTIVE"}}`},
		{"herramienta sin step_index", `{"event":"step_update","step_update":{"state":"ACTIVE","step_type":"tool","tool_name":"x"}}`},
		{"herramienta sin nombre", `{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool"}}`},
		{"TargetFile que no es texto", `{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"write_to_file","tool_info":{"parameters":{"TargetFile":3}}}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			events, err := ParseLine([]byte(tc.line))
			if err == nil {
				t.Fatalf("quiero error, obtuve eventos %v", events)
			}
			if !strings.Contains(err.Error(), "agy "+TestedVersion) {
				t.Errorf("el error no nombra la CLI y la versión: %v", err)
			}
		})
	}
}
