package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
)

func registry() Registry {
	return Registry{"claude": func() agent.Agent { return newFake() }}
}

const sample = `{
  "name": "feature",
  "steps": [
    {"name": "plan", "agent": "claude", "prompt": "Planea", "mode": "semi",
     "budget": {"max_turns": 5, "max_tokens": 50000, "max_seconds": 300}},
    {"name": "impl", "use": "agente 1", "prompt": "Implementa:\n{{prev}}", "require": "LISTO"},
    {"name": "rev", "agent": "claude", "prompt": "Revisa", "mode": "AUTO", "model": "m",
     "budget": {"max_turns": 2}}
  ]
}`

func TestParse(t *testing.T) {
	p, err := Parse([]byte(sample), registry())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "feature" || len(p.Steps) != 3 {
		t.Fatalf("pipeline = %+v", p)
	}
	s0, s1, s2 := p.Steps[0], p.Steps[1], p.Steps[2]
	if s0.Mode != Semi || s0.Agent == nil ||
		s0.Budget != (orch.Budget{MaxTurns: 5, MaxTokens: 50000, MaxDuration: 300 * time.Second}) {
		t.Fatalf("paso 1 = %+v", s0)
	}
	if s1.Mode != Manual || s1.Use != "agente 1" || s1.Agent != nil || s1.Condition == nil {
		t.Fatalf("paso 2 = %+v", s1)
	}
	if !s1.Condition(Result{Text: "ya LISTO"}) || s1.Condition(Result{Text: "no"}) ||
		s1.Condition(Result{Text: "LISTO", IsError: true}) {
		t.Fatal("la condición require no se aplica bien")
	}
	if s2.Mode != Auto || s2.Model != "m" || s2.Budget.MaxTurns != 2 {
		t.Fatalf("paso 3 = %+v", s2)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct{ name, json, want string }{
		{"json roto", `{`, "JSON inválido"},
		{"campo desconocido", `{"name":"x","steps":[],"foo":1}`, "foo"},
		{"datos extra", `{"steps":[{"name":"a","agent":"claude","prompt":"x"}]} {}`, "después del objeto"},
		{"agente desconocido", `{"steps":[{"name":"a","agent":"gpt","prompt":"x"}]}`, `agente "gpt" desconocido`},
		{"modo desconocido", `{"steps":[{"name":"a","agent":"claude","prompt":"x","mode":"volando"}]}`, "volando"},
		{"auto sin presupuesto", `{"steps":[{"name":"a","agent":"claude","prompt":"x","mode":"auto"}]}`, "Auto exige presupuesto"},
		{"presupuesto negativo", `{"steps":[{"name":"a","agent":"claude","prompt":"x","budget":{"max_turns":-1}}]}`, "negativo"},
		{"sin pasos", `{"name":"x"}`, "sin pasos"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.json), registry())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, se esperaba %q", c.name, err, c.want)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "no.json"), registry()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archivo ausente: %v", err)
	}
	path := filepath.Join(dir, "pipeline.json")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path, registry())
	if err != nil || len(p.Steps) != 3 {
		t.Fatalf("Load = %+v, %v", p, err)
	}
	if err := os.WriteFile(path, []byte(`{"steps":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, registry()); err == nil || !strings.Contains(err.Error(), "pipeline.json") {
		t.Fatalf("el error debe nombrar el archivo: %v", err)
	}
}
