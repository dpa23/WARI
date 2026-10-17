---
name: wari-cli-adapter
description: Procedimiento para escribir o actualizar un adaptador de CLI de agente en WARI (claude, agy, codex u otro) en internal/agent/<cli>. Úsalo antes de tocar un adaptador, al grabar fixtures de una CLI o cuando cambie el formato de salida de una CLI.
---

# Adaptador de CLI en WARI

Un adaptador traduce la salida en streaming de una CLI oficial a los eventos normalizados de `internal/agent` (`TextDelta`, `ToolCall`, `FileEditProposed`, `Usage`, `NeedsApproval`, `Done`, `Error`) e implementa la interfaz `Agent`. Nunca llama APIs directas ni usa tokens OAuth.

## 1. Conocer la CLI real (no de memoria)
- Ejecuta `<cli> --version`, `<cli> --help` y la subayuda del modo headless. Usa solo los flags que aparecen ahí.
- Anota en el adaptador la versión mínima soportada (constante `MinVersion`) y los flags usados.
- Lo que diga un modelo sobre el formato (incluido el propio agente de esa CLI) es una hipótesis hasta verlo en una salida grabada.

## 2. Grabar fixtures
- Graba corridas reales en `testdata/<cli>/<caso>.jsonl`: respuesta simple, uso de herramienta, edición de archivo, petición de permiso, error, y reanudación de sesión si la CLI la soporta.
- Usa un directorio temporal como cwd para no tocar el repo.
- Sanitiza antes de versionar: `scripts/sanitize-fixture.sh testdata/<cli>/*.jsonl`. Revisa el aviso de nombre de usuario.

## 3. Parser con tests
- El parser es una función pura de `[]byte` (una línea) a eventos normalizados; se prueba contra los fixtures.
- Una línea con formato desconocido produce un `Error` claro que nombra la CLI, la versión y el campo faltante; nunca un pánico ni un silencio.
- Los eventos desconocidos pero bien formados se ignoran de forma explícita (y se cuentan en un test).

## 4. Proceso
- Lanza la CLI con `internal/proc` (grupo de procesos propio). Nunca `exec.Command` directo desde el adaptador.
- `Kill` termina al grupo completo; el test de kill comprueba con `scripts/orphans.sh` que no quedan procesos.

## 5. Aislamiento
- Todo el código vive en `internal/agent/<cli>/`. Si necesitas tocar `orch`, `tui` u otro adaptador, la abstracción está mal: detente y propón el cambio de contrato al líder.

## Notas por CLI
- **claude (2.1.302):** `-p --output-format stream-json --verbose --include-partial-messages`, `--resume <id>`, `--permission-mode manual|acceptEdits|plan` con `--permission-prompts none`, y el prompt después de `--`. Stdin en `/dev/null` (si no, espera 3 s). `is_error` manda sobre `subtype`. Las denegaciones llegan en `result.permission_denials`. Fixtures: `scripts/record-claude-fixtures.sh`.
- **agy (≥ 1.4.0):** el prompt va pegado al flag (`-p="..."`). `--output-format stream-json` emite `init` (`conversation_id`, `model`, `tools`), `step_update` (`step_index`, `state`, `step_type`, `text_delta`, `usage`) y `result` (`status`, `response`, `num_turns`, `usage`). Reanuda con `--conversation <id>`. En modo print los permisos se auto-deniegan y la corrida termina sin salida: hacen falta reglas `permissions.allow`. `--mode plan` se ignora junto con `--disable-slash-commands`.
