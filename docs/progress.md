# Progreso

## Fase actual
**Las 14 fases del PLAN están implementadas.** Pendiente: revisión del autor, push y CI (aún no ejecutado), pruebas con servidores LSP/DAP reales (no instalados en la máquina de desarrollo).

## Hecho
### Fase 0 — Arranque
- Módulo Go `github.com/dpa23/wari` (Go 1.26); `docs/PLAN.md`, `docs/decisions.md`, `AGENTS.md`, `CLAUDE.md`, `.gitignore`.
- Scripts de verificación, CI (Linux, macOS, build Windows), skills del proyecto y permisos compartidos.

### Fase 1 — Proceso y streaming
- `internal/proc`: procesos como líderes de su propio grupo; Pause/Resume (SIGSTOP/SIGCONT al grupo); Kill ordenado y luego forzado; limpieza de restos al salir el líder; stub de Windows que compila.
- `internal/agent`: contrato `Agent`, eventos normalizados (más `Started`), `PermissionMode` explícito.
- `internal/agent/claude`: parser de `stream-json` (claude 2.1.302) y adaptador por turnos con `--resume`; prompt después de `--`; permisos nunca heredados del usuario.
- `testdata/claude/*.jsonl`: 6 salidas reales, regrabables con `scripts/record-claude-fixtures.sh`, sin datos personales ni huella del entorno.
- `cmd/wari -prompt "..." [-model m] [-kill-after d]`: vuelca el streaming y termina el grupo con Ctrl+C o el plazo.
- Aceptación con la CLI real: kill a los 10 s tras 251 palabras, SIGINT tras 218 y turno completo; en los tres, `scripts/orphans.sh` sin procesos y ningún descendiente vivo.
- Revisiones: `/code-review` (10 hallazgos, todos atendidos), Antigravity (8: 2 nuevos aplicados, 1 en parte, 4 ya resueltos, 1 rechazado con motivo), `/security-review` (sin hallazgos; observación de privacidad aplicada).

### Fase 2 — TUI mínimo con un agente
- `internal/tui`: panel de chat en Bubble Tea v2 (historial, estado, entrada); eventos en lotes; Esc cancela, el siguiente mensaje reanuda con el ID de sesión; ctrl+n sesión nueva; ctrl+c sale.
- `cmd/wari`: sin flags abre el TUI; registro de agentes y SIGHUP para no dejar huérfanos.
- Aceptación en herdr con la CLI real: conversar, cancelar a mitad de respuesta, reanudar (`--resume` con la misma sesión; el agente recuerda el pedido, no el texto parcial), ctrl+n, cerrar la terminal a mitad de turno: sin procesos vivos.
- Revisiones: `/code-review` (9, atendidos), Antigravity por herdr (3: 1 aplicado, 2 rechazados), `/simplify` (aplicados los de eficiencia, simplificación y el error tipado; el refactor de altitud queda para la fase 5).

### Fase 3 — Editor núcleo
- `internal/editor`: buffer `[][]rune`, cursor y selección, movimientos (carácter, palabra, línea, doc), copy/cut/paste sin portapapeles del sistema.
- Inserción con auto-indentación al insertar salto de línea (copia la indentación de la línea anterior).
- Undo/redo con pila de operaciones inversas; agrupación por pausa configurable (por defecto 1 s); reloj inyectable para tests.
- Abrir/guardar UTF-8 preservando estilo de salto de línea (LF o CRLF) y trailing newline; guardado atómico (temp + fsync + rename) conservando permisos.
- Búsqueda y reemplazo con regexp de Go: siguiente coincidencia desde el cursor con wrap-around, reemplazar una y todas; expansión de grupos de captura; todo deshacible como un solo paso.
- Aceptación: `TestAcceptanceLargeJavaFile` (5000 líneas: abrir ~1,4 ms, 100 ediciones ~0,2 ms, 100 undos y guardado idéntico byte a byte).
- Revisiones: staticcheck (bug de undo en DeleteForward), test de aceptación (undo partido por lecturas de reloj), Gemini 3.8 (5 hallazgos de redo, saltos y permisos, los 5 reproducidos con tests y corregidos).

### Fase 4 — Layout editor + chat
- `internal/config`: atajos desde JSON (usuario y proyecto). Implementado por Gemini 3.8 vía herdr.
- `internal/tui/editorview`: editor con resaltado chroma (Java, Kotlin, C#), selección y scroll; ~3,6 ms por render con 5000 líneas.
- `internal/tui/filetree` y `internal/tui/layout`: árbol, editor y agentes; foco con tab/shift+tab; paleta ctrl+p con búsqueda difusa; plegado automático al achicar; todo desde `config.Keymap`.
- E2e en herdr: abrir por paleta, editar y guardar, redimensionar sin desbordes (paneles plegados a 35 columnas y restaurados), chatear con el agente desde el panel derecho, salir sin huérfanos.

### Fase 5 — Multiagente, cola y presupuestos
- Refactor: `internal/agent/turn.Runner` genérico; contrato con `Cancel` (turno) y `Close` (agente).
- `internal/orch`: cola FIFO con límite de activos (2), presupuestos de turnos, tokens y tiempo; pausa, parada por agente y global; carreras halladas por Gemini corregidas con tests.
- Panel multiagente: lista con estado, turnos, tokens y costo; chat por agente; ctrl+t, alt+↑/↓, ctrl+o, ctrl+x, ctrl+k; barra con activos y cola siempre visible.
- E2e en herdr con agentes reales: 3 agentes con límite 2 (uno en cola), pausa que conserva cupo, stop que arranca la cola, parada global y salida con un agente pausado sin huérfanos.
- Revisiones: /code-review de la fase 4 (10, atendidos), Gemini sobre orch (5: 4 reproducidos y corregidos, 1 ya correcto), /security-review (sin hallazgos).

### Fase 6 — Seguridad de cambios
- `internal/workspace/checkpoint` (Gemini vía herdr), `watch` y `owner`; integración en `orch`; diff y reversión desde el panel; `-accept-edits`.
- Aceptación: tests de dos agentes en paralelo, choque con pausa previa y reversión con SHA256 idéntico; e2e con la CLI real (editar, diff, revertir).

### Fase 7 — Gates y pipeline
- Pipeline de pasos con gates de aprobación (resumen y diff), detección de bucles con pausa automática; atajos alt+p/alt+y/alt+n/alt+e.
- Aceptación (tests): pipeline de 3 pasos que se detiene en el gate y sigue al aprobar; rechazar detiene; agente en bucle se pausa solo.
- Seguridad de la fase 6: vulnerabilidad de manifest fabricado corregida con os.Root (tests reproducen el ataque).

### Fase 8 — Adaptador agy
- `internal/agent/agy` sobre el ejecutor genérico; `-agent claude|agy`; pipeline mixto claude → agy. agy no tiene modo de solo lectura (ver decisiones).

### Fase 9 — Snippets y plantillas
- Motores `internal/snippets` y `internal/templates` (Sonnet); datos en `assets/` (34 snippets, 17 plantillas, mapas de imports; Gemini vía herdr), embebidos en el binario.
- Tab expande snippets con sesión de tabstops; alt+f crea archivos desde plantilla con `package`/`namespace` derivados.
- UX de Gemini: ayuda de atajos (?/F1), paleta ampliada, alertas de presupuesto (docs/ux-review.md).

### Fase 10 — Imports y Spring Boot
- `internal/imports`: inserción ordenada y sin duplicar (Java/Kotlin `import`, C# `using`); alt+i con selector si hay varios candidatos (List → java.util primero).
- Editor: `ReplaceRange` y grupos de undo; `filetree.Refresh`.
- Seguridad (Gemini): creación de archivos vía os.Root; lectura acotada de datos del proyecto.

### Fase 11 — Estructura e índice
- `internal/index`: indexador en Go puro (Java, Kotlin, C#) consciente de comentarios y cadenas; 5000 líneas en ~1,7 ms; ctags opt-in con `-ctags`.
- Outline (alt+o), ir a definición (F12/alt+d), completar (alt+/), imports de tipos propios. Revisión de Gemini sin hallazgos nuevos.

### Fase 12 — LSP opcional
- `internal/lsp`: cliente JSON-RPC (initialize, didOpen/didChange, completion, definition, diagnósticos, cancelación), gestor bajo demanda para jdtls, kotlin-language-server y csharp-ls, RSS por grupo de procesos. `proc.Spec.Stdin` para hablar con el servidor.

### Fase 13 — Sandbox (Podman/Docker)
- `internal/sandbox`: overlay en podman o copia en docker, red none por defecto, límites, salidas fuera del proyecto y Promote con os.Root. Integración real con podman: proyecto idéntico por hash.
- Seguridad (Gemini): 4 fallos corregidos (lectura de archivos del anfitrión vía symlink en el log, contenedor huérfano al cancelar, montaje de sockets del motor, Promote durante la ejecución).
- TUI: alt+g ejecuta el perfil (maven/gradle/dotnet) con logs y alt+x para detener; alt+w promueve un artefacto.

### Fase 14 — Debugger (DAP)
- `internal/dap`: cliente y sesión (breakpoints, pasos, pila, ámbitos, árbol de variables), netcoredbg y java-debug (vía hook de puerto).
- TUI: F9/F5/F8/F10/F11 (con alternativas alt+letra), línea resaltada, paneles de pila y variables navegables; `.wari/launch.json` con argumentos opacos.
- Sin probar con adaptadores reales (no instalados); java-debug requiere integrar el plugin en jdtls.

### UX de Gemini (puntos 7 y 10)
- UX puntos 7 y 10 de `docs/ux-review.md`: banner de choques visible con el panel plegado y resolución asistida; colores adaptativos para fondos claros y oscuros. Verificado en herdr (editor y colores) y con tests.

### Rama newclis (2026-10-18)
- Adaptadores de `codex` y `opencode` (`-agent codex|opencode`, pasos de pipeline), `turn.Finisher`, README reescrito, arreglos de macOS y de SIGPIPE. CI en verde en Linux, macOS y Windows.
- Sin verificar con la CLI real: el parser de codex salvo errores (sin sesión iniciada; ver `testdata/codex/SYNTHETIC.md`).

### Pulido (2026-10-20)
- Atajos por SO con tecla líder para macOS, iconos `auto|nerd|emoji|off` por omisión, `scripts/install.sh`, uso en tokens por agente. Verificado en herdr (iconos emoji, `ctrl+e f` abre las plantillas). Ver `docs/decisions.md`.

### Giro a terminal real (2026-10-20)
- Fase 1: `proc.Spec.PTY`, `internal/term.Pane` (x/vt) y el modo terminal del panel de agentes; `claude` real con `/usage` dentro de WARI. Faltan: estado por título/salida (fase 3), uso desde los registros de cada CLI (4), MCP y hooks (5) y orquestación entre paneles (6). Ver `docs/decisions.md`.

## Pendiente

- CI se ejecuta por primera vez con el primer push que apruebe el autor.
- Límite conocido: un descendiente que escape del grupo con `setsid` no se controla (ver `docs/decisions.md`).

### Tiling libre (2026-10-22)
- `internal/tui/tile` (motor), `tui.TermAgent`, `layout/tiles.go`. Agentes en terminal como tiles movibles con teclado y ratón; panel de chat oculto por omisión; iconos Nerd/emoji/ASCII (`internal/tui/icons`) y fuente JetBrainsMono Nerd en `assets/fonts` (`scripts/install-font.sh`). Verificado en herdr con `claude` real (formulario superpuesto, dos agentes, modo tiles, maximizar). Ver `docs/decisions.md`.
