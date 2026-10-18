# WARI

Editor de código en terminal (TUI), escrito en Go, con varios agentes de IA trabajando por debajo mientras ves y editas. Los agentes son las CLIs oficiales (`claude` y `agy` de Antigravity) lanzadas como subprocesos; WARI es el proceso padre y decide cuándo corren, se pausan o mueren.

Pensado para máquinas modestas: sin LSP obligatorio, con límite de agentes activos y cola.

## Qué hace

- **Editor propio** con resaltado (Java, Kotlin, C#), snippets con tabstops, plantillas de archivo (incluido Spring Boot) e imports automáticos.
- **Varios agentes a la vez** con cola (2 activos por defecto), presupuestos de turnos, tokens y tiempo, pausa, parada por agente y parada global.
- **Seguridad de cambios**: checkpoint antes de cada edición de un agente, diff y reversión por archivo, turno o agente, y aviso con pausa si dos agentes quieren editar el mismo archivo.
- **Pipelines** de pasos entre agentes con gates de aprobación (resumen y diff) y detección de bucles.
- **Opcionales**: índice de símbolos propio (outline, ir a definición, completar), LSP (jdtls, kotlin-language-server, csharp-ls), ejecución aislada en Podman/Docker y depuración (DAP).

## Requisitos

- Go 1.26 o superior.
- Al menos una CLI de agente con su sesión iniciada: `claude` (Claude Code) o `agy` (Antigravity CLI).
- Opcional: Podman o Docker (ejecutar en sandbox), jdtls/kotlin-language-server/csharp-ls (LSP), netcoredbg (depurar C#), universal-ctags.

Linux y macOS. Windows compila, pero el control de procesos todavía es parcial.

## Uso

```sh
go build -o wari ./cmd/wari
./wari                 # abre el editor en el directorio actual
./wari src/Main.java   # abre un archivo
./wari -agent agy      # usa Antigravity en vez de Claude
./wari -accept-edits   # los agentes editan sin preguntar (con checkpoint)
./wari -prompt "..."   # sin TUI: un turno y vuelca la salida
```

`./wari -h` lista todas las opciones (presupuestos, límite de activos, LSP, índice).

## Atajos principales

Todos se pueden cambiar en `~/.config/wari/keys.json` o `<proyecto>/.wari/keys.json`. `F1` o `?` muestran la lista completa.

| Atajo | Acción |
|---|---|
| `ctrl+p` | Paleta de comandos |
| `alt+1` / `alt+2` / `alt+3` | Ir al árbol / editor / agentes |
| `ctrl+s` | Guardar |
| `ctrl+t` | Nuevo agente |
| `ctrl+o` / `ctrl+x` / `ctrl+k` | Pausar / detener agente / parar todos |
| `ctrl+r` | Ver el diff de la última edición de un agente |
| `alt+r` / `alt+t` / `alt+a` | Revertir archivo / turno / agente |
| `alt+i` | Importar el tipo bajo el cursor |
| `alt+f` | Nuevo archivo desde plantilla |
| `ctrl+c` | Salir |

## Documentación del proyecto

- `docs/PLAN.md`: plan por fases.
- `docs/decisions.md`: decisiones y sus motivos.
- `docs/progress.md`: estado actual.
- `AGENTS.md`: reglas para quien contribuya (personas o agentes).

## Licencia

AGPL-3.0. Ver `LICENSE`.
