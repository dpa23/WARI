package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
)

// Registry asocia el nombre que usa "agent" en el JSON con la fábrica que crea
// el agente (p. ej. "claude").
type Registry map[string]func() agent.Agent

type fileBudget struct {
	MaxTurns   int   `json:"max_turns"`
	MaxTokens  int64 `json:"max_tokens"`
	MaxSeconds int   `json:"max_seconds"`
}

type fileStep struct {
	Name    string      `json:"name"`
	Agent   string      `json:"agent"`
	Use     string      `json:"use"`
	Prompt  string      `json:"prompt"`
	Model   string      `json:"model"`
	Mode    string      `json:"mode"`
	Budget  *fileBudget `json:"budget"`
	Require string      `json:"require"`
}

type file struct {
	Name  string     `json:"name"`
	Steps []fileStep `json:"steps"`
}

// Load lee un pipeline de un archivo JSON (formato en la documentación del
// paquete). Si el archivo no existe, el error cumple errors.Is(err,
// os.ErrNotExist).
func Load(path string, agents Registry) (Pipeline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Pipeline{}, err
	}
	p, err := Parse(data, agents)
	if err != nil {
		return Pipeline{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Parse interpreta el JSON de un pipeline y lo valida. No admite campos
// desconocidos ni datos tras el objeto.
func Parse(data []byte, agents Registry) (Pipeline, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return Pipeline{}, fmt.Errorf("JSON inválido: %w", err)
	}
	if dec.More() {
		return Pipeline{}, fmt.Errorf("JSON inválido: datos después del objeto")
	}
	p := Pipeline{Name: f.Name, Steps: make([]Step, len(f.Steps))}
	for i, fs := range f.Steps {
		s := Step{Name: fs.Name, Use: fs.Use, Prompt: fs.Prompt, Model: fs.Model}
		at := fmt.Sprintf("paso %d (%q)", i+1, fs.Name)
		switch strings.ToLower(fs.Mode) {
		case "", "manual":
			s.Mode = Manual
		case "semi":
			s.Mode = Semi
		case "auto":
			s.Mode = Auto
		default:
			return Pipeline{}, fmt.Errorf("%s: modo %q desconocido (manual, semi o auto)", at, fs.Mode)
		}
		if fs.Agent != "" {
			factory, ok := agents[fs.Agent]
			if !ok {
				return Pipeline{}, fmt.Errorf("%s: agente %q desconocido", at, fs.Agent)
			}
			s.Agent = factory
		}
		if b := fs.Budget; b != nil {
			if b.MaxTurns < 0 || b.MaxTokens < 0 || b.MaxSeconds < 0 {
				return Pipeline{}, fmt.Errorf("%s: el presupuesto no puede ser negativo", at)
			}
			s.Budget = orch.Budget{
				MaxTurns:    b.MaxTurns,
				MaxTokens:   b.MaxTokens,
				MaxDuration: time.Duration(b.MaxSeconds) * time.Second,
			}
		}
		if req := fs.Require; req != "" {
			s.Condition = func(r Result) bool { return !r.IsError && strings.Contains(r.Text, req) }
		}
		p.Steps[i] = s
	}
	if err := p.Validate(); err != nil {
		return Pipeline{}, err
	}
	return p, nil
}
