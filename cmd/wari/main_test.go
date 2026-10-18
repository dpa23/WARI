package main

import (
	"os"
	"path/filepath"
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
