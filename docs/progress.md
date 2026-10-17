# Progreso

## Fase actual
Fase 1 — Proceso y streaming: **terminada**, a la espera de que el autor la revise. La fase 2 (TUI mínimo con un agente) no empieza hasta entonces.

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

## Pendiente
- Fase 2: panel de chat en bubbletea conectado al adaptador; cancelar con un atajo y reanudar con el ID de sesión.
- CI se ejecuta por primera vez con el primer push que apruebe el autor.
- Límite conocido: un descendiente que escape del grupo con `setsid` no se controla (ver `docs/decisions.md`).
