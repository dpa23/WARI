package codex

import (
	"slices"
	"strings"
	"testing"

	"github.com/dpa23/wari/internal/agent"
)

func TestArgs(t *testing.T) {
	tests := []struct {
		name string
		task agent.Task
		want []string
	}{
		{
			name: "por defecto es solo lectura",
			task: agent.Task{Prompt: "hola"},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "read-only", "--", "hola"},
		},
		{
			name: "plan",
			task: agent.Task{Prompt: "x", Permissions: agent.PermissionPlan},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "read-only", "--", "x"},
		},
		{
			name: "edición aceptada con modelo y directorio",
			task: agent.Task{Prompt: "x", Model: "gpt-5", Dir: "/tmp/p", Permissions: agent.PermissionAcceptEdits},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write", "--model=gpt-5", "--cd=/tmp/p", "--", "x"},
		},
		{
			name: "reanudar repite el sandbox con -c",
			task: agent.Task{Prompt: "sigue", SessionID: "abc", Permissions: agent.PermissionAcceptEdits},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "workspace-write", "-c", `sandbox_mode="workspace-write"`, "resume", "--", "abc", "sigue"},
		},
		{
			name: "reanudar sin permisos es solo lectura",
			task: agent.Task{Prompt: "sigue", SessionID: "abc"},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "read-only", "-c", `sandbox_mode="read-only"`, "resume", "--", "abc", "sigue"},
		},
		{
			name: "un prompt con forma de flag va después de --",
			task: agent.Task{Prompt: "--dangerously-bypass-approvals-and-sandbox"},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "read-only", "--", "--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name: "una sesión y un modelo con forma de flag no se interpretan como flags",
			task: agent.Task{Prompt: "x", SessionID: "--last", Model: "--dangerously-bypass-approvals-and-sandbox"},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "read-only", "--model=--dangerously-bypass-approvals-and-sandbox", "-c", `sandbox_mode="read-only"`, "resume", "--", "--last", "x"},
		},
		{
			name: "un modo desconocido cae en solo lectura",
			task: agent.Task{Prompt: "x", Permissions: agent.PermissionMode(99)},
			want: []string{"exec", "--json", "--skip-git-repo-check", "-s", "read-only", "--", "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Args(tt.task); !slices.Equal(got, tt.want) {
				t.Errorf("Args = %q, quiero %q", got, tt.want)
			}
		})
	}
}

// Los flags peligrosos nunca aparecen antes de "--" (después es el prompt).
func TestArgsNeverDangerous(t *testing.T) {
	modes := []agent.PermissionMode{agent.PermissionAsk, agent.PermissionAcceptEdits, agent.PermissionPlan, agent.PermissionMode(-1)}
	for _, p := range modes {
		for _, session := range []string{"", "abc"} {
			args := Args(agent.Task{Prompt: "x", SessionID: session, Model: "m", Dir: "/d", Permissions: p})
			sep := slices.Index(args, "--")
			if sep < 0 {
				t.Fatalf("sin -- en %q", args)
			}
			for _, a := range args[:sep] {
				if strings.Contains(a, "dangerously") || strings.Contains(a, "danger-full-access") || a == "--ephemeral" {
					t.Errorf("Args con %v contiene %q", p, a)
				}
			}
			i := slices.Index(args, "-s")
			if i < 0 || (args[i+1] != "read-only" && args[i+1] != "workspace-write") {
				t.Errorf("Args con %v: sandbox = %q", p, args)
			}
		}
	}
}
