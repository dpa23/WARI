package opencode

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dpa23/wari/internal/agent"
)

func readFixtureLines(t *testing.T, filename string) [][]byte {
	t.Helper()
	path := filepath.Join("../../../testdata/opencode", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("leer fixture %s: %v", filename, err)
	}
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		lines = append(lines, scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("escanear fixture %s: %v", filename, err)
	}
	return lines
}

func parseFixture(t *testing.T, filename string) (*Driver, []agent.Event) {
	t.Helper()
	lines := readFixtureLines(t, filename)
	d := &Driver{}
	var allEvents []agent.Event
	for i, l := range lines {
		evs, err := d.Parse(l)
		if err != nil {
			t.Fatalf("%s fila %d: Parse err = %v", filename, i+1, err)
		}
		allEvents = append(allEvents, evs...)
	}
	return d, allEvents
}

func joinText(events []agent.Event) string {
	var sb strings.Builder
	for _, ev := range events {
		if td, ok := ev.(agent.TextDelta); ok {
			sb.WriteString(td.Text)
		}
	}
	return sb.String()
}

func TestFixtures(t *testing.T) {
	t.Run("simple.jsonl", func(t *testing.T) {
		d, events := parseFixture(t, "simple.jsonl")

		// Secuencia de eventos esperada: Started -> TextDelta -> Usage
		if len(events) != 3 {
			t.Fatalf("se esperaban 3 eventos, se obtuvieron %d: %+v", len(events), events)
		}
		st, ok := events[0].(agent.Started)
		if !ok || st.SessionID != "ses_000000000002" {
			t.Errorf("evento 0 = %+v, want Started ses_000000000002", events[0])
		}
		td, ok := events[1].(agent.TextDelta)
		if !ok || td.Text != "hola" {
			t.Errorf("evento 1 = %+v, want TextDelta 'hola'", events[1])
		}
		us, ok := events[2].(agent.Usage)
		if !ok || us.InputTokens != 10939 || us.OutputTokens != 32 { // 4 output + 28 reasoning = 32
			t.Errorf("evento 2 = %+v, want Usage input:10939 output:32", events[2])
		}

		if gotText := joinText(events); gotText != "hola" {
			t.Errorf("texto concatenado = %q, want 'hola'", gotText)
		}

		done := d.Finish(nil, "")
		if done == nil || done.IsError || done.SessionID != "ses_000000000002" || done.Result != "hola" || done.Turns != 1 {
			t.Errorf("Finish = %+v, want Done exitoso con Result 'hola', Turns 1", done)
		}
	})

	t.Run("tool_read.jsonl", func(t *testing.T) {
		d, events := parseFixture(t, "tool_read.jsonl")

		// Secuencia: Started -> ToolCall (read) -> Usage -> TextDelta
		if len(events) != 4 {
			t.Fatalf("se esperaban 4 eventos, se obtuvieron %d: %+v", len(events), events)
		}
		st, ok := events[0].(agent.Started)
		if !ok || st.SessionID != "ses_000000000010" {
			t.Errorf("evento 0 = %+v, want Started", events[0])
		}
		tc, ok := events[1].(agent.ToolCall)
		if !ok || tc.Name != "read" || tc.ID != "call-00000000000000000000000000000018" {
			t.Errorf("evento 1 = %+v, want ToolCall read", events[1])
		}
		if !strings.Contains(string(tc.Input), "nota.txt") {
			t.Errorf("Input de ToolCall = %s, want nota.txt", string(tc.Input))
		}
		us, ok := events[2].(agent.Usage)
		if !ok || us.InputTokens != 10951 || us.OutputTokens != 57 { // 35 + 22 = 57
			t.Errorf("evento 2 = %+v, want Usage input:10951 output:57", events[2])
		}
		td, ok := events[3].(agent.TextDelta)
		if !ok || td.Text != "contenido de prueba 42" {
			t.Errorf("evento 3 = %+v, want TextDelta", events[3])
		}

		if gotText := joinText(events); gotText != "contenido de prueba 42" {
			t.Errorf("texto concatenado = %q, want 'contenido de prueba 42'", gotText)
		}

		done := d.Finish(nil, "")
		if done == nil || done.IsError || done.SessionID != "ses_000000000010" || done.Result != "contenido de prueba 42" || done.Turns != 2 {
			t.Errorf("Finish = %+v, want Done exitoso con Turns 2", done)
		}
	})

	t.Run("write_build.jsonl", func(t *testing.T) {
		d, events := parseFixture(t, "write_build.jsonl")

		// Verifica que NUNCA emite FileEditProposed
		for _, ev := range events {
			if _, ok := ev.(agent.FileEditProposed); ok {
				t.Fatalf("write_build emitió FileEditProposed: %+v", ev)
			}
		}

		// Secuencia: Started -> ToolCall(write) -> Usage -> ToolCall(read) -> Usage -> TextDelta
		if len(events) != 6 {
			t.Fatalf("se esperaban 6 eventos, se obtuvieron %d: %+v", len(events), events)
		}
		if tc, ok := events[1].(agent.ToolCall); !ok || tc.Name != "write" || tc.ID != "call-00000000000000000000000000000031" {
			t.Errorf("evento 1 = %+v, want ToolCall write", events[1])
		}
		if tc, ok := events[3].(agent.ToolCall); !ok || tc.Name != "read" || tc.ID != "call-00000000000000000000000000000032" {
			t.Errorf("evento 3 = %+v, want ToolCall read", events[3])
		}

		wantText := "Listo. El archivo `hola.txt` ha sido creado en `/tmp/wari-opencode-fixture/hola.txt` con el texto \"hola\"."
		if gotText := joinText(events); gotText != wantText {
			t.Errorf("texto concatenado = %q, want %q", gotText, wantText)
		}

		done := d.Finish(nil, "")
		if done == nil || done.IsError || done.Turns != 3 || done.Result != wantText {
			t.Errorf("Finish = %+v, want Done con Turns 3", done)
		}
	})

	t.Run("write_plan.jsonl", func(t *testing.T) {
		d, events := parseFixture(t, "write_plan.jsonl")

		// En modo plan, el tool_use tuvo status: "error", por lo que NO emite ToolCall ni FileEditProposed
		for _, ev := range events {
			if _, ok := ev.(agent.ToolCall); ok {
				t.Fatalf("write_plan emitió ToolCall indebidamente: %+v", ev)
			}
			if _, ok := ev.(agent.FileEditProposed); ok {
				t.Fatalf("write_plan emitió FileEditProposed: %+v", ev)
			}
		}

		// Secuencia: Started -> Usage -> TextDelta -> Usage
		if len(events) != 4 {
			t.Fatalf("se esperaban 4 eventos, se obtuvieron %d: %+v", len(events), events)
		}
		if _, ok := events[0].(agent.Started); !ok {
			t.Errorf("evento 0 = %+v, want Started", events[0])
		}
		if _, ok := events[1].(agent.Usage); !ok {
			t.Errorf("evento 1 = %+v, want Usage", events[1])
		}
		td, ok := events[2].(agent.TextDelta)
		if !ok || !strings.Contains(td.Text, "modo Plan") {
			t.Errorf("evento 2 = %+v, want TextDelta sobre modo Plan", events[2])
		}
		if _, ok := events[3].(agent.Usage); !ok {
			t.Errorf("evento 3 = %+v, want Usage", events[3])
		}

		done := d.Finish(nil, "")
		if done == nil || done.IsError || done.Turns != 2 {
			t.Errorf("Finish = %+v, want Done con Turns 2", done)
		}
	})

	t.Run("resume.jsonl", func(t *testing.T) {
		d, events := parseFixture(t, "resume.jsonl")

		if len(events) != 2 {
			t.Fatalf("se esperaban 2 eventos, se obtuvieron %d: %+v", len(events), events)
		}
		st, ok := events[0].(agent.Started)
		if !ok || st.SessionID != "ses_000000000002" {
			t.Errorf("evento 0 = %+v, want Started ses_000000000002", events[0])
		}
		td, ok := events[1].(agent.TextDelta)
		if !ok || td.Text != "hola" {
			t.Errorf("evento 1 = %+v, want TextDelta 'hola'", events[1])
		}

		done := d.Finish(nil, "")
		if done == nil || done.IsError || done.SessionID != "ses_000000000002" || done.Result != "hola" || done.Turns != 1 {
			t.Errorf("Finish = %+v", done)
		}
	})

	t.Run("resume_missing.jsonl", func(t *testing.T) {
		d, events := parseFixture(t, "resume_missing.jsonl")

		// Al faltar la sesión, sessionID es vacío: NO debe emitir Started
		for _, ev := range events {
			if _, ok := ev.(agent.Started); ok {
				t.Fatalf("resume_missing emitió Started: %+v", ev)
			}
		}

		if len(events) != 1 {
			t.Fatalf("se esperaba 1 evento Error, se obtuvieron %d: %+v", len(events), events)
		}
		errEv, ok := events[0].(agent.Error)
		if !ok || errEv.Err == nil || !strings.Contains(errEv.Err.Error(), "Session not found") {
			t.Errorf("evento 0 = %+v, want Error con 'Session not found'", events[0])
		}

		done := d.Finish(errors.New("exit status 1"), "")
		if done == nil || !done.IsError || done.Result != "Session not found" {
			t.Errorf("Finish = %+v, want Done con IsError: true y Result 'Session not found'", done)
		}
	})

	t.Run("error_model.jsonl", func(t *testing.T) {
		d, events := parseFixture(t, "error_model.jsonl")

		// error_model tiene sessionID no vacío: debe emitir Started y luego Error
		if len(events) != 2 {
			t.Fatalf("se esperaban 2 eventos (Started, Error), se obtuvieron %d: %+v", len(events), events)
		}
		st, ok := events[0].(agent.Started)
		if !ok || st.SessionID != "ses_000000000001" {
			t.Errorf("evento 0 = %+v, want Started ses_000000000001", events[0])
		}
		errEv, ok := events[1].(agent.Error)
		if !ok || errEv.Err == nil || !strings.Contains(errEv.Err.Error(), "Model unavailable") {
			t.Errorf("evento 1 = %+v, want Error con 'Model unavailable'", events[1])
		}

		done := d.Finish(errors.New("exit status 1"), "")
		if done == nil || !done.IsError || done.SessionID != "ses_000000000001" || !strings.Contains(done.Result, "Model unavailable") {
			t.Errorf("Finish = %+v, want Done con IsError: true y SessionID ses_000000000001", done)
		}
	})
}

func TestMalformedLines(t *testing.T) {
	d := &Driver{}

	t.Run("linea vacia o espacios", func(t *testing.T) {
		evs, err := d.Parse([]byte("   \n\t  "))
		if err != nil || len(evs) != 0 {
			t.Errorf("línea vacía devolvió evs=%v, err=%v", evs, err)
		}
	})

	t.Run("json invalido", func(t *testing.T) {
		_, err := d.Parse([]byte("{not a valid json"))
		if err == nil || !strings.Contains(err.Error(), "opencode 2.0.18: línea inválida") {
			t.Errorf("err = %v, want 'opencode 2.0.18: línea inválida'", err)
		}
	})

	t.Run("sin campo type", func(t *testing.T) {
		_, err := d.Parse([]byte(`{"sessionID":"s1","foo":"bar"}`))
		if err == nil || !strings.Contains(err.Error(), `opencode 2.0.18: línea sin campo "type"`) {
			t.Errorf("err = %v, want 'línea sin campo \"type\"'", err)
		}
	})

	t.Run("tipo desconocido pero valido", func(t *testing.T) {
		evs, err := d.Parse([]byte(`{"type":"future_event","sessionID":"s1"}`))
		if err != nil {
			t.Errorf("evento desconocido dio error: %v", err)
		}
		// Si es la primera línea con sessionID, emite Started y luego ignora el tipo
		if len(evs) > 1 {
			t.Errorf("evs = %v", evs)
		}
		if d.p.unknown["future_event"] != 1 {
			t.Errorf("unknown count = %d, want 1", d.p.unknown["future_event"])
		}
	})

	t.Run("text sin part", func(t *testing.T) {
		_, err := d.Parse([]byte(`{"type":"text"}`))
		if err == nil || !strings.Contains(err.Error(), "text sin part") {
			t.Errorf("err = %v, want 'text sin part'", err)
		}
	})

	t.Run("tool_use sin id o tool", func(t *testing.T) {
		_, err := d.Parse([]byte(`{"type":"tool_use","part":{"tool":"read","state":{"status":"completed"}}}`))
		if err == nil || !strings.Contains(err.Error(), "tool_use sin id o tool") {
			t.Errorf("err = %v, want 'tool_use sin id o tool'", err)
		}
	})

	t.Run("tool_use sin state", func(t *testing.T) {
		_, err := d.Parse([]byte(`{"type":"tool_use","part":{"id":"c1","tool":"read"}}`))
		if err == nil || !strings.Contains(err.Error(), "tool_use sin state") {
			t.Errorf("err = %v, want 'tool_use sin state'", err)
		}
	})

	t.Run("step_finish sin tokens", func(t *testing.T) {
		_, err := d.Parse([]byte(`{"type":"step_finish","part":{"cost":0}}`))
		if err == nil || !strings.Contains(err.Error(), "step_finish sin tokens") {
			t.Errorf("err = %v, want 'step_finish sin tokens'", err)
		}
	})
}

func TestParseLineStandalone(t *testing.T) {
	line := []byte(`{"type":"text","sessionID":"ses_standalone","part":{"text":"hola standalone"}}`)
	evs, err := ParseLine(line)
	if err != nil {
		t.Fatalf("ParseLine err: %v", err)
	}
	if len(evs) != 2 { // Started + TextDelta
		t.Fatalf("ParseLine devolvió %d eventos, se esperaban 2: %+v", len(evs), evs)
	}
	if _, ok := evs[0].(agent.Started); !ok {
		t.Errorf("evento 0 debe ser Started: %+v", evs[0])
	}
	if td, ok := evs[1].(agent.TextDelta); !ok || td.Text != "hola standalone" {
		t.Errorf("evento 1 debe ser TextDelta: %+v", evs[1])
	}
}
