package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Settings son los ajustes de apariencia de WARI, en settings.json (del usuario
// y, encima, del proyecto en .wari/). Un valor vacío deja el predeterminado.
// Orden de prioridad: la opción de la línea de comandos, el entorno, el
// archivo del proyecto, el del usuario y, al final, la detección automática.
type Settings struct {
	// Theme es "auto" (detecta el fondo de la terminal), "dark" o "light".
	Theme string `json:"theme,omitempty"`
	// Icons es "auto", "nerd", "emoji" u "off" (ver -icons).
	Icons string `json:"icons,omitempty"`
}

var (
	validThemes = map[string]bool{"": true, "auto": true, "dark": true, "light": true}
	validIcons  = map[string]bool{"": true, "auto": true, "nerd": true, "emoji": true, "off": true}
)

func (s Settings) validate(path string) error {
	if !validThemes[s.Theme] {
		return fmt.Errorf("%s: theme %q no es auto, dark ni light", path, s.Theme)
	}
	if !validIcons[s.Icons] {
		return fmt.Errorf("%s: icons %q no es auto, nerd, emoji ni off", path, s.Icons)
	}
	return nil
}

// LoadSettings aplica <userDir>/settings.json y luego <projectDir>/settings.json
// sobre los valores vacíos. Un archivo ausente se ignora; uno con un campo
// desconocido o un valor inválido da error con su nombre.
func LoadSettings(userDir, projectDir string) (Settings, error) {
	var out Settings
	for _, dir := range []string{userDir, projectDir} {
		if dir == "" {
			continue
		}
		s, err := readSettings(filepath.Join(dir, "settings.json"))
		if err != nil {
			return Settings{}, err
		}
		if s.Theme != "" {
			out.Theme = s.Theme
		}
		if s.Icons != "" {
			out.Icons = s.Icons
		}
	}
	return out, nil
}

func readSettings(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Settings{}, nil
		}
		return Settings{}, err
	}
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Settings{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, s.validate(path)
}

// SaveTheme guarda el tema en <userDir>/settings.json conservando el resto de
// los ajustes.
func SaveTheme(userDir, theme string) error {
	path := filepath.Join(userDir, "settings.json")
	s, err := readSettings(path)
	if err != nil {
		return err
	}
	s.Theme = theme
	if err := s.validate(path); err != nil {
		return err
	}
	if err := os.MkdirAll(userDir, 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
