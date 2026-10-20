package agy

import (
	"errors"
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
			// PermissionAsk no puede quedar en el modo por defecto de la CLI,
			// que escribe en el proyecto.
			name: "por defecto usa plan",
			task: agent.Task{Prompt: "hola"},
			want: []string{"-p=hola", "--output-format", "stream-json", "--mode=plan"},
		},
		{
			name: "plan",
			task: agent.Task{Prompt: "x", Permissions: agent.PermissionPlan},
			want: []string{"-p=x", "--output-format", "stream-json", "--mode=plan"},
		},
		{
			name: "reanudar con modelo y edición aceptada",
			task: agent.Task{Prompt: "sigue", SessionID: "c1", Model: "gemini-3.8-flash-low", Permissions: agent.PermissionAcceptEdits},
			want: []string{"-p=sigue", "--output-format", "stream-json", "--conversation=c1", "--model=gemini-3.8-flash-low", "--mode=accept-edits"},
		},
		{
			name: "un prompt con forma de flag va pegado a -p",
			task: agent.Task{Prompt: "--dangerously-skip-permissions"},
			want: []string{"-p=--dangerously-skip-permissions", "--output-format", "stream-json", "--mode=plan"},
		},
		{
			name: "un modelo o una sesión con forma de flag van pegados a su flag",
			task: agent.Task{Prompt: "x", SessionID: "--continue", Model: "--dangerously-skip-permissions"},
			want: []string{"-p=x", "--output-format", "stream-json", "--conversation=--continue", "--model=--dangerously-skip-permissions", "--mode=plan"},
		},
		{
			name: "un modo desconocido cae en plan",
			task: agent.Task{Prompt: "x", Permissions: agent.PermissionMode(99)},
			want: []string{"-p=x", "--output-format", "stream-json", "--mode=plan"},
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

func TestArgsNeverSkipPermissions(t *testing.T) {
	for _, p := range []agent.PermissionMode{agent.PermissionAsk, agent.PermissionAcceptEdits, agent.PermissionPlan} {
		for _, a := range Args(agent.Task{Prompt: "x", Permissions: p}) {
			if a == "--dangerously-skip-permissions" || a == "--disable-slash-commands" || a == "--mode=auto" {
				t.Errorf("Args con %v contiene %q", p, a)
			}
		}
	}
}

func TestDriverDetectsMissingConversation(t *testing.T) {
	started := func(id string) []byte {
		return []byte(`{"event":"init","conversation_id":"` + id + `","init":{"model":"m"}}`)
	}

	d := &Driver{}
	d.want = "pedida"
	events, err := d.Parse(started("otra"))
	if !errors.Is(err, agent.ErrSessionNotFound) {
		t.Fatalf("err = %v, quiero ErrSessionNotFound", err)
	}
	if len(events) != 1 || events[0] != (agent.Started{SessionID: "otra", Model: "m"}) {
		t.Errorf("eventos = %v, quiero el Started de la conversación nueva", events)
	}

	if _, err := d.Parse(started("pedida")); err != nil {
		t.Errorf("reanudar la conversación pedida dio error: %v", err)
	}

	d.want = ""
	if _, err := d.Parse(started("cualquiera")); err != nil {
		t.Errorf("un turno nuevo (sin sesión pedida) dio error: %v", err)
	}
}

func TestArgsEffort(t *testing.T) {
	args := Args(agent.Task{Prompt: "x", Model: "gemini-3.8-flash-low", Effort: "high"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--model=gemini-3.8-flash-low") || !strings.Contains(joined, "--effort=high") {
		t.Errorf("args = %v", args)
	}
	if strings.Contains(strings.Join(Args(agent.Task{Prompt: "x"}), " "), "--effort") {
		t.Error("sin Effort no debe haber --effort")
	}
}
