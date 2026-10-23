package layout

import (
	"fmt"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dpa23/wari/internal/config"
)

// Regla de descubribilidad:
// La tecla '?' solo abre el overlay de ayuda cuando el foco no está escribiendo
// en el editor (con un archivo abierto) ni en la entrada de texto del chat.
// Esto evita interceptar el carácter '?' al redactar código o hacer preguntas.
// La tecla F1 siempre abre el overlay de ayuda, sin importar el foco.
// Esc cierra el overlay. El overlay nunca desborda en ningún tamaño de pantalla.

// helpItem representa una acción y su descripción para el overlay.
type helpItem struct {
	action string
	desc   string
}

// helpGroup representa una categoría de atajos agrupados por contexto.
type helpGroup struct {
	title string
	items []helpItem
}

// helpGroups define los 5 contextos obligatorios requeridos por WARI:
// Navegación, Editor, Agentes, Diffs y Pipeline.
func helpGroups() []helpGroup {
	return []helpGroup{
		{
			title: "Navegación",
			items: []helpItem{
				{"focus_next", "Siguiente panel"},
				{"focus_prev", "Panel anterior"},
				{"toggle_tree", "Alternar árbol de archivos"},
				{"toggle_agents", "Alternar panel de agentes"},
				{"palette", "Paleta de comandos"},
				{"page_up", "Página arriba"},
				{"page_down", "Página abajo"},
			},
		},
		{
			title: "Editor",
			items: []helpItem{
				{"save", "Guardar archivo"},
				{"undo", "Deshacer cambios"},
				{"redo", "Rehacer cambios"},
				{"find", "Buscar en archivo"},
				{"new_file", "Nuevo archivo desde plantilla"},
				{"import_type", "Importar el tipo bajo el cursor"},
				{"outline", "Outline: clases y métodos del archivo"},
				{"goto_definition", "Ir a la definición"},
				{"complete", "Completar (LSP o nombres del proyecto)"},
			},
		},
		{
			title: "Agentes",
			items: []helpItem{
				{"agent_new", "Nuevo agente"},
				{"new_session", "Nueva sesión"},
				{"agent_next", "Siguiente agente"},
				{"agent_prev", "Agente anterior"},
				{"agent_pause", "Pausar / reanudar agente"},
				{"agent_stop", "Detener turno del agente"},
				{"stop_all", "Detener todos los agentes"},
				{"send", "Enviar mensaje al agente"},
				{"newline", "Nueva línea en entrada"},
				{"cancel", "Cancelar / cerrar"},
				{"quit", "Salir de WARI"},
			},
		},
		{
			title: "Diffs",
			items: []helpItem{
				{"diff_file", "Ver diff en el centro (n/p hunks)"},
				{"revert_file", "Revertir archivo"},
				{"revert_turn", "Revertir turno"},
				{"revert_agent", "Revertir agente completo"},
			},
		},
		{
			title: "Pipeline",
			items: []helpItem{
				{"pipeline_run", "Lanzar pipeline"},
				{"gate_approve", "Aprobar gate"},
				{"gate_reject", "Rechazar gate"},
				{"gate_edit", "Editar prompt del gate"},
			},
		},
		{
			title: "Ejecución y depuración",
			items: []helpItem{
				{"focus_dock", "Ir al panel de salida / variables"},
				{"run_project", "Ejecutar el proyecto en el sandbox"},
				{"run_stop", "Detener la ejecución"},
				{"promote_artifact", "Promover un artefacto al proyecto"},
				{"toggle_breakpoint", "Poner o quitar breakpoint"},
				{"debug_start", "Depurar: iniciar o continuar"},
				{"debug_continue", "Depurar: continuar"},
				{"debug_next", "Depurar: siguiente línea"},
				{"debug_step_in", "Depurar: entrar"},
				{"debug_step_out", "Depurar: salir"},
				{"debug_stop", "Depurar: detener"},
			},
		},
		{
			title: "LSP (opcional)",
			items: []helpItem{
				{"toggle_lsp", "Encender o apagar el LSP"},
				{"diagnostics", "Diagnósticos del archivo"},
				{"tile_mode", "Modo tiles: partir, mover, redimensionar, cerrar"},
				{"tile_zoom", "Maximizar / restaurar el tile con foco"},
				{"tile_left", "Foco al tile de la izquierda (también ←↑↓→ con alt)"},
			},
		},
	}
}

// formatKeys devuelve las teclas asignadas a una acción en la configuración.
func formatKeys(km config.Keymap, action string) string {
	if km == nil {
		return "—"
	}
	keys := km[action]
	if len(keys) == 0 {
		return "—"
	}
	labels := make([]string, len(keys))
	for i, key := range keys {
		labels[i] = km.KeyLabel(runtime.GOOS, key)
	}
	return strings.Join(labels, ", ")
}

// truncate recorta s a n runas, marcando el corte con "…" si excede.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// helpKey atiende eventos de teclado con el overlay de ayuda abierto.
func (m Model) helpKey(k tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case m.is(k, "cancel") || k.String() == "?" || k.String() == "f1" || k.String() == "F1":
		m.helpOpen = false
		m.helpScroll = 0
		return m, nil
	case key.Matches(k, palKeys.up) || k.String() == "k":
		if m.helpScroll > 0 {
			m.helpScroll--
		}
	case key.Matches(k, palKeys.down) || k.String() == "j":
		m.helpScroll++
	case m.is(k, "page_up"):
		m.helpScroll = max(0, m.helpScroll-10)
	case m.is(k, "page_down"):
		m.helpScroll += 10
	}
	return m, nil
}

// helpView compone el overlay de ayuda en una cuadrícula de exactamente w columnas
// y h filas, garantizando la ausencia total de desbordes en cualquier tamaño.
func (m Model) helpView(w, h int) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	if h < 3 || w < 20 {
		// En tamaños ultra-pequeños, muestra un aviso minimalista que no desborda
		out := make([]string, h)
		msg := "Atajos (?/F1)"
		for y := 0; y < h; y++ {
			if y == 0 {
				out[y] = fit(" "+msg, w)
			} else {
				out[y] = strings.Repeat(" ", w)
			}
		}
		return out
	}

	boxW := min(w, 76)
	boxH := min(h, 24)
	innerW := max(0, boxW-4)
	innerH := max(0, boxH-2)

	hdrSt := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	keySt := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	descSt := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	borderSt := lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Background(lipgloss.Color("237"))
	titleSt := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("237"))
	bgSt := lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("252"))

	groups := helpGroups()

	var contentLines []string
	if innerW >= 66 {
		// Dos columnas: Col 1 (Navegación, Editor, Diffs), Col 2 (Agentes, Pipeline,
		// Ejecución y depuración, LSP)
		col1W := (innerW - 3) / 2
		col2W := innerW - 3 - col1W

		formatCol := func(colGroups []helpGroup, colW int) []string {
			var lines []string
			for gi, g := range colGroups {
				if gi > 0 {
					lines = append(lines, "")
				}
				hdrText := "── " + g.title + " "
				if pad := colW - lipgloss.Width(hdrText); pad > 0 {
					hdrText += strings.Repeat("─", pad)
				}
				lines = append(lines, hdrSt.Render(hdrText))
				for _, it := range g.items {
					k := formatKeys(m.km, it.action)
					maxKeyW := 13
					keyPart := fmt.Sprintf("%-13s", truncate(k, maxKeyW))
					maxDescW := max(0, colW-maxKeyW-3)
					descPart := truncate(it.desc, maxDescW)
					line := "  " + keySt.Render(keyPart) + " " + descSt.Render(descPart)
					lines = append(lines, fit(line, colW))
				}
			}
			return lines
		}

		col1 := formatCol([]helpGroup{groups[0], groups[1], groups[3]}, col1W)
		col2 := formatCol([]helpGroup{groups[2], groups[4], groups[5], groups[6]}, col2W)

		n := max(len(col1), len(col2))
		sep := borderSt.Render("│")
		for r := 0; r < n; r++ {
			c1 := ""
			if r < len(col1) {
				c1 = col1[r]
			}
			c2 := ""
			if r < len(col2) {
				c2 = col2[r]
			}
			row := fit(c1, col1W) + " " + sep + " " + fit(c2, col2W)
			contentLines = append(contentLines, fit(row, innerW))
		}
	} else {
		// Una sola columna adaptativa
		for gi, g := range groups {
			if gi > 0 {
				contentLines = append(contentLines, "")
			}
			hdrText := "── " + g.title + " "
			if pad := innerW - lipgloss.Width(hdrText); pad > 0 {
				hdrText += strings.Repeat("─", pad)
			}
			contentLines = append(contentLines, hdrSt.Render(hdrText))
			for _, it := range g.items {
				k := formatKeys(m.km, it.action)
				maxKeyW := min(14, max(8, innerW/3))
				keyPart := fmt.Sprintf("%-*s", maxKeyW, truncate(k, maxKeyW))
				maxDescW := max(0, innerW-maxKeyW-3)
				descPart := truncate(it.desc, maxDescW)
				line := "  " + keySt.Render(keyPart) + " " + descSt.Render(descPart)
				contentLines = append(contentLines, fit(line, innerW))
			}
		}
	}

	// Manejo del desplazamiento sin desbordes
	scroll := 0
	if len(contentLines) > innerH {
		maxScroll := len(contentLines) - innerH
		scroll = max(0, min(m.helpScroll, maxScroll))
	}
	visible := contentLines
	if len(contentLines) > innerH {
		visible = contentLines[scroll : scroll+innerH]
	}

	// Bordes superior e inferior
	title := " Atajos de teclado (Esc cerrar) "
	topPad := max(0, boxW-2-lipgloss.Width(title)-1)
	topBorder := borderSt.Render("┌─") + titleSt.Render(title) + borderSt.Render(strings.Repeat("─", topPad)+"┐")

	var bottomBorder string
	if len(contentLines) > innerH {
		hint := " [↑↓ desplazar · Esc cerrar] "
		botPad := max(0, boxW-2-lipgloss.Width(hint)-1)
		bottomBorder = borderSt.Render("└─") + borderSt.Render(hint) + borderSt.Render(strings.Repeat("─", botPad)+"┘")
	} else {
		bottomBorder = borderSt.Render("└" + strings.Repeat("─", max(0, boxW-2)) + "┘")
	}

	var boxLines []string
	boxLines = append(boxLines, fit(topBorder, boxW))
	for _, l := range visible {
		row := borderSt.Render("│ ") + bgSt.Render(fit(l, innerW)) + borderSt.Render(" │")
		boxLines = append(boxLines, fit(row, boxW))
	}
	for len(boxLines) < boxH-1 {
		row := borderSt.Render("│ ") + bgSt.Render(strings.Repeat(" ", innerW)) + borderSt.Render(" │")
		boxLines = append(boxLines, fit(row, boxW))
	}
	boxLines = append(boxLines, fit(bottomBorder, boxW))
	if len(boxLines) > boxH {
		boxLines = boxLines[:boxH]
	}

	padY := max(0, (h-len(boxLines))/2)
	padX := max(0, (w-boxW)/2)

	out := make([]string, h)
	for y := 0; y < h; y++ {
		if y < padY || y >= padY+len(boxLines) {
			out[y] = strings.Repeat(" ", w)
		} else {
			line := boxLines[y-padY]
			linePad := strings.Repeat(" ", padX) + line
			out[y] = fit(linePad, w)
		}
	}
	return out
}
