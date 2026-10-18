# Fixtures sintéticos de codex

`codex` no tenía sesión iniciada al escribir el adaptador, así que no se
pudieron grabar respuestas reales del modelo. Los archivos `synthetic_*.jsonl`
NO son grabaciones: se derivaron a mano del esquema oficial de
`docs/codex-events.md` (codex-cli 0.163.0, `exec_events.rs`). Sirven para
ejercitar el parser, pero pueden diferir de la salida real (orden de eventos,
campos opcionales, textos).

La única grabación real es `error_unauthenticated.jsonl` (turno sin login).

| Archivo | Cubre |
|---|---|
| `synthetic_simple.jsonl` | respuesta simple y `usage` completo |
| `synthetic_command.jsonl` | `command_execution` started/completed y dos `agent_message` |
| `synthetic_file_change_accept.jsonl` | `file_change` (llega ya ejecutado), `usage` sin `cache_write_input_tokens` |
| `synthetic_declined.jsonl` | `command_execution` con `status: declined` |
| `synthetic_todo_reasoning.jsonl` | `reasoning` y `todo_list` (sin evento) |
| `synthetic_tools_misc.jsonl` | `mcp_tool_call`, `web_search`, `collab_tool_call`, y un tipo de evento y de item desconocidos |
| `synthetic_turn_failed.jsonl` | `error` de nivel superior seguido de `turn.failed` |

## Reemplazo

Tras `codex login`, ejecutar `scripts/record-codex-fixtures.sh`: graba
`simple`, `tool_read`, `write_workspace`, `write_readonly`, `resume`,
`resume_missing` y `error_model` en `testdata/codex/*.jsonl`. Hay que revisar
cada grabación, ajustar los tests que hoy leen los sintéticos para que usen
las grabaciones y borrar el sintético que sustituyan. Los casos sin
equivalente grabable de forma fiable (`declined`, `todo_list`, `mcp`,
`web_search`, `turn_failed` real) deben provocarse a mano y grabarse aparte.
