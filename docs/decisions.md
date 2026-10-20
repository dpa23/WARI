# Registro de decisiones

Formato: fecha · decisión · motivo · alternativas descartadas.

## 2026-10-17 · Sistema operativo objetivo
- **Decisión:** Linux y macOS en el primer ciclo; Windows después.
- **Motivo:** desarrollo en Linux; Windows es el destino final porque ahí el consumo de recursos es más crítico, pero su manejo de procesos (Job Objects, sin `SIGSTOP`) necesita trabajo propio.
- **Descartado:** Windows desde el inicio (retrasa la fase 1); solo Linux (macOS comparte casi todo el código Unix).

## 2026-10-17 · Paquete `internal/proc`
- **Decisión:** el manejo de grupos de procesos vive en `internal/proc` (no previsto en la estructura original) detrás de una interfaz. Implementación `//go:build unix` con `Setpgid` y señales al grupo; stub Windows que compila y devuelve "no soportado" hasta implementar Job Objects.
- **Motivo:** lo usan tanto los adaptadores (`agent`) como el supervisor (`orch`); aislarlo permite cambiar la plataforma sin tocar a ninguno.
- **Pendiente:** la pausa en Windows requiere `NtSuspendProcess` (no documentado oficialmente); validar antes de prometerla.
- **Descartado:** código de procesos dentro de cada adaptador (duplicación); dentro de `orch` (los adaptadores no deben importar `orch`).

## 2026-10-17 · Adaptadores de CLI
- **Decisión:** primer adaptador `claude`; segundo `agy` (Antigravity CLI ≥ 1.4.0). `codex`, `gemini` y otros quedan para la comunidad.
- **Motivo:** son las CLIs que el autor tiene con suscripción. La CLI `gemini` no está instalada; `agy` da acceso a modelos Gemini.
- **Hechos verificados de `agy` 1.4.0:** el prompt va pegado al flag (`-p="..."`); `--output-format stream-json` emite `init` (con `conversation_id`), `step_update` (con `text_delta` y `usage`) y `result` (con `status`, `usage`, `num_turns`); se reanuda con `--conversation <id>`; en modo print los permisos se auto-deniegan y la corrida termina sin salida; `--mode plan` se ignora junto con `--disable-slash-commands`.
- **Descartado:** `codex` como adaptador del núcleo.

## 2026-10-17 · Nombre
- **Decisión:** WARI. Módulo `github.com/dpa23/wari`, binario `wari`, configuración en `~/.config/wari/` y `.wari/`.
- **Descartado:** Virú, Vicús.

## 2026-10-17 · Sin OAuth ni APIs directas
- **Decisión:** WARI solo lanza las CLIs oficiales como subprocesos headless con salida JSON en streaming.
- **Motivo:** términos de uso de los proveedores y robustez. A diferencia de multiplexores como herdr, que manejan TUIs interactivas leyendo la pantalla, WARI consume eventos estructurados.
- **Descartado:** extraer tokens OAuth de suscripciones para llamar APIs; manejar las CLIs por PTY y lectura de pantalla.

## 2026-10-17 · Árbitro determinista: staticcheck
- **Decisión:** `gofmt -l`, `go vet`, `go test -race` y `staticcheck` antes de cada commit (`scripts/check.sh`).
- **Motivo:** staticcheck es un binario instalado con `go install`, no entra a `go.mod` y es ligero en máquinas de 2 núcleos.
- **Descartado:** golangci-lint (más pesado y con más configuración, poco valor extra para este stack).

## 2026-10-17 · Skills y plugins externos (entorno del autor, no del repo)
- **Decisión:** a nivel usuario, skills `samber/cc-skills-golang` (golang-testing, golang-concurrency, golang-error-handling, golang-safety) y `mattpocock/skills@tdd`; plugin oficial `gopls-lsp` con gopls v0.23.0 (la v0.24 exige Go 1.27; Fedora trae 1.26 con `GOTOOLCHAIN=local`). Se revisó su contenido antes de usarlos: solo markdown y evals.
- **Regla:** las sugerencias de los skills no anulan la regla de dependencias mínimas; testify y goleak no se usan sin aprobación.
- **Descartado:** skills de bubbletea/TUI (poca reputación: menos de 1.2K instalaciones, repos con 37 estrellas o menos); plugins LSP de Java, Kotlin y C# (no ayudan a construir WARI). `github/awesome-copilot@java-springboot` se reevalúa en las fases 9–10.

## 2026-10-17 · CI
- **Decisión:** GitHub Actions: `go test -race`, `go vet` y `staticcheck` en ubuntu y macos; `go build` y `go vet` para Windows.
- **Motivo:** el autor no tiene Mac, y el stub de Windows debe seguir compilando.

## 2026-10-17 · Contrato de agentes (fase 1)
- **Evento `Started`** (extiende la lista de PLAN §6.2): trae el ID de sesión antes que cualquier otro evento, para poder reanudar aunque el turno se cancele (aceptación de la fase 2).
- **`Task.Permissions`**: el valor cero pide aprobación. Nunca se hereda el modo del usuario. Verificado: con la configuración del autor la CLI arrancó en `permissionMode: auto` y el agente escribió un archivo con Bash sin preguntar.
- **Estados con prefijo `Status`** (`StatusIdle`, …): `Done` y `Error` chocaban con los nombres de los eventos.
- **Un canal de eventos por agente**, vivo durante todos sus turnos; se cierra en `Kill`. `Send` lanza un turno nuevo con `--resume`.
- **Cada turno termina con exactamente un `Done`**, emitido cuando el proceso ya salió y el estado ya es Idle o Error, para que quien lo recibe pueda llamar a `Send` en el acto. Si la CLI no reporta resultado, el adaptador sintetiza `Done{IsError: true}` precedido de un `Error` con el stderr. Un turno interrumpido por `Kill` no emite `Done`; las llamadas concurrentes a `Kill` esperan a la primera.
- **Fixtures sin huella del entorno**: los eventos `system` (init, commands_changed) se graban con skills, plugins, conectores MCP, comandos y rutas de sockets vaciados; el parser no los usa.

## 2026-10-17 · Diseño de `internal/proc`
- **Pipe propio para stdout** (no `StdoutPipe`): `Wait` corre en cuanto el líder sale, sin esperar al lector; `Close` desbloquea a un lector cuyo pipe sigue abierto.
- **Sin `exec.CommandContext`**: su hook `Cancel` se ejecuta dentro del ciclo de `Wait`, y un `Kill` que espera a `Wait` se bloquearía. Una goroutine propia vigila el contexto.
- **Kill**: SIGTERM + SIGCONT al grupo (un grupo pausado no atiende SIGTERM sin continuar); si el líder no sale en el plazo de gracia (2 s), SIGKILL al grupo.
- **Restos al salir el líder**: lo que quede del grupo (procesos en segundo plano, servidores auxiliares) recibe SIGTERM y, si sigue vivo al vencer el plazo, SIGKILL. Sin restos no se envía ninguna señal. Así ningún turno deja huérfanos ni retiene stdout.
- **Sin señales tras `Done`**: una vez recogido el líder y limpiado el grupo, `Pause`, `Resume` y `Kill` no envían señales, para no alcanzar a un grupo ajeno que reutilice el PGID.
- **stderr por pipe propio** (no un `io.Writer`): `Wait` no espera a restos que retengan el pipe; se drena hasta 1 s.
- **Límite conocido**: un descendiente que llama a `setsid`/`setpgid` escapa del grupo. Si aparece en la práctica: cgroups o `PR_SET_CHILD_SUBREAPER` en Linux. `Kill` igual desbloquea la lectura cerrando los pipes.

## 2026-10-17 · Adaptador `claude` (2.1.302)
- **Flags verificados con `--help`**: `-p`, `--output-format stream-json`, `--verbose`, `--include-partial-messages`, `--resume`, `--model`, `--permission-mode manual|acceptEdits|plan`, `--permission-prompts none`. No existe `--max-turns`; los presupuestos los aplicará WARI (fase 5).
- **El prompt va después de `--`**: verificado que la CLI lo acepta; impide que un prompt como `--dangerously-skip-permissions` se interprete como flag.
- **Stdin en `/dev/null`**: sin eso la CLI espera 3 s por datos.
- **`is_error` manda**: con un modelo inexistente la CLI responde `subtype: success`, `is_error: true` y sale con código 1.
- **Fixtures** con `scripts/record-claude-fixtures.sh` en un directorio fijo y neutro (`/tmp/wari-fixture`): el texto llega en deltas y una ruta con el nombre del usuario partida entre dos deltas no se puede sanitizar después (pasó en la primera grabación).

## 2026-10-17 · Charm v2 para el TUI (fase 2)
- **Decisión:** `charm.land/bubbletea/v2` v2.1.0, `charm.land/bubbles/v2` v2.2.1, `charm.land/lipgloss/v2` v2.0.6: las versiones vigentes de las dependencias aprobadas (la v1 ya no es la línea principal). bubbletea v2.1.0 exige Go 1.26.0.
- **Transitiva nueva:** `github.com/atotto/clipboard` (la importa `bubbles/v2/textarea`); no es una dependencia directa.
- **Descartado:** bubbletea v1 (API anterior, sin `tea.View` ni cursor real).

## 2026-10-17 · Cancelar y reanudar en el panel de chat
- **Decisión:** cancelar (Esc) mata al agente con `Kill`, que es definitivo; el siguiente mensaje crea un agente nuevo con `Task.SessionID` = la sesión anterior. Continuar y reanudar son el mismo camino (`Start` con `SessionID` explícito). Una generación por agente descarta los eventos del agente cancelado.
- **Verificado con la CLI:** la sesión se puede reanudar tras matar un turno a mitad de respuesta y conserva los mensajes del usuario, pero **la CLI no guarda la respuesta parcial** del turno cancelado: el agente no la recuerda aunque el panel la muestre.
- **Pendiente (idea):** reinyectar el texto parcial cancelado en el siguiente mensaje si se quiere que el agente lo recuerde.
- **Descartado:** agregar un `Cancel()` al contrato `Agent`; no hace falta mientras cada turno sea un proceso.

## 2026-10-17 · Salida del TUI sin huérfanos
- **Registro de agentes en `main`**: todo agente creado se registra y se mata con `defer` al salir, por cualquier vía. No depende del modelo final de `Program.Run`, que es nil tras un pánico.
- **SIGHUP**: Bubble Tea solo atrapa SIGINT/SIGTERM; al cerrarse la terminal, `wari` mata el programa y el registro mata a los agentes (sus grupos no reciben el SIGHUP de la terminal). Verificado cerrando el pane de herdr a mitad de un turno.
- **`agent.ErrSessionNotFound`**: el adaptador lo emite cuando reanudar falla sin que llegue `Started` (verificado: claude 2.1.302 responde un `result` con `error_during_execution`, `num_turns: 0` y "No conversation found with session ID" en stderr). El TUI solo revisa el tipo y sugiere ctrl+n.

## 2026-10-17 · Refactor pendiente: ejecutor de turnos genérico y Cancel/Close
- **Hallazgo de `/simplify`** (altitud): `Kill` sirve a la vez para cancelar un turno y terminar el agente, lo que obliga al TUI a recrear agentes (generaciones, `dying`) y a `main` a llevar un registro. Además, la maquinaria de turnos (serialización, `Done` retenido, síntesis de `Done`, Kill/Pause) es genérica pero vive en el adaptador de claude.
- **Hecho (inicio de la fase 5):** `internal/agent/turn.Runner` implementa `agent.Agent` para CLIs de un proceso por turno; el adaptador solo aporta un `Driver` (`Spec`, `Parse`, `Name`). El contrato tiene `Cancel()` (corta el turno, el agente sigue Idle, sin Done ni eventos posteriores) y `Close()` (definitivo). El TUI usa un único agente: se eliminaron generaciones, recreación de agentes y el registro de `main` (ahora `defer ag.Close()`).

## 2026-10-17 · Editor núcleo (fase 3)
- **Buffer `[][]rune`** como pide el PLAN: con el `.java` de 5000 líneas, abrir tarda ~1,4 ms y 100 ediciones ~0,2 ms; no hace falta rope ni gap buffer.
- **Reloj inyectable para agrupar el undo**: cada llamada pública lee el reloj una sola vez; leerlo por primitiva partía una edición en varios grupos (bug detectado por el test de aceptación).
- **Implementación orquestada**: Antigravity (Claude Sonnet 4.6, `accept-edits`, limitado a `internal/editor/`) implementó; Haiku generó el fixture con `scripts/gen-java-fixture.sh`; Gemini 3.8 revisó; staticcheck encontró un bug de undo en DeleteForward. Al agotarse la cuota de Claude en Antigravity, la verificación pasó a Sonnet (respaldo previsto).

## 2026-10-17 · Editor propio, no embebido (PLAN §10.4, cerrada)
- **Decisión:** se mantiene `internal/editor`. El autor pidió el que mejor sirva para que las IAs trabajen por debajo mientras se ve o edita (referencias: herdr, orca.dev, Cursor) y delegó la elección.
- **Motivo:** WARI es dueño del buffer: puede recargar en vivo lo que edita un agente, mostrar diffs contra checkpoints y bloquear o avisar sobre archivos reclamados (fases 6 y 7) sin integraciones frágiles; no suma un proceso por editor (clave en máquinas modestas y Windows); rinde de sobra (abrir 5000 líneas en ~1,4 ms).
- **Descartado:** Helix o Neovim en un PTY: más funciones de inmediato, pero el buffer vive en otro proceso, más RAM y la coordinación con los agentes pasa por la terminal.

## 2026-10-17 · Seguridad de cambios (fase 6)
- **Checkpoint al llegar `FileEditProposed`**, antes de reenviar el evento; claves `<fecha>-a<ID>/<turno>` en `.wari/checkpoints` (los IDs se reinician por ejecución). Implementado: `checkpoint` por Gemini vía herdr; watcher, propiedad e integración por Sonnet.
- **Choques**: si un agente propone editar un archivo reclamado por otro activo, se pausa antes de editar y se avisa; reanudarlo le cede el archivo.
- **Límites conocidos**: con `-accept-edits` la CLI puede ejecutar la edición antes de que termine el checkpoint (con el modo por defecto, que pregunta, se deniega); las ediciones por shell no tienen checkpoint (se ven y atribuyen, no se revierten); revertir un turno pisa ediciones posteriores de otro agente en el mismo archivo; nada limpia `.wari/checkpoints` todavía.
- **E2e con la CLI real**: edición con checkpoint, diff y reversión del turno con SHA256 idéntico al original.

## 2026-10-17 · Gates, pipeline y bucles (fase 7)
- **Pipeline** (`internal/orch/pipeline`, `.wari/pipeline.json`): pasos Manual, Semi o Auto; Auto exige presupuesto propio y no reutiliza agentes existentes. Lo lanza el usuario; un paso Manual tras un gate espera otro Run; no hay gate tras el último paso.
- **Gate**: estado `WaitingApproval` con resultado, archivos editados y su diff contra el checkpoint; aprobar, rechazar o editar el prompt siguiente.
- **Bucles** (cualquier agente): misma herramienta con los mismos argumentos 3 veces seguidas en un turno → pausa inmediata; 3 turnos seguidos sin cambios de archivo (solo con `-accept-edits`) → el turno siguiente arranca pausado. Límite: las ediciones por shell no atribuidas no cuentan como cambio.

## 2026-10-17 · Adaptador agy (fase 8, agy 1.4.2)
- **Driver** en `internal/agent/agy` sobre `turn.Runner`, sin cambios al contrato: el segundo adaptador no obligó a tocar nada fuera de su carpeta salvo el registro de fábricas en `main` (`-agent claude|agy`).
- **Sin modo de solo lectura**: `--mode plan` solo agrega un comando `/plan`; si se le pide escribir con `write_to_file`, escribe en el proyecto (verificado). `PermissionAsk`/`Plan` → `--mode=plan` como mejor esfuerzo; la protección real con agy son los checkpoints de WARI (fase 6). Los comandos de shell sí se deniegan solos en modo print, y llegan en `result.denied_actions` sin ID ni argumentos.
- **`--conversation` con ID inexistente no falla**: abre otra conversación; el adaptador emite `ErrSessionNotFound` si `Started` trae otro ID.
- **Planes de agy** (`write_to_file` a `~/.gemini/antigravity-cli/brain/`) no cuentan como edición del proyecto (filtro por ruta, frágil si agy la cambia).
- **Aceptación**: pipeline mixto claude → agy con `{{prev}}` (test sobre CLIs falsas) y corridas reales de `wari -agent agy -prompt` sin huérfanos.

## 2026-10-17 · Índice de símbolos sin tree-sitter (fase 11)
- **Decisión:** indexador en Go puro (patrones de declaraciones de Java, Kotlin y C#, consciente de comentarios y cadenas) por defecto, más universal-ctags si está instalado.
- **Motivo:** tree-sitter exige CGO y compilación nativa pesada en máquinas de 2 núcleos; el PLAN pide un tier 2 opcional y liviano.
- **Descartado por ahora:** tree-sitter detrás de un build tag (queda como opción futura).

## 2026-10-18 · Sandbox de ejecución (PLAN §10.5, cerrada)
- **Decisión del autor:** solo Podman/Docker para aislar la ejecución y depuración (fase 13): proyecto montado en solo lectura con capa de escritura efímera, detrás de una interfaz `Sandbox`.
- **Descartado:** bubblewrap (más liviano, pero el autor prefiere contenedores reproducibles).

## 2026-10-18 · Adaptadores de codex y opencode (rama newclis)
- **opencode 2.0.18** (fixtures reales): `opencode run` **escribe sin pedir permiso** si no se le indica nada; `--agent plan` no escribió en nuestras pruebas, así que Ask y Plan usan `--agent plan` y solo `-accept-edits` lo quita. Nunca `--auto`. Sin evento final: `turn.Finisher` (nuevo, opcional) cierra el turno con la salida del proceso. `Session not found` y los errores de modelo salen como evento `error`.
- **codex 0.163.0**: `-s read-only` por defecto y `workspace-write` con `-accept-edits`; nunca los flags de bypass. Esquema leído del código fuente (`docs/codex-events.md`); solo `thread.started`, `turn.started`, `error` y `turn.failed` están grabados con la CLI real (sin sesión iniciada). El resto de fixtures son sintéticos y están declarados en `testdata/codex/SYNTHETIC.md`; `scripts/record-codex-fixtures.sh` graba los reales tras `codex login`. Reanudar una sesión inexistente falla con `no rollout found for thread id` sin `thread.started`, así que el ejecutor ya lo traduce a `ErrSessionNotFound`.
- **Edición ya ejecutada**: con codex y opencode el evento de la herramienta llega cuando la edición ya ocurrió. WARI no emite `FileEditProposed` para ellos: ni checkpoint previo ni detección de choques antes de editar (el watcher la ve después).
- **Autenticación**: WARI no maneja credenciales con ninguna CLI; cada una usa su propia sesión.

## 2026-10-20 · Atajos por SO, iconos por omisión, instalación y uso
- **macOS sin alt ni win**: `config.DefaultFor(goos)` añade la acción `leader` (`ctrl+e`). Tras la líder, la tecla siguiente se despacha como si llevara alt (`layout.handleKey`); no depende de la terminal ni de la distribución del teclado (descartado: mapear los caracteres que Option escribe, que varían por layout y tienen teclas muertas). En macOS la ayuda y la barra muestran `ctrl+e f` en vez de `alt+f` (`Keymap.Label`/`KeyLabel`) y prefieren una tecla sin alt ni F. En Linux y Windows no cambia nada. Se pierde `ctrl+e` en la entrada del chat (fin de línea).
- **Iconos**: `-icons` pasa de bool apagado a `auto|nerd|emoji|off`, con `auto` por omisión: Nerd Font solo si la terminal la trae (kitty, WezTerm, Ghostty) o `WARI_NERD_FONT=1`; si no, emoji, que no piden fuente. Antes salía apagado y el árbol parecía no tener iconos.
- **Instalación**: `scripts/install.sh` deja `wari` en `~/.local/bin` (o `$GOBIN`); no se versiona ni se usa `./wari`.
- **Uso antes que dinero**: el panel y la línea de estado muestran tokens (abreviados: 12.3k) por agente; el costo en US$ solo aparece si la CLI lo reporta (con suscripción es 0).
- **LSP**: no hay jdtls, kotlin-language-server ni csharp-ls en esta máquina. El cliente se verificó contra gopls real (`internal/lsp/real_test.go`, se omite sin gopls): initialize, completar y definición. Además `TestRealServers` (se omite con `-short` o sin el servidor): jdtls real pasa (completar `length` y definición) y en el TUI con `wari -lsp` abre "Completar (LSP)" con candidatos de jdtls; kotlin-language-server 1.3.x pasa con JDK 21 y no arranca con JDK 25.0.4.1 (muere en `JavaVersion.parse`); csharp-ls 0.28.0 pasa con el SDK de .NET 10 (instalado con dotnet-install en `~/.dotnet`); jdtls pasa con JDK 25 y no arranca con JDK 21. Los tres servidores quedan verificados contra binarios reales; cada uno pide su JDK (ver README).
- **Chat** (feedback de Codex y Grok): los resúmenes de herramientas muestran el comando o la ruta decodificados (sin `\u0026`); la línea de estado suelta ayuda, sesión y modelo antes de cortar el uso; el cuadro de reversión del diff tiene texto corto en ancho estrecho. Además: con el diff abierto la confirmación de reversión la dibuja solo el editor (antes salían el cuadro del diff, otro en el panel, el aviso de la línea de estado y otro en el historial); las rutas dentro del proyecto se muestran relativas en el chat; los enlaces Markdown a archivos locales se reducen a su texto (el destino se partía a mitad de ruta en columnas estrechas); las llamadas idénticas consecutivas a una herramienta se colapsan en una línea con `×N`; el cursor ya no tapa la "E" del placeholder; los resúmenes de herramientas admiten 240 runas. `^[[?1;2c` suelto al iniciar: causa hallada, `lipgloss/compat` consultaba el color de fondo en `init` (antes de `main`, en modo normal) y una respuesta tardía de la terminal salía como eco. Se reemplazó por `internal/tui/theme`: WARI nunca consulta por su cuenta; sin `WARI_THEME` ni `COLORFGBG` asume oscuro y el layout pide el color con `tea.RequestBackgroundColor` (bubbletea lo hace con la terminal en modo crudo) y lo aplica al llegar `BackgroundColorMsg`. Los colores adaptativos siguen igual y el artefacto desaparece. Probado en herdr con `-agent agy`; revisión del cambio por agy en un pane de herdr (modo plan), sin hallazgos. WARI no toca el tema de la terminal (solo colores adaptativos de primer plano).

## 2026-10-20 · Segunda pasada sobre el feedback de Codex y Grok
- **Diff sin checkpoint**: `ctrl+r` sobre un agente cuyas ediciones no tienen checkpoint (codex, opencode, comandos de shell) muestra el archivo contra git HEAD (`Supervisor.GitDiff`, `git show` por `internal/proc` con `LC_ALL=C`, lectura acotada con `os.Root`). Solo se ve; no se puede revertir.
- **Barra inferior**: con un choque, la información de agentes se abrevia entera (`act 0/2 · cola 0`) en vez de cortarse a mitad de palabra.
- **Comandos de herramientas**: el resumen admite 600 runas (antes 80).
- **Chips de estado**: mismo vocabulario en mayúsculas en la fila ancha y la angosta; IDLE con colores adaptativos que contrastan (subagente Sonnet).
- **Desplazamiento con ↑ en 202×49**: no se reprodujo; dos pruebas (layout y chat con texto ancho y largo) confirman que la vista nunca excede filas ni columnas. Si ocurre en Terminal.app, es de la terminal y no de la vista.
- **Sin hacer**: aviso de tokens altos de codex, forzar un choque simultáneo real, configurador interactivo de atajos.

## 2026-10-18 · Lo que reveló el CI de macOS
- El CI de `main` falló solo en macOS. Los logs exigen sesión, pero las anotaciones son públicas: el CI ahora vuelca los fallos como anotaciones.
- **proc**: en macOS, señalar un grupo con solo zombis da `EPERM` (no `ESRCH`); `Kill` y `Cancel` devolvían error. **owner.Rel** y el test del sandbox: el directorio temporal vive bajo `/var → /private/var`. **watch**: kqueue entrega algún evento tarde. Corregidos en `newclis`.
- **SIGPIPE** (modo `-prompt`): `wari -prompt x | head -1` mataba a wari sin limpiar y dejaba al agente huérfano; ahora termina su grupo.

## 2026-10-17 · Orquestación del desarrollo
- **Decisión:** Claude Code (Opus) lidera y es el único que hace commit; subagentes Sonnet implementan y Haiku hace tareas mecánicas; Antigravity (Gemini 3.8 Flash) revisa los diffs con otra familia de modelos y genera los datos del tier 1. Máximo 2 agentes activos a la vez. Commits locales; push solo a pedido del autor. Detalle en `AGENTS.md` y `CLAUDE.md`.
- **Motivo:** máquina de 2 núcleos y 5,6 GB; la diversidad de modelos encuentra errores distintos, pero todo hallazgo se verifica (Antigravity adivinó mal flags y nombres de eventos de su propia CLI).
- **Descartado:** git worktrees por agente (PLAN §3); usar Claude dentro de Antigravity salvo como respaldo de cuota; gpt-oss.
