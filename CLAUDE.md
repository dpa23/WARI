@AGENTS.md

## Orquestación (Claude Code como líder)

| Rol | Modelo | Effort | Uso |
|---|---|---|---|
| Líder | sesión principal (Opus) | xhigh en contratos, procesos, concurrencia y propiedad de archivos; high en integración y revisión | Diseño, interfaces, `internal/proc`, supervisor, revisiones, commits |
| Implementador | subagente `sonnet` | high en lógica con tests; medium en vistas TUI y config | Tareas especificadas con carpetas propias y criterios de aceptación |
| Mecánico | subagente `haiku` | low | Fixtures sintéticos, `progress.md`, cambios repetitivos |
| Revisor cruzado | agente `revisor` en un pane de herdr: `herdr agent start revisor --kind agy --pane <pane> -- --mode plan --model gemini-3.8-flash-high --effort high` | — | El autor prefiere verlo trabajar en herdr. Se le da la lista de archivos y criterios con `herdr agent prompt revisor … --wait`. Respaldo headless: `agy --mode plan … -p="…"` con todo incrustado |
| Datos tier 1 | `agy` con `gemini-3.8-flash-low` en un pane de herdr | — | Snippets, plantillas, mapa de imports, en su carpeta |
| Respaldo de cuota | `agy` con `claude-sonnet-4-6` | — | Solo si se agota la cuota de Claude Code |

- Máximo 2 agentes trabajando a la vez y un solo proceso `agy`.
- Verifica cada hallazgo de Antigravity antes de aplicarlo.
- Al cerrar cada fase: skill `wari-phase-gate`, `/code-review` y, en las fases 1, 5, 6 y 13, `/security-review`.
- En herdr: panes hermanos con `--no-focus`; nunca cierres panes que no creaste.
- **Diálogos de agy en herdr**: nunca respondas con un Enter a ciegas ni con un bucle automático. Un Enter sobre el diálogo equivocado sacó una vez al revisor del modo plan al modo auto (ediciones aprobadas solas). Antes de cada respuesta, lee el diálogo con `herdr agent read <agente> --source visible` y comprueba la etiqueta del modo (`plan · Gemini`). Pide las revisiones "sin ejecutar comandos ni leer fuera del repo" para que no haya diálogos.
- No uses `pkill -f`/`pgrep -f` con un patrón que aparezca en tu propio comando: coincide con tu shell y la mata.
