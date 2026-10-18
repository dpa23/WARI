//go:build unix

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
	"github.com/dpa23/wari/internal/orch/pipeline"
)

// fakeBins escribe en un directorio nuevo ejecutables `claude` y `agy` que
// vuelcan un fixture y guardan sus argumentos en <dir>/<cli>.args (uno por
// línea), y los pone primeros en PATH.
func fakeBins(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	for cli, fixture := range map[string]string{"claude": "claude/simple.jsonl", "agy": "agy/simple.jsonl"} {
		src, err := filepath.Abs(filepath.Join("..", "..", "testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + filepath.Join(dir, cli+".args") + "'\ncat '" + src + "'\n"
		if err := os.WriteFile(filepath.Join(dir, cli), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func waitPipeline(t *testing.T, r *pipeline.Runner, want pipeline.State) pipeline.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := r.Snapshot()
		if s.State == want {
			return s
		}
		if s.State == pipeline.Failed || time.Now().After(deadline) {
			t.Fatalf("esperaba %s; estado %s (%s), paso %d", want, s.State, s.Reason, s.Cur)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// TestPipelineClaudeThenAgy corre un pipeline real (supervisor, runners y
// adaptadores) con un paso de claude y otro de agy sobre CLIs falsas que
// vuelcan los fixtures: el resultado de claude llega como {{prev}} al prompt
// de agy.
func TestPipelineClaudeThenAgy(t *testing.T) {
	bins := fakeBins(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".wari"), 0o755); err != nil {
		t.Fatal(err)
	}
	pipe := `{"name":"mixto","steps":[
	  {"name":"plan","agent":"claude","prompt":"planea","mode":"semi"},
	  {"name":"impl","agent":"agy","prompt":"implementa: {{prev}}","mode":"semi","model":"gemini-3.8-flash-low"}]}`
	if err := os.WriteFile(filepath.Join(root, pipelineFile), []byte(pipe), 0o600); err != nil {
		t.Fatal(err)
	}

	sup := orch.New(2)
	defer sup.Close()
	r := loadPipeline(sup, root, agent.Task{Permissions: agent.PermissionAcceptEdits}, orch.Budget{})
	if r == nil {
		t.Fatal("el pipeline con claude y agy debe cargarse")
	}
	defer r.Close()

	if err := r.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := waitPipeline(t, r, pipeline.GateWaiting)
	if s.Gate == nil || s.Gate.Result.Text != "hola" || s.Gate.NextPrompt != "implementa: {{prev}}" {
		t.Fatalf("gate tras el paso de claude = %+v", s.Gate)
	}
	if err := r.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	s = waitPipeline(t, r, pipeline.Done)

	if got := s.Steps[1].Sent; got != "implementa: hola" {
		t.Errorf("prompt enviado a agy = %q, quiero el resultado de claude sustituido", got)
	}
	for i, st := range s.Steps {
		if st.State != pipeline.StepDone || st.Result.Text != "hola" && st.Result.Text != "hola\n" {
			t.Errorf("paso %d = %+v", i, st)
		}
	}

	claudeArgs := readLines(t, filepath.Join(bins, "claude.args"))
	if claudeArgs[0] != "-p" || claudeArgs[len(claudeArgs)-1] != "planea" {
		t.Errorf("args de claude = %q", claudeArgs)
	}
	agyArgs := readLines(t, filepath.Join(bins, "agy.args"))
	for _, want := range []string{"-p=implementa: hola", "--model=gemini-3.8-flash-low", "--mode=accept-edits"} {
		if !slices.Contains(agyArgs, want) {
			t.Errorf("args de agy = %q, falta %q", agyArgs, want)
		}
	}
}
