package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	expected := map[string][]string{
		"send":          {"enter"},
		"newline":       {"alt+enter", "ctrl+j"},
		"cancel":        {"esc"},
		"quit":          {"ctrl+c"},
		"new_session":   {"ctrl+n"},
		"page_up":       {"pgup"},
		"page_down":     {"pgdown"},
		"focus_next":    {"tab"},
		"focus_prev":    {"shift+tab"},
		"palette":       {"ctrl+p"},
		"save":          {"ctrl+s"},
		"undo":          {"ctrl+z"},
		"redo":          {"ctrl+y"},
		"find":          {"ctrl+f"},
		"new_file":      {"alt+f"},
		"toggle_tree":   {"ctrl+b"},
		"toggle_agents": {"ctrl+g"},
		"agent_new":     {"ctrl+t"},
		"agent_next":    {"alt+down"},
		"agent_prev":    {"alt+up"},
		"agent_pause":   {"ctrl+o"},
		"agent_stop":    {"ctrl+x"},
		"stop_all":      {"ctrl+k"},
		"diff_file":     {"ctrl+r"},
		"revert_file":   {"alt+r"},
		"revert_turn":   {"alt+t"},
		"revert_agent":  {"alt+a"},
		"pipeline_run":  {"alt+p"},
		"gate_approve":  {"alt+y"},
		"gate_reject":   {"alt+n"},
		"gate_edit":     {"alt+e"},
	}

	got := Default()

	if len(got) != len(expected) {
		t.Fatalf("Default() tiene %d acciones, se esperaban %d", len(got), len(expected))
	}

	for act, wantKeys := range expected {
		gotKeys, ok := got[act]
		if !ok {
			t.Errorf("acción %q no encontrada en Default()", act)
			continue
		}
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Errorf("acción %q: se esperaba %v, se obtuvo %v", act, wantKeys, gotKeys)
		}
	}

	// El mapa por defecto no debe tener conflictos
	conflicts := got.Conflicts()
	if len(conflicts) != 0 {
		t.Errorf("Default().Conflicts() no está vacío: %v", conflicts)
	}
}

func TestLoad_OverrideOrder(t *testing.T) {
	userDir := t.TempDir()
	projectDir := t.TempDir()

	userJSON := `{
		"send": ["ctrl+m"],
		"save": ["ctrl+w"]
	}`
	if err := os.WriteFile(filepath.Join(userDir, "keys.json"), []byte(userJSON), 0o600); err != nil {
		t.Fatalf("error escribiendo user keys.json: %v", err)
	}

	projectJSON := `{
		"send": ["f1"],
		"undo": ["ctrl+u"]
	}`
	if err := os.WriteFile(filepath.Join(projectDir, "keys.json"), []byte(projectJSON), 0o600); err != nil {
		t.Fatalf("error escribiendo project keys.json: %v", err)
	}

	km, err := Load(userDir, projectDir)
	if err != nil {
		t.Fatalf("Load devolvió error inesperado: %v", err)
	}

	// projectDir tiene prioridad sobre userDir
	if want := []string{"f1"}; !reflect.DeepEqual(km["send"], want) {
		t.Errorf("send: se esperaba %v (project override), se obtuvo %v", want, km["send"])
	}

	// userDir tiene prioridad sobre default
	if want := []string{"ctrl+w"}; !reflect.DeepEqual(km["save"], want) {
		t.Errorf("save: se esperaba %v (user override), se obtuvo %v", want, km["save"])
	}

	// projectDir tiene prioridad sobre default
	if want := []string{"ctrl+u"}; !reflect.DeepEqual(km["undo"], want) {
		t.Errorf("undo: se esperaba %v (project override), se obtuvo %v", want, km["undo"])
	}

	// Las acciones no modificadas conservan el valor por defecto
	if want := []string{"ctrl+c"}; !reflect.DeepEqual(km["quit"], want) {
		t.Errorf("quit: se esperaba %v (default), se obtuvo %v", want, km["quit"])
	}
}

func TestLoad_NonExistentFiles(t *testing.T) {
	nonExistentUser := filepath.Join(t.TempDir(), "nonexistent_user")
	nonExistentProject := filepath.Join(t.TempDir(), "nonexistent_proj")

	km, err := Load(nonExistentUser, nonExistentProject)
	if err != nil {
		t.Fatalf("Load con archivos inexistentes no debe fallar: %v", err)
	}

	expected := Default()
	if !reflect.DeepEqual(km, expected) {
		t.Errorf("Load con archivos inexistentes debe ser idéntico a Default()")
	}
}

func TestLoad_UnknownAction(t *testing.T) {
	userDir := t.TempDir()
	content := `{
		"invalid_action": ["ctrl+x"]
	}`
	path := filepath.Join(userDir, "keys.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("error escribiendo keys.json: %v", err)
	}

	_, err := Load(userDir, "")
	if err == nil {
		t.Fatal("se esperaba error con acción desconocida, se obtuvo nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "keys.json") {
		t.Errorf("el error debe contener el nombre del archivo: %q", errStr)
	}
	if !strings.Contains(errStr, "invalid_action") {
		t.Errorf("el error debe contener el nombre de la acción desconocida: %q", errStr)
	}
}

func TestLoad_EmptyKeyList(t *testing.T) {
	userDir := t.TempDir()
	content := `{
		"send": []
	}`
	path := filepath.Join(userDir, "keys.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("error escribiendo keys.json: %v", err)
	}

	_, err := Load(userDir, "")
	if err == nil {
		t.Fatal("se esperaba error con lista de teclas vacía, se obtuvo nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "keys.json") {
		t.Errorf("el error debe contener el nombre del archivo: %q", errStr)
	}
	if !strings.Contains(errStr, "send") {
		t.Errorf("el error debe contener el nombre de la acción con lista vacía: %q", errStr)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	userDir := t.TempDir()
	path := filepath.Join(userDir, "keys.json")
	if err := os.WriteFile(path, []byte(`{ invalid: json `), 0o600); err != nil {
		t.Fatalf("error escribiendo keys.json: %v", err)
	}

	_, err := Load(userDir, "")
	if err == nil {
		t.Fatal("se esperaba error con JSON inválido, se obtuvo nil")
	}

	if !strings.Contains(err.Error(), "keys.json") {
		t.Errorf("el error debe contener el nombre del archivo: %q", err.Error())
	}
}

func TestConflicts(t *testing.T) {
	t.Run("sin conflictos", func(t *testing.T) {
		km := Keymap{
			"send":   {"enter"},
			"cancel": {"esc"},
		}
		if got := km.Conflicts(); len(got) != 0 {
			t.Errorf("se esperaban 0 conflictos, se obtuvo: %v", got)
		}
	})

	t.Run("con conflicto simple", func(t *testing.T) {
		km := Keymap{
			"save": {"ctrl+s"},
			"find": {"ctrl+s"},
		}
		got := km.Conflicts()
		if len(got) != 1 {
			t.Fatalf("se esperaba 1 conflicto, se obtuvieron %d: %v", len(got), got)
		}
		if !strings.Contains(got[0], "ctrl+s") {
			t.Errorf("la descripción debe contener la tecla: %q", got[0])
		}
		if !strings.Contains(got[0], "save") || !strings.Contains(got[0], "find") {
			t.Errorf("la descripción debe contener ambas acciones: %q", got[0])
		}
	})

	t.Run("múltiples conflictos ordenados", func(t *testing.T) {
		km := Keymap{
			"undo":       {"ctrl+z"},
			"redo":       {"ctrl+z"},
			"focus_next": {"tab"},
			"focus_prev": {"tab"},
		}
		got := km.Conflicts()
		if len(got) != 2 {
			t.Fatalf("se esperaban 2 conflictos, se obtuvieron %d: %v", len(got), got)
		}
		// Deben estar ordenados por nombre de tecla: "ctrl+z" antes que "tab"
		if !strings.Contains(got[0], "ctrl+z") {
			t.Errorf("el primer conflicto debe ser 'ctrl+z': %q", got[0])
		}
		if !strings.Contains(got[1], "tab") {
			t.Errorf("el segundo conflicto debe ser 'tab': %q", got[1])
		}
	})

	t.Run("tecla duplicada en la misma acción no es conflicto", func(t *testing.T) {
		km := Keymap{
			"save": {"ctrl+s", "ctrl+s"},
		}
		if got := km.Conflicts(); len(got) != 0 {
			t.Errorf("tecla duplicada en la misma acción no es conflicto, se obtuvo: %v", got)
		}
	})
}

func TestUserDir(t *testing.T) {
	dir, err := UserDir()
	if err != nil {
		t.Fatalf("UserDir() devolvió error: %v", err)
	}
	if filepath.Base(dir) != "wari" {
		t.Errorf("UserDir() debe terminar en 'wari', se obtuvo %q", dir)
	}
}
