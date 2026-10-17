---
name: wari-phase-gate
description: Checklist de "hecho" para cerrar una fase o tarea de WARI (PLAN §13). Úsalo antes de declarar terminada una fase, antes de pedir al autor que la revise y antes de empezar la siguiente.
---

# Cierre de fase en WARI

Una fase está hecha solo si se cumple todo lo siguiente, con evidencia (comando y salida reales, nunca resumidos de memoria).

1. **Verificación completa:** `scripts/check.sh` termina en `OK`. Si staticcheck no está instalado, dilo en el resumen.
2. **Criterios de aceptación:** recorre uno por uno los de la fase en `docs/PLAN.md`. Para cada uno, el comando exacto que lo demuestra y su salida. Si alguno no se cumple, la fase no está hecha: dilo.
3. **Sin huérfanos:** si la fase lanza procesos, lanza el caso de prueba, termínalo y corre `scripts/orphans.sh <pgid>`; debe decir "sin procesos".
4. **Revisiones:**
   - `/code-review` (high) sobre el diff de la fase.
   - Fases 1, 5, 6 y 13: además `/security-review`.
   - Revisión cruzada de Antigravity en modo plan, con el diff, los archivos de contrato y los criterios incrustados en el prompt:
     `agy --mode plan --model gemini-3.8-flash-high --effort high -p="$PROMPT"`
     Verifica cada hallazgo antes de aplicarlo; Antigravity puede equivocarse.
5. **Registro:** `docs/progress.md` actualizado (fase, hecho, pendiente); toda decisión nueva en `docs/decisions.md`.
6. **Fixtures:** todo `testdata/<cli>/*.jsonl` nuevo pasó por `scripts/sanitize-fixture.sh` y no contiene correos, rutas de usuario ni IDs reales.
7. **Commits:** cambios lógicos en commits separados, hechos por el líder. Sin push salvo que el autor lo pida.
8. **Resumen al autor** (regla 10 del PLAN), corto:
   - Qué quedó funcionando.
   - Cómo probarlo: comandos exactos.
   - Qué quedó pendiente, y si la fase tiene un punto de decisión (fase 3: editor propio o embebido; fase 13: sandbox), la pregunta con opciones.

No empieces la fase siguiente hasta que el autor haya visto el resumen.
