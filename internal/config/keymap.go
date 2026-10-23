package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Keymap asocia nombres de acciones a combinaciones de teclas en formato
// compatible con bubbletea v2 (p. ej. "ctrl+p", "enter", "alt+enter", "pgup").
type Keymap map[string][]string

// Clone devuelve una copia profunda del mapa de teclas.
func (k Keymap) Clone() Keymap {
	if k == nil {
		return nil
	}
	cp := make(Keymap, len(k))
	for act, keys := range k {
		cp[act] = append([]string(nil), keys...)
	}
	return cp
}

// Default devuelve el mapa de teclas predeterminado del sistema donde corre WARI.
func Default() Keymap { return DefaultFor(runtime.GOOS) }

// DefaultFor devuelve el mapa predeterminado para un sistema operativo (valor
// de runtime.GOOS). En macOS no existe la tecla Win y Option escribe
// caracteres tipográficos salvo que la terminal la use como Meta, así que las
// acciones con alt+ también se alcanzan con la tecla líder ("leader", ctrl+e
// por omisión) seguida de la letra, y la ayuda las muestra de esa forma.
// Eso no depende de la terminal ni de la distribución del teclado.
func DefaultFor(goos string) Keymap {
	km := baseKeymap()
	km["leader"] = []string{"ctrl+e"}
	return km
}

// Label devuelve cómo se escribe la acción en goos para mostrarla al usuario:
// la primera tecla utilizable. En macOS prefiere una tecla sin alt+ ni F (el
// sistema captura las F) y, si no la hay, la secuencia con líder ("ctrl+e f").
func (k Keymap) Label(goos, action string) string {
	keys := k[action]
	if len(keys) == 0 {
		return ""
	}
	if goos != "darwin" {
		return keys[0]
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "alt+") && !isFunctionKey(key) {
			return key
		}
	}
	for _, key := range keys {
		if strings.HasPrefix(key, "alt+") {
			return k.KeyLabel(goos, key)
		}
	}
	return keys[0]
}

// KeyLabel escribe una tecla como se pulsa en goos: en macOS "alt+f" es la
// secuencia con líder ("ctrl+e f"); en el resto no cambia.
func (k Keymap) KeyLabel(goos, key string) string {
	if goos != "darwin" || !strings.HasPrefix(key, "alt+") {
		return key
	}
	leader := "ctrl+e"
	if l := k["leader"]; len(l) > 0 {
		leader = l[0]
	}
	return leader + " " + strings.TrimPrefix(key, "alt+")
}

func isFunctionKey(key string) bool {
	key = key[strings.LastIndex(key, "+")+1:]
	if len(key) < 2 || key[0] != 'f' {
		return false
	}
	for _, r := range key[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func baseKeymap() Keymap {
	return Keymap{
		"send":            []string{"enter"},
		"newline":         []string{"alt+enter", "ctrl+j"},
		"cancel":          []string{"esc"},
		"quit":            []string{"ctrl+c"},
		"new_session":     []string{"ctrl+n"},
		"page_up":         []string{"pgup"},
		"page_down":       []string{"pgdown"},
		"focus_next":      []string{"tab"},
		"focus_prev":      []string{"shift+tab"},
		"focus_tree":      []string{"alt+1"},
		"focus_editor":    []string{"alt+2"},
		"focus_agents":    []string{"alt+3"},
		"palette":         []string{"ctrl+p"},
		"save":            []string{"ctrl+s"},
		"undo":            []string{"ctrl+z"},
		"redo":            []string{"ctrl+y"},
		"find":            []string{"ctrl+f"},
		"new_file":        []string{"alt+f"},
		"import_type":     []string{"alt+i"},
		"outline":         []string{"alt+o"},
		"goto_definition": []string{"f12", "alt+d"},
		"complete":        []string{"alt+/", "ctrl+space"},
		"toggle_lsp":      []string{"alt+l"},
		"diagnostics":     []string{"alt+m"},
		"toggle_tree":     []string{"ctrl+b"},
		"toggle_agents":   []string{"ctrl+g"},
		"zoom_agents":     []string{"alt+z"},
		"agent_new":       []string{"ctrl+t"},
		"agent_next":      []string{"alt+down"},
		"agent_prev":      []string{"alt+up"},
		"agent_pause":     []string{"ctrl+o"},
		"agent_stop":      []string{"ctrl+x"},
		"stop_all":        []string{"ctrl+k"},
		"diff_file":       []string{"ctrl+r"},
		"revert_file":     []string{"alt+r"},
		"revert_turn":     []string{"alt+t"},
		"revert_agent":    []string{"alt+a"},
		"pipeline_run":    []string{"alt+p"},
		"gate_approve":    []string{"alt+y"},
		"gate_reject":     []string{"alt+n"},
		"gate_edit":       []string{"alt+e"},

		// Ejecución en el sandbox y depuración (fases 13 y 14). Las teclas F
		// son las convencionales de los IDE; F10 y F11 las suelen tomar el
		// menú de la terminal o el gestor de ventanas, y en macOS las F las
		// captura el sistema, por eso cada acción tiene una alternativa alt+.
		"focus_dock":        []string{"alt+v"},
		"run_project":       []string{"alt+g"},
		"run_stop":          []string{"alt+x"},
		"promote_artifact":  []string{"alt+w"},
		"toggle_breakpoint": []string{"f9", "alt+b"},
		"debug_start":       []string{"f5", "alt+s"},
		"debug_continue":    []string{"f8", "alt+c"},
		"debug_next":        []string{"f10", "alt+j"},
		"debug_step_in":     []string{"f11", "alt+k"},
		"debug_step_out":    []string{"shift+f11", "alt+u"},
		"debug_stop":        []string{"shift+f5", "alt+q"},
	}
}

// UserDir devuelve la ruta del directorio de configuración del usuario
// ($XDG_CONFIG_HOME/wari o ~/.config/wari en Unix, %AppData%\wari en Windows).
func UserDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wari"), nil
}

// Load carga la configuración de teclas partiendo de Default(), aplicando
// después <userDir>/keys.json y finalmente <projectDir>/keys.json si existen.
// Si un archivo no existe, se ignora silenciosamente.
// Si contiene una acción desconocida o una lista de teclas vacía, devuelve
// un error indicando el nombre del archivo y la acción.
func Load(userDir, projectDir string) (Keymap, error) {
	km := Default()

	if userDir != "" {
		if err := applyFile(filepath.Join(userDir, "keys.json"), km); err != nil {
			return nil, err
		}
	}
	if projectDir != "" {
		if err := applyFile(filepath.Join(projectDir, "keys.json"), km); err != nil {
			return nil, err
		}
	}

	return km, nil
}

func applyFile(path string, km Keymap) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s: JSON inválido: %w", path, err)
	}

	actions := make([]string, 0, len(raw))
	for act := range raw {
		actions = append(actions, act)
	}
	sort.Strings(actions)

	for _, act := range actions {
		if _, ok := km[act]; !ok {
			return fmt.Errorf("%s: acción desconocida %q", path, act)
		}
		if len(raw[act]) == 0 {
			return fmt.Errorf("%s: lista vacía para la acción %q", path, act)
		}
	}

	for _, act := range actions {
		km[act] = append([]string(nil), raw[act]...)
	}

	return nil
}

// Conflicts devuelve descripciones ordenadas de teclas asignadas a más de una acción.
// Si no hay conflictos, devuelve un slice vacío.
func (k Keymap) Conflicts() []string {
	keyActions := make(map[string]map[string]struct{})
	for action, keys := range k {
		for _, key := range keys {
			if key == "" {
				continue
			}
			if keyActions[key] == nil {
				keyActions[key] = make(map[string]struct{})
			}
			keyActions[key][action] = struct{}{}
		}
	}

	var conflictKeys []string
	for key, actions := range keyActions {
		if len(actions) > 1 {
			conflictKeys = append(conflictKeys, key)
		}
	}
	sort.Strings(conflictKeys)

	res := make([]string, 0, len(conflictKeys))
	for _, key := range conflictKeys {
		actions := make([]string, 0, len(keyActions[key]))
		for act := range keyActions[key] {
			actions = append(actions, act)
		}
		sort.Strings(actions)
		res = append(res, fmt.Sprintf("tecla %q asignada a más de una acción: %s", key, strings.Join(actions, ", ")))
	}

	return res
}

// Describe lista las acciones con sus teclas como se pulsan en goos, una por
// línea y ordenadas ("new_file  ctrl+e f"). En macOS las teclas con alt+ se
// muestran como secuencia con líder.
func (k Keymap) Describe(goos string) []string {
	actions := make([]string, 0, len(k))
	width := 0
	for a := range k {
		actions = append(actions, a)
		width = max(width, len(a))
	}
	sort.Strings(actions)
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		labels := make([]string, len(k[a]))
		for i, key := range k[a] {
			labels[i] = k.KeyLabel(goos, key)
		}
		out = append(out, fmt.Sprintf("%-*s  %s", width, a, strings.Join(labels, ", ")))
	}
	return out
}

// ErrKeysExist indica que keys.json ya existe y no se sobrescribe.
var ErrKeysExist = errors.New("keys.json ya existe")

// WriteTemplate escribe en dir/keys.json los atajos predeterminados de goos
// como punto de partida para editar. Nunca sobrescribe un archivo existente.
func WriteTemplate(dir, goos string) (string, error) {
	path := filepath.Join(dir, "keys.json")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(DefaultFor(goos), "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return path, ErrKeysExist
		}
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return "", err
	}
	return path, nil
}
