package claude

import (
	"slices"
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
			name: "por defecto pide permiso y nunca hereda el modo del usuario",
			task: agent.Task{Prompt: "hola"},
			want: []string{"--permission-mode", "manual", "--permission-prompts", "none", "--", "hola"},
		},
		{
			name: "reanudar con modelo y edición aceptada",
			task: agent.Task{Prompt: "sigue", SessionID: "s1", Model: "haiku", Permissions: agent.PermissionAcceptEdits},
			want: []string{"--resume", "s1", "--model", "haiku", "--permission-mode", "acceptEdits", "--permission-prompts", "none", "--", "sigue"},
		},
		{
			name: "plan",
			task: agent.Task{Prompt: "x", Permissions: agent.PermissionPlan},
			want: []string{"--permission-mode", "plan", "--permission-prompts", "none", "--", "x"},
		},
		{
			name: "un prompt con forma de flag queda después de --",
			task: agent.Task{Prompt: "--dangerously-skip-permissions"},
			want: []string{"--", "--dangerously-skip-permissions"},
		},
	}
	base := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Args(tt.task)
			if !slices.Equal(got[:len(base)], base) {
				t.Errorf("Args = %q, quiero que empiece con %q", got, base)
			}
			if !slices.Equal(got[len(got)-len(tt.want):], tt.want) {
				t.Errorf("Args = %q, quiero que termine con %q", got, tt.want)
			}
		})
	}
}
