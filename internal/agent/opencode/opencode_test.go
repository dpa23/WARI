package opencode

import (
	"errors"
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
			name: "por defecto PermissionAsk usa plan",
			task: agent.Task{Prompt: "hola"},
			want: []string{"run", "--standalone", "--format", "json", "--agent", "plan", "--", "hola"},
		},
		{
			name: "PermissionPlan usa plan",
			task: agent.Task{Prompt: "hola", Permissions: agent.PermissionPlan},
			want: []string{"run", "--standalone", "--format", "json", "--agent", "plan", "--", "hola"},
		},
		{
			name: "PermissionAcceptEdits sin --agent",
			task: agent.Task{Prompt: "escribe archivo", Permissions: agent.PermissionAcceptEdits},
			want: []string{"run", "--standalone", "--format", "json", "--", "escribe archivo"},
		},
		{
			name: "reanudar sesion con modelo y modo plan",
			task: agent.Task{
				Prompt:      "continua",
				SessionID:   "ses_001",
				Model:       "anthropic/claude-3-7-sonnet",
				Permissions: agent.PermissionAsk,
			},
			want: []string{
				"run", "--standalone", "--format", "json",
				"--agent", "plan",
				"--session", "ses_001",
				"--model", "anthropic/claude-3-7-sonnet",
				"--", "continua",
			},
		},
		{
			name: "reanudar sesion con edicion aceptada",
			task: agent.Task{
				Prompt:      "modifica",
				SessionID:   "ses_002",
				Permissions: agent.PermissionAcceptEdits,
			},
			want: []string{
				"run", "--standalone", "--format", "json",
				"--session", "ses_002",
				"--", "modifica",
			},
		},
		{
			name: "un prompt con forma de flag queda protegido despues de --",
			task: agent.Task{Prompt: "--dangerously-skip-permissions"},
			want: []string{
				"run", "--standalone", "--format", "json",
				"--agent", "plan",
				"--", "--dangerously-skip-permissions",
			},
		},
		{
			name: "un modo desconocido cae en plan",
			task: agent.Task{Prompt: "x", Permissions: agent.PermissionMode(99)},
			want: []string{
				"run", "--standalone", "--format", "json",
				"--agent", "plan",
				"--", "x",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Args(tt.task)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Args = %q, quiero %q", got, tt.want)
			}
		})
	}
}

func TestArgsNeverAuto(t *testing.T) {
	for _, p := range []agent.PermissionMode{agent.PermissionAsk, agent.PermissionAcceptEdits, agent.PermissionPlan, agent.PermissionMode(123)} {
		args := Args(agent.Task{Prompt: "test", Permissions: p})
		for _, a := range args {
			if a == "--auto" {
				t.Fatalf("Args con permisos %v incluye --auto: %q", p, args)
			}
		}
	}
}

func TestFinish(t *testing.T) {
	t.Run("caso 1: sin error y exitErr nil devuelve Done exitoso", func(t *testing.T) {
		d := &Driver{}
		d.p = newParser()
		d.p.sessionID = "ses_abc"
		d.p.text.WriteString("resultado final")
		d.p.stepStarts = 3

		done := d.Finish(nil, "")
		if done == nil {
			t.Fatal("Finish devolvió nil")
		}
		if done.IsError {
			t.Error("IsError debe ser false")
		}
		if done.SessionID != "ses_abc" {
			t.Errorf("SessionID = %q, want ses_abc", done.SessionID)
		}
		if done.Result != "resultado final" {
			t.Errorf("Result = %q, want 'resultado final'", done.Result)
		}
		if done.Turns != 3 {
			t.Errorf("Turns = %d, want 3", done.Turns)
		}
	})

	t.Run("caso 2: hubo eventos error devuelve Done con IsError y ultimo mensaje", func(t *testing.T) {
		d := &Driver{}
		d.p = newParser()
		d.p.sessionID = "ses_err"
		d.p.hasError = true
		d.p.lastError = "error de proveedor"

		// Tanto con exitErr nil como con exitErr != nil debe devolver el Done de error
		for _, exitErr := range []error{nil, errors.New("exit status 1")} {
			done := d.Finish(exitErr, "stderr output")
			if done == nil {
				t.Fatalf("Finish con exitErr=%v devolvió nil", exitErr)
			}
			if !done.IsError {
				t.Errorf("IsError debe ser true")
			}
			if done.SessionID != "ses_err" {
				t.Errorf("SessionID = %q, want ses_err", done.SessionID)
			}
			if done.Result != "error de proveedor" {
				t.Errorf("Result = %q, want 'error de proveedor'", done.Result)
			}
		}
	})

	t.Run("caso 3: exitErr != nil sin eventos error devuelve nil", func(t *testing.T) {
		d := &Driver{}
		d.p = newParser()
		d.p.sessionID = "ses_crash"
		d.p.text.WriteString("texto parcial")

		done := d.Finish(errors.New("signal: killed"), "fallo inesperado")
		if done != nil {
			t.Errorf("Finish debía devolver nil para que el Runner maneje el error de salida, obtuvo: %+v", done)
		}
	})
}
