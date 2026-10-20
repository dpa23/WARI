//go:build unix

package claude

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dpa23/wari/internal/agent"
)

// fakeBin pone un ejecutable falso con el nombre de la CLI en el PATH.
func fakeBin(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestLaunch(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // ninguna CLI instalada
	if _, _, err := Launch(agent.Task{}, ""); err == nil {
		t.Fatal("sin la CLI en el PATH debe fallar")
	}
	fakeBin(t, "claude")
	path, args, err := Launch(agent.Task{Model: "m1", Effort: "high", Permissions: agent.PermissionAcceptEdits}, "hola")
	if err != nil || filepath.Base(path) != "claude" {
		t.Fatalf("path = %q, %v", path, err)
	}
	t.Logf("args = %q", args)
	want := map[string][]string{
		"claude":   {"--model", "m1", "--effort", "high", "--permission-mode", "acceptEdits", "--", "hola"},
		"agy":      {"--model=m1", "--effort=high", "--mode=accept-edits", "--prompt-interactive=hola"},
		"opencode": {"--standalone", "--auto", "--prompt", "hola"},
		"codex":    {"-s", "workspace-write", "--model=m1", "--", "hola"},
	}["claude"]
	if !slices.Equal(args, want) {
		t.Errorf("args = %q, quería %q", args, want)
	}
	// Sin nada elegido no se pasa ningún flag de más.
	_, bare, _ := Launch(agent.Task{}, "")
	for _, a := range bare {
		if a == "--model" || a == "--effort" || a == "--auto" {
			t.Errorf("sin elecciones no debe haber %q en %q", a, bare)
		}
	}
}
