package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsProjectOverridesUser(t *testing.T) {
	user, proj := t.TempDir(), t.TempDir()
	write := func(dir, body string) {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(user, `{"theme":"light","icons":"nerd"}`)
	write(proj, `{"theme":"dark"}`)
	got, err := LoadSettings(user, proj)
	if err != nil || got.Theme != "dark" || got.Icons != "nerd" {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	if got, err := LoadSettings(t.TempDir(), ""); err != nil || got != (Settings{}) {
		t.Errorf("sin archivos: %+v %v", got, err)
	}
}

func TestSettingsRejectsUnknownFieldsAndValues(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{`{"tema":"dark"}`, `{"theme":"rojo"}`, `{"icons":"x"}`} {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(dir, ""); err == nil || !strings.Contains(err.Error(), "settings.json") {
			t.Errorf("%s debía fallar con el nombre del archivo: %v", body, err)
		}
	}
}

func TestSaveThemeKeepsOtherSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"icons":"emoji"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveTheme(dir, "light"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(dir, "")
	if err != nil || got.Theme != "light" || got.Icons != "emoji" {
		t.Fatalf("settings = %+v, %v", got, err)
	}
	if err := SaveTheme(dir, "rojo"); err == nil {
		t.Error("un tema inválido no se guarda")
	}
}
