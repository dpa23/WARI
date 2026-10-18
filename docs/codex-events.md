# Eventos de `codex exec --json` (codex-cli 0.163.0)

Fuente: `codex-rs/exec/src/exec_events.rs` de openai/codex (rama main), leído el
2026-10-18, más salidas reales grabadas en `testdata/codex/`. Cada línea de
stdout es un evento JSON con campo `type`.

## Eventos de nivel superior

| `type` | Campos |
|---|---|
| `thread.started` | `thread_id` (UUID de la sesión, sirve para `codex exec resume <id>`) |
| `turn.started` | (ninguno) |
| `turn.completed` | `usage`: `input_tokens`, `cached_input_tokens`, `cache_write_input_tokens` (default 0), `output_tokens`, `reasoning_output_tokens` |
| `turn.failed` | `error.message` |
| `item.started` / `item.updated` / `item.completed` | `item` |
| `error` | `message` (en la raíz; no cierra el turno: p. ej. "Reconnecting... 2/5 (...)") |

## Items (`item.type`, snake_case; todos tienen `id`)

| `type` | Campos | Notas |
|---|---|---|
| `agent_message` | `text` | texto del agente |
| `reasoning` | `text` | |
| `command_execution` | `command`, `aggregated_output`, `exit_code` (null mientras corre), `status` | `in_progress`, `completed`, `failed`, `declined`. Empieza al lanzar el comando y completa al salir |
| `file_change` | `changes[]` {`path`, `kind`: add/delete/update}, `status` | solo llega como `item.completed` (la edición ya ocurrió); `in_progress`, `completed`, `failed` |
| `mcp_tool_call` | `server`, `tool`, `arguments`, `result`, `error`, `status` | |
| `collab_tool_call` | `tool`, `sender_thread_id`, `receiver_thread_ids`, `prompt`, `agents_states`, `status` | subagentes |
| `web_search` | `id`, `query`, `action`, `results` | |
| `todo_list` | `items[]` {`text`, `completed`} | completa al terminar el turno |
| `error` | `message` | |

## Flags verificados con `codex exec --help` (0.163.0)

- `codex exec [OPTIONS] [PROMPT]`; sin PROMPT o con `-` lee de stdin.
- `--json` (JSONL a stdout), `-m/--model`, `-C/--cd <DIR>`, `--skip-git-repo-check`.
- `-s/--sandbox read-only|workspace-write|danger-full-access`.
- Reanudar: `codex exec resume [OPTIONS] <SESSION_ID> [PROMPT]` (o `--last`).
- No usar `--dangerously-bypass-approvals-and-sandbox`.

## Límites

- `file_change` llega ya ejecutado: WARI no puede tomar el checkpoint previo
  con este evento.
- Sin sesión iniciada (`codex login`), el turno termina con varios `error`
  de reconexión (401) y un `turn.failed`; ver `testdata/codex/error_unauthenticated.jsonl`.
