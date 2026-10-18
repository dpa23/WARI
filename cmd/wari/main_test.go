package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/orch/pipeline"
)

func TestLoadPipeline(t *testing.T) {
	root := t.TempDir()
	sup := orch.New(1)
	defer sup.Close()

	if r := loadPipeline(sup, root, agent.Task{}, orch.Budget{}); r != nil {
		t.Fatal("sin archivo no debe haber pipeline")
	}

	if err := os.MkdirAll(filepath.Join(root, ".wari"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, pipelineFile)
	if err := os.WriteFile(path, []byte(`{"steps":[{"name":"a","agent":"nadie","prompt":"x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := loadPipeline(sup, root, agent.Task{}, orch.Budget{}); r != nil {
		t.Fatal("un archivo inválido no debe crear pipeline")
	}

	good := `{"name":"demo","steps":[
	  {"name":"plan","agent":"claude","prompt":"planea","mode":"semi"},
	  {"name":"impl","agent":"claude","prompt":"haz {{prev}}"}]}`
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	r := loadPipeline(sup, root, agent.Task{}, orch.Budget{})
	if r == nil {
		t.Fatal("el archivo válido debe crear el pipeline")
	}
	defer r.Close()
	if s := r.Snapshot(); s.Name != "demo" || len(s.Steps) != 2 || s.State != pipeline.NotStarted {
		t.Fatalf("snapshot = %+v", s)
	}
	if len(sup.Snapshots()) != 0 {
		t.Fatal("cargar el pipeline no debe lanzar ningún agente")
	}
}

func TestAgentFactories(t *testing.T) {
	want := []string{"agy", "claude", "codex", "opencode"}
	if got := agentNames(); !slices.Equal(got, want) {
		t.Errorf("agentNames = %v, quiero %v", got, want)
	}
	for name, newAgent := range agentFactories {
		a := newAgent()
		if a == nil || a.Status() != agent.StatusIdle {
			t.Errorf("la fábrica %q no devuelve un agente inactivo", name)
		}
		a.Close()
	}
}

func TestNewIndex(t *testing.T) {
	root := t.TempDir()
	if newIndex(root, true, false) != nil {
		t.Error("-no-index debía desactivar el índice")
	}
	ix := newIndex(root, false, false)
	if ix == nil || ix.Root() != root {
		t.Fatalf("índice = %v", ix)
	}
	// Con -ctags y sin ctags en el PATH avisa y sigue con el indexador propio.
	t.Setenv("PATH", t.TempDir())
	if newIndex(root, false, true) == nil {
		t.Error("sin ctags debía seguir con el indexador propio")
	}
}
