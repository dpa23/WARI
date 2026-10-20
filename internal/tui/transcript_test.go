package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dpa23/wari/internal/agent"
)

func TestRelPathsQuitaLaRaiz(t *testing.T) {
	m := &transcript{root: "/home/u/WARI"}
	got := m.relPaths("[README.md](/home/u/WARI/README.md) y file:///home/u/WARI/a/b.go#L3 y /otro/x")
	want := "[README.md](README.md) y a/b.go#L3 y /otro/x"
	if got != want {
		t.Errorf("relPaths = %q, quiero %q", got, want)
	}
	if got := (&transcript{}).relPaths("/home/u/WARI/x"); got != "/home/u/WARI/x" {
		t.Errorf("sin raíz no cambia: %q", got)
	}
}

func TestStripFileLinks(t *testing.T) {
	in := "En [`internal/orch/orch.go`](internal/orch/orch.go#L192), [Go](https://go.dev) y [x](file:///a/b.go)."
	want := "En `internal/orch/orch.go`, [Go](https://go.dev) y x."
	if got := stripFileLinks(in); got != want {
		t.Errorf("stripFileLinks = %q, quiero %q", got, want)
	}
}

func TestToolCallsIdenticasSeColapsan(t *testing.T) {
	m := &transcript{}
	call := agent.ToolCall{Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)}
	m.applyEvent(call)
	m.applyEvent(call)
	m.applyEvent(call)
	m.applyEvent(agent.ToolCall{Name: "read_file", Input: json.RawMessage(`{"path":"b.go"}`)})
	if len(m.entries) != 2 || m.entries[0].text != "⚙ read_file a.go ×3" || m.entries[1].text != "⚙ read_file b.go" {
		t.Errorf("entradas = %+v", m.entries)
	}
}

func TestTurnoCaroAvisa(t *testing.T) {
	m := &transcript{}
	m.applyEvent(agent.Usage{InputTokens: 223_958, OutputTokens: 2_319})
	m.applyEvent(agent.Done{})
	last := m.entries[len(m.entries)-1]
	if last.kind != kindInfo || !strings.Contains(last.text, "226") {
		t.Fatalf("faltó el aviso de turno caro: %+v", m.entries)
	}
	n := len(m.entries)
	m.applyEvent(agent.Usage{InputTokens: 500, OutputTokens: 10})
	m.applyEvent(agent.Done{})
	if len(m.entries) != n {
		t.Errorf("un turno barato no avisa y el contador se reinicia: %+v", m.entries[n:])
	}
}
