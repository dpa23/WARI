# AGENTS.md — WARI

Editor de código TUI en Go con agentes múltiples (las CLIs oficiales `claude` y `agy` como subprocesos headless). Plan completo: `docs/PLAN.md`. Decisiones: `docs/decisions.md`. Estado: `docs/progress.md`.

## Reglas
1. **Una fase a la vez.** No empieces la siguiente sin cumplir los criterios de aceptación de la actual y sin que el autor la haya visto.
2. **Pequeño y verificable.** Un cambio lógico por commit. Antes de entregar: `scripts/check.sh` en verde.
3. **Tests primero en lo difícil:** buffer, undo/redo, snippets, parsers de eventos de CLIs, supervisor de procesos.
4. **No inventes flags ni formatos de las CLIs.** Ejecuta `<cli> --help` y graba salidas reales en `testdata/<cli>/*.jsonl` (sanitizadas con `scripts/sanitize-fixture.sh`). Declara la versión mínima en el adaptador.
5. **Dependencias mínimas:** bubbletea, bubbles, lipgloss, chroma, fsnotify, creack/pty. Cualquier otra requiere aprobación y una entrada en `docs/decisions.md`. Los tests usan solo el paquete `testing` de la biblioteca estándar: testify, goleak u otras requieren aprobación aunque un skill las sugiera.
6. **Decisión abierta → detente y pregunta** (PLAN §10) con opciones concretas.
7. **Procesos:** todo hijo en su propio grupo, terminable con sus descendientes (`internal/proc`). Nada se da por terminado si `scripts/orphans.sh` muestra huérfanos.
8. **Nada destructivo sin confirmar:** no borres archivos del autor, no reescribas el historial de git, nada contra bases de datos reales. Push solo a pedido del autor.
9. **Registro:** decisiones en `docs/decisions.md`, avance en `docs/progress.md`.

## Arquitectura
- `editor` y `agent` **no se importan entre sí**; se comunican vía `tui` y `orch` con mensajes. Un adaptador nunca conoce el editor.
- Añadir un adaptador no debe tocar nada fuera de `internal/agent/<cli>`.
- El código de plataforma va en archivos con build tags (`_unix.go`, `_windows.go`).

## Trabajo con varios agentes
- **Propiedad por paquete:** cada tarea declara sus carpetas; edita solo esas. Los contratos (`internal/agent/agent.go`, eventos, interfaces de `orch` y `proc`), `go.mod`/`go.sum`, `AGENTS.md` y `docs/decisions.md` son del líder.
- **Solo el líder hace commit.** Los demás agentes entregan con `go vet` y `go test ./internal/<pkg>/...` en verde.
- Un solo `go test` a la vez en la máquina de desarrollo (2 núcleos).

## Comandos
```sh
scripts/check.sh                 # gofmt, vet, staticcheck, test -race, vet cruzado linux/darwin/windows
go test ./internal/<pkg>/...     # tests de un paquete
scripts/orphans.sh <pgid>        # procesos vivos de un grupo
scripts/sanitize-fixture.sh <archivo.jsonl>
```
