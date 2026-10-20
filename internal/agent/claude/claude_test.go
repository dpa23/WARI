package claude

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

func TestArgsEffortAndInfo(t *testing.T) {
	args := Args(agent.Task{Prompt: "x", Model: "sonnet", Effort: "low"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--model sonnet") || !strings.Contains(joined, "--effort low") {
		t.Errorf("args = %v", args)
	}
	if strings.Contains(strings.Join(Args(agent.Task{Prompt: "x"}), " "), "--effort") {
		t.Error("sin Effort no debe haber --effort")
	}
	if info := Info(); info.Name != "claude" || len(info.Models) == 0 || len(info.Efforts) == 0 {
		t.Errorf("info = %+v", info)
	}
}
