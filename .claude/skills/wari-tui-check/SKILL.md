---
name: wari-tui-check
description: Prueba end-to-end del TUI de WARI dentro de herdr - compilar, abrir en un pane hermano, enviar teclas, leer la pantalla y redimensionar. Úsalo para verificar criterios de aceptación visibles del TUI (fases 2, 4 y 5) cuando la sesión corre dentro de herdr.
---

# Verificación del TUI con herdr

Requisito: `test "${HERDR_ENV:-}" = 1`. Si falla, no estás dentro de herdr: dilo y usa la verificación por tests.

## Pasos
1. **Compilar fuera del repo:**
   `go build -o "$TMPDIR/wari" ./cmd/wari` (o el directorio scratchpad de la sesión).
2. **Abrir un pane hermano sin quitarle el foco al autor:**
   `herdr pane split --current --direction down --cwd "$PWD" --no-focus`
   Toma el ID de `.result.pane.pane_id`; no lo deduzcas.
3. **Lanzar y esperar:**
   `herdr pane run <pane> "$TMPDIR/wari <args>"`
   `herdr pane wait-output <pane> --match "<texto esperado>" --timeout 15000`
4. **Interactuar:**
   - Texto: `herdr pane send-text <pane> "<texto>"`
   - Teclas: `herdr pane send-keys <pane> enter`, `ctrl+c`, `esc`, `tab`, …
5. **Leer evidencia:**
   `herdr pane read <pane> --source visible` (usa `--format ansi` solo si el color es parte del criterio).
6. **Redimensionar** (fase 4):
   `herdr pane resize --pane <pane> --direction up --amount 0.2`, luego vuelve a leer `visible` y comprueba que el layout no se rompe.
7. **Procesos:** antes de cerrar, si el criterio involucra agentes, obtén su grupo con `herdr pane process-info --pane <pane>` y, tras salir de WARI, corre `scripts/orphans.sh <pgid>`.
8. **Limpiar:** sal de WARI con su atajo, cierra **solo el pane que creaste** con `herdr pane close <pane>` y borra el binario temporal.

Incluye en la evidencia la salida de `pane read` de cada paso relevante, no una descripción.
