# Progreso

## Fase actual
Fase 0 — Arranque (previa a la fase 1 del PLAN).

## Hecho
- Módulo Go `github.com/dpa23/wari` (Go 1.26).
- `docs/PLAN.md` con las decisiones resueltas, `docs/decisions.md`, `AGENTS.md`, `CLAUDE.md`, `.gitignore`.
- Scripts `scripts/check.sh`, `scripts/orphans.sh`, `scripts/sanitize-fixture.sh`; CI en `.github/workflows/ci.yml`.
- Skills del proyecto en `.claude/skills/` y permisos compartidos en `.claude/settings.json`.

## Pendiente
- Fase 1: `internal/proc` (grupos de procesos Unix + stub Windows), interfaz `Agent` y eventos, adaptador `claude` con fixtures grabados.
- CI se ejecuta por primera vez con el primer push que apruebe el autor.
