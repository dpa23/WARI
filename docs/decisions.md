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

## 2026-10-17 · Orquestación del desarrollo
- **Decisión:** Claude Code (Opus) lidera y es el único que hace commit; subagentes Sonnet implementan y Haiku hace tareas mecánicas; Antigravity (Gemini 3.8 Flash) revisa los diffs con otra familia de modelos y genera los datos del tier 1. Máximo 2 agentes activos a la vez. Commits locales; push solo a pedido del autor. Detalle en `AGENTS.md` y `CLAUDE.md`.
- **Motivo:** máquina de 2 núcleos y 5,6 GB; la diversidad de modelos encuentra errores distintos, pero todo hallazgo se verifica (Antigravity adivinó mal flags y nombres de eventos de su propia CLI).
- **Descartado:** git worktrees por agente (PLAN §3); usar Claude dentro de Antigravity salvo como respaldo de cuota; gpt-oss.
