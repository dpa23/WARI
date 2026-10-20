package tui

import (
	"fmt"
	"strings"

	"github.com/dpa23/wari/internal/agent"
)

// Comandos con "/" del chat. Los entiende WARI y no se envían al agente: las
// CLIs corren sin pantalla y no procesan sus propios "/usage" o "/model". Lo
// que no se reconoce aquí sí se envía tal cual al agente.
//
//	/config   abre el formulario del agente (CLI, modelo, potencia, permisos)
//	/usage    uso de cada agente
//	/help     esta lista
const slashHelp = "/config (CLI, modelo, potencia) · /usage · /help"

// slash atiende un comando "/" del chat. handled es false si text no es uno de
// los comandos de WARI.
func (m Panel) slash(text string) (Panel, bool) {
	v := m.cur()
	switch strings.Fields(text)[0] {
	case "/help":
		v.add(kindInfo, slashHelp)
	case "/usage":
		for _, line := range m.usageLines() {
			v.add(kindInfo, line)
		}
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
		return m.openForm(v, "", false), true
	default:
		return m, false
	}
	m.input.Reset()
	m.refreshTo(true)
	return m, true
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
