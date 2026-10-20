package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/tui/theme"
)

// Comandos con "/" del chat. Los entiende WARI y no se envían al agente: las
// CLIs corren sin pantalla y no procesan sus propios "/usage" o "/model". Lo
// que no se reconoce aquí sí se envía tal cual al agente.
//
//	/config   abre el formulario del agente (CLI, modelo, potencia, permisos)
//	/theme    muestra o cambia el tema (se guarda en settings.json)
//	/keys     dónde cambiar los atajos
//	/usage    uso de cada agente
//	/help     esta lista
const slashHelp = "/config (CLI, modelo, potencia) · /theme [auto|dark|light] · /keys · /usage · /help"

// slash atiende un comando "/" del chat. handled es false si text no es uno de
// los comandos de WARI.
func (m Panel) slash(text string) (Panel, tea.Cmd, bool) {
	v := m.cur()
	switch strings.Fields(text)[0] {
	case "/help":
		v.add(kindInfo, slashHelp)
	case "/usage":
		for _, line := range m.usageLines() {
			v.add(kindInfo, line)
		}
	case "/theme":
		for _, line := range slashTheme(strings.Fields(text)[1:]) {
			v.add(kindInfo, line)
		}
	case "/keys":
		dir, _ := config.UserDir()
		v.add(kindInfo, "atajos: F1 o ? abren la ayuda con todos; se cambian en "+filepath.Join(dir, "keys.json")+" (wari -keys-init lo crea con los de tu sistema, wari -keys los muestra)")
	case "/config":
		if v.id != 0 {
			v.add(kindInfo, "/config: "+v.name+" ya empezó con "+v.cli+"; "+m.keys.AgentNew.Help().Key+" crea otro agente con otra configuración")
			break
		}
		if len(m.cfg.Infos) == 0 {
			v.add(kindInfo, "/config: no hay CLIs configurables")
			break
		}
		m.input.Reset()
		m, cmd := m.openForm(v, "", false)
		return m, cmd, true
	default:
		return m, nil, false
	}
	m.input.Reset()
	m.refreshTo(true)
	return m, nil, true
}

// slashTheme atiende /theme: sin argumento muestra el tema vigente; con
// auto, dark o light lo guarda en settings.json y, con dark o light, lo aplica
// al momento (auto se aplica al reiniciar WARI, que vuelve a detectarlo).
func slashTheme(args []string) []string {
	cur := "claro"
	if theme.Dark() {
		cur = "oscuro"
	}
	if len(args) == 0 {
		return []string{"tema vigente: " + cur + " · /theme auto|dark|light lo cambia y lo guarda"}
	}
	pref := strings.ToLower(args[0])
	if pref != "auto" && pref != "dark" && pref != "light" {
		return []string{"/theme: " + args[0] + " no es auto, dark ni light"}
	}
	dir, err := config.UserDir()
	if err == nil {
		err = config.SaveTheme(dir, pref)
	}
	if err != nil {
		return []string{"/theme: no se pudo guardar: " + err.Error()}
	}
	switch pref {
	case "dark":
		theme.SetDark(true)
	case "light":
		theme.SetDark(false)
	default:
		return []string{"tema guardado: auto (se detecta de nuevo al reiniciar WARI)"}
	}
	return []string{"tema " + pref + " aplicado y guardado en settings.json"}
}

// usageLines resume el uso de cada agente: tokens y turnos, y el costo solo si
// la CLI lo reporta.
func (m Panel) usageLines() []string {
	var total int64
	lines := make([]string, 0, len(m.views)+1)
	for _, v := range m.views {
		state := "sin iniciar"
		if v.hasSnap {
			state = strings.ToUpper(v.snap.State.String())
		}
		line := fmt.Sprintf("%s · %s · %s · %s tok · t%d", v.name, cmpOr(v.cli, "—"), state, compactTokens(v.snap.Tokens), v.snap.Turns)
		if v.snap.CostUSD > 0 {
			line += fmt.Sprintf(" · US$%.4f", v.snap.CostUSD)
		}
		if b := m.cfg.Budget; b.MaxTokens > 0 {
			line += fmt.Sprintf(" (límite %s)", compactTokens(b.MaxTokens))
		}
		lines = append(lines, line)
		total += v.snap.Tokens
	}
	return append(lines, "total: "+compactTokens(total)+" tok")
}

// effectivePermissions son los permisos del agente: los que eligió en el
// formulario (un orquestador solo planea) y, si no, los de la configuración.
func (m Panel) effectivePermissions(v *agentView) agent.PermissionMode {
	switch {
	case v.role == roleOrchestrator:
		return agent.PermissionPlan
	case v.perm != nil:
		return *v.perm
	}
	return m.cfg.Task.Permissions
}

func cmpOr(vals ...string) string {
	for _, s := range vals {
		if s != "" {
			return s
		}
	}
	return ""
}
