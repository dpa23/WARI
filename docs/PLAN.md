# PLAN.md — Editor de código TUI multiagente en Go

> Documento dirigido a un agente de código (IA). Léelo completo antes de escribir código. Las reglas operativas resumidas están en `AGENTS.md`; las decisiones tomadas desde la versión original, en `docs/decisions.md`.

## 1. Objetivo

Construir un **editor de código en terminal (TUI), escrito en Go**, con **capacidad multiagente**: varios agentes de IA corriendo en paneles, cada uno con estado y control propio, que el usuario puede pausar, matar o aprobar en cualquier momento.

- Los agentes son las **CLIs oficiales** (`claude` y `agy` —Antigravity CLI— en el núcleo; `codex` y otras quedan para adaptadores de la comunidad) lanzadas como subprocesos en modo headless con salida JSON en streaming. **Nunca** una API directa ni extracción de tokens OAuth.
- Es un **editor primero**. No es un IDE completo ni un orquestador sin editor.
- Lenguajes objetivo inicial: **Java, Kotlin, C#**, con foco en **Spring Boot**.
- Debe ser **liviano** (pensado para máquinas de pocos recursos): sin LSP obligatorio, sin índice pesado en la base.
- Problema original a resolver: al pasar un goal de un agente a otro, el usuario pierde el control de cuándo pararlo. El harness es el proceso padre y debe decidir cuándo los agentes corren, pausan o mueren.

Nombre del proyecto: **WARI** (decidido por el usuario). Módulo Go `github.com/dpa23/wari`; binario `wari`; configuración en `~/.config/wari/` y `.wari/`.

## 2. Reglas de operación para el agente

1. **Una fase a la vez.** No empieces la fase N+1 hasta que la N cumpla todos sus criterios de aceptación y el usuario la haya visto.
2. **Pequeño y verificable.** Commits pequeños, un cambio lógico por commit. Después de cada tarea corre `go build ./...`, `go vet ./...` y `go test ./...`.
3. **Tests primero en lo difícil.** Buffer, undo/redo, expansión de snippets, parser de eventos de CLIs y supervisor de procesos llevan tests unitarios desde que se crean.
4. **No inventes flags de las CLIs.** Antes de escribir o cambiar un adaptador, ejecuta `<cli> --help` y la subayuda correspondiente, y usa solo lo que veas. Declara la versión mínima soportada en el adaptador.
5. **Dependencias mínimas.** Solo las listadas en la sección 4 salvo que el usuario apruebe otra. Registra cada dependencia nueva con una línea de justificación en `docs/decisions.md`.
6. **No avances sobre una decisión abierta.** Si la sección 10 dice que algo está abierto y tu tarea depende de ello, detente y pregunta con una pregunta concreta y opciones.
7. **Seguridad de procesos.** Todo proceso hijo se lanza en su propio grupo de procesos y debe poder terminarse junto con sus descendientes. Ninguna tarea se da por terminada si deja procesos huérfanos.
8. **Nada destructivo sin confirmar.** No borres archivos del usuario, no reescribas historial de git, no ejecutes migraciones ni comandos contra bases de datos reales.
9. **Registro de decisiones.** Mantén `docs/decisions.md` (fecha, decisión, motivo, alternativas descartadas) y `docs/progress.md` (fase actual, hecho, pendiente).
10. **Al terminar una fase**, entrega un resumen corto: qué quedó funcionando, cómo probarlo (comandos exactos) y qué quedó pendiente.

## 3. Prioridad por tier

| Tier | Contenido | Prioridad |
|---|---|---|
| **0** | Editor TUI, chat multiagente, control y parada de agentes, seguridad de cambios | Núcleo. Sin esto no hay producto |
| **1** | Snippets, plantillas de archivo, mapa de imports (JSON) | Alta |
| **2** | Resaltado preciso, índice de símbolos, LSP opcional | Media |
| **3** | Sandbox de ejecución, runner y debugger | Después. **Sin `git worktree`**: se aísla la ejecución, no la edición |

## 4. Stack y restricciones

- **Lenguaje:** Go (versión estable actual). Sin Rust ni FFI en este proyecto.
- **TUI:** `charmbracelet/bubbletea`, `bubbles`, `lipgloss`.
- **Procesos:** `os/exec`, `creack/pty` donde se necesite terminal virtual, grupos de procesos (`Setpgid` en Unix).
- **Archivos:** `fsnotify` para el watcher.
- **Resaltado (tier 0):** `alecthomas/chroma` (Go puro, sin CGO).
- **Tree-sitter:** solo en tier 2, aceptando CGO, y detrás de un build tag para no romper la compilación base.
- **Configuración:** archivos TOML o JSON en `~/.config/wari/` y `.wari/` por proyecto.
- **Sin base de datos.** Persistencia en archivos.
- **Sistema operativo objetivo:** Linux y macOS ahora; Windows después (donde los recursos son más escasos). El código de procesos vive en `internal/proc` detrás de una interfaz: implementación Unix (`//go:build unix`) y stub Windows que compila (Job Objects más adelante).

## 5. Estructura del repositorio

```
/cmd/wari           main, flags, arranque
/internal/editor    buffer, cursor, selección, undo/redo, búsqueda
/internal/tui       modelo Bubble Tea, layout, paneles, atajos, temas
/internal/proc      grupos de procesos por plataforma (lanzar, pausar, reanudar, matar sin huérfanos)
/internal/agent     interfaz Agent, adaptadores por CLI, eventos normalizados
/internal/orch      supervisor, cola, presupuestos, gates, pipeline de goals
/internal/workspace watcher, checkpoints, diffs, propiedad de archivos
/internal/snippets  cargador JSON, expansión, tabstops
/internal/templates plantillas de archivo, variables, menú "Nuevo"
/internal/imports   mapa tipo→import, inserción
/internal/lang      configuración por lenguaje (Java, Kotlin, C#)
/internal/index     tree-sitter/ctags (tier 2)
/internal/lsp       cliente LSP opcional (tier 2)
/internal/sandbox   runner aislado y DAP (tier 3)
/internal/store     persistencia de sesiones y configuración
/docs               decisions.md, progress.md
/testdata           fixtures (archivos .java/.kt/.cs, salidas de CLIs grabadas)
```

**Regla de acoplamiento:** `editor` y `agent` no se importan entre sí. Se comunican solo a través de `tui` y `orch`, mediante mensajes. Un adaptador de CLI nunca debe conocer el editor.

## 6. Contratos centrales

### 6.1 Interfaz de agente
```go
type Agent interface {
    Start(ctx context.Context, task Task) error
    Send(msg string) error
    Events() <-chan Event
    Pause() error
    Resume() error
    Kill() error
    Status() Status
}
```

Estados: `Idle`, `Running`, `Paused`, `WaitingApproval`, `Error`, `Done`.

### 6.2 Eventos normalizados
`TextDelta`, `ToolCall`, `FileEditProposed`, `Usage`, `NeedsApproval`, `Done`, `Error`. Cada adaptador traduce el formato propio de su CLI a estos. El resto del sistema solo conoce estos eventos.

### 6.3 Pruebas de adaptadores
Graba salidas reales de cada CLI en `testdata/<cli>/*.jsonl` y prueba el parser contra ellas. Si el formato de una CLI cambia, la prueba debe fallar con un mensaje claro.

## 7. TIER 0 — Fases

### Fase 1 — Proceso y streaming
- Lanzar una CLI (la primera: `claude`) en modo headless con un prompt fijo, leer su salida línea a línea y volcarla a stdout.
- Lanzar en grupo de procesos propio; implementar `Kill` que termina al grupo completo.
- **Aceptación:** lanzar, ver streaming, matar a mitad de respuesta y comprobar con `ps` que no queda ningún proceso hijo.
- **Tests:** parser con salida grabada; kill sin huérfanos.

### Fase 2 — TUI mínimo con un agente
- Bubble Tea con un panel de chat: entrada de texto, historial, respuesta en streaming, indicador de estado.
- Conectar con el adaptador de la fase 1 mediante `Event`.
- **Aceptación:** conversar con el agente en el TUI, cancelar con un atajo y reanudar la sesión usando el ID que devuelve la CLI.

### Fase 3 — Editor núcleo
- Buffer como slice de líneas (`[][]rune`). No implementes rope ni gap buffer salvo que midas lentitud real.
- Operaciones: insertar, borrar, mover cursor (carácter, palabra, línea, inicio/fin), selección, copiar/pegar, indentación automática.
- Undo/redo con pila de operaciones inversas, agrupadas por pausa de tecleo.
- Abrir/guardar con UTF-8 y preservación del estilo de salto de línea; guardado atómico (archivo temporal y renombrar).
- Búsqueda y reemplazo en archivo con regex de Go.
- **Aceptación:** abrir un `.java` de 5.000 líneas sin lag perceptible, editar, deshacer 100 pasos y guardar sin corromper (verificar con diff contra el original).
- **Punto de decisión:** al cerrar esta fase, informa al usuario cómo se siente el editor y pregunta si se continúa con editor propio o se embebe uno existente (sección 10).

### Fase 4 — Layout editor + chat
- Layout: árbol de archivos (izquierda), editor (centro), panel de agentes (derecha o abajo). Colapsable y configurable.
- Resaltado con `chroma` para Java, Kotlin y C#.
- Modo foco entre editor, chat y paneles; paleta de comandos (Ctrl+P).
- Atajos definidos en archivo de configuración desde el inicio, no fijos en el código.
- **Aceptación:** redimensionar la terminal sin romper el layout; todo operable con teclado.

### Fase 5 — Multiagente, cola y presupuestos
- Varios agentes simultáneos, cada uno en su panel, con estado, turnos y tokens visibles.
- **Límite de agentes activos configurable (por defecto 2)** y cola para el resto; cada CLI puede pesar 100-300 MB.
- Presupuestos por agente y por tarea: máximo de turnos, tiempo y tokens (desde eventos `Usage` cuando la CLI los reporte).
- Pausar (`SIGSTOP` al grupo) y reanudar (`SIGCONT`); botón de parada global y por agente, siempre visible.
- **Aceptación:** correr 3 agentes con límite de 2 (el tercero espera en cola), pausar uno, matar otro, y comprobar que los presupuestos detienen una tarea que se pasa.
- **Hito:** al terminar esta fase el problema original ya está resuelto.

### Fase 6 — Seguridad de cambios (sin worktree, sin sandbox)
Como los agentes editan el árbol real, compensa así:
- **Checkpoints automáticos:** antes de cada turno, copia de los archivos que el agente va a tocar en `.wari/checkpoints/<agente>/<turno>/`. Son copias simples, no git.
- **Watcher** (`fsnotify`): detecta qué archivo cambió, atribuye el cambio al agente activo y muestra el diff contra el checkpoint.
- **Revertir** por archivo, por turno o por agente.
- **Propiedad de archivos:** un agente reclama archivos o carpetas; si otro intenta editar uno ya reclamado, se bloquea o se avisa. Es el punto más delicado del multiagente.
- Arrancar las CLIs en modo "preguntar antes de editar" cuando exista (verifica el flag con `--help`).
- **Aceptación:** dos agentes editando archivos distintos en paralelo funcionan; si chocan en el mismo archivo, el sistema avisa antes de perder trabajo; revertir un turno deja el archivo idéntico al checkpoint (comparar hash).

### Fase 7 — Gates y pipeline de goals
- **Pipeline:** lista ordenada de pasos (agente, prompt, condición de paso). Cada paso puede ser manual, semiautomático o automático con presupuesto.
- **Gate de aprobación:** el paso de un goal entre agentes queda en `WaitingApproval` hasta que el usuario confirme, mostrando resumen y diff.
- **Detección de bucles:** misma herramienta con los mismos argumentos N veces seguidas, o N turnos sin cambios de archivo, produce pausa automática y notificación.
- **Aceptación:** un pipeline de 3 pasos que se detiene solo en el gate, y un agente en bucle (simulado con una CLI falsa de pruebas) que se pausa solo.

### Fase 8 — Más adaptadores
- Segundo adaptador: `agy` (Antigravity CLI). Ojo: en modo print sus permisos se auto-deniegan; requiere reglas `permissions.allow`. Otros adaptadores (`codex`, `gemini`) quedan para la comunidad. Cada uno en su carpeta, cumpliendo la interfaz de 6.1 sin modificar `orch` ni `tui`.
- Si añadir un adaptador obliga a tocar código fuera de `internal/agent/<cli>`, la abstracción está mal: corrígela.
- **Aceptación:** un pipeline que combine dos CLIs distintas.

**Fin del tier 0.**

## 8. TIER 1 — Snippets, plantillas e imports

### Fase 9 — Snippets y plantillas
- **Formato de snippets:** JSON por lenguaje, estilo VS Code (`prefix`, `body`, `description`, tabstops `$1`, `${1:default}`, `$0`).
  ```json
  { "ctor": { "prefix": "ctor", "body": ["public ${1:Class}(${2}) {", "\t$0", "}"], "description": "Constructor" } }
  ```
- **Motor de expansión:** parseo de tabstops y placeholders, navegación con Tab/Shift+Tab, variables (`$TM_FILENAME_BASE`, `$CLASS_NAME`).
- **Paquetes iniciales:** Java (`main`, `sout`, `for`, `foreach`, `ctor`, `getset`), Kotlin (`fun`, `data class`, `when`), C# (`prop`, `ctor`, `cw`, `foreach`).
- **Plantillas de archivo:** menú "Nuevo → clase, interfaz, enum, record, controller, service, repository, entity, test". Archivos de texto con cabecera YAML (nombre, lenguaje, variables). Variables `{{package}}` (derivada de la ruta bajo `src/main/java`), `{{ClassName}}`, `{{namespace}}` (carpeta y `.csproj` en C#). En .NET, delegar a `dotnet new` cuando esté instalado.
- **Aceptación:** crear un controller en `com/empresa/api/` y que el `package` salga correcto sin escribirlo; expandir `ctor` y recorrer tabstops con Tab.

### Fase 10 — Imports y Spring Boot
- **Mapa de imports:** JSON `tipo → import` (`"List": "java.util.List"`, `"LocalDate": "java.time.LocalDate"`) con equivalentes en C# (`using`) y Kotlin.
- **Inserción** en la posición correcta: ordenada, sin duplicar. Si hay varios candidatos (`List` de AWT vs `java.util`), mostrar un selector.
- **Cobertura inicial:** biblioteca estándar y los paquetes de Spring más usados.
- **Plantillas y snippets de Spring Boot:** `@RestController`, `@Service`, `@Repository`, `@Entity`, `@Configuration`, `@GetMapping`, `@PostMapping`, `application.properties`.
- **Aceptación:** escribir `List`, aceptar la sugerencia y que aparezca `import java.util.List;` una sola vez en el sitio correcto.

## 9. TIER 2 y TIER 3 — Fases

### Tier 2 (opcional y liviano)

**Fase 11 — Estructura e índice**
- Tree-sitter con build tag (CGO) para resaltado preciso y outline de clases, métodos y anotaciones.
- Índice de símbolos del proyecto con tree-sitter propio o `universal-ctags`; ir a definición aproximado; autocompletado de nombres del proyecto.
- El mapa de imports (fase 10) puede ampliarse con los tipos del proyecto.

**Fase 12 — LSP opcional**
- Cliente mínimo: `initialize`, `didOpen`/`didChange`, `completion`, `definition`, `diagnostics`.
- Servidores: `jdtls`, `kotlin-language-server`, `csharp-ls`. Se levantan solo bajo demanda, mostrando el consumo de RAM.
- **Aceptación:** con LSP apagado el editor sigue siendo plenamente usable; con LSP encendido el completado sugiere métodos del objeto.

### Tier 3 — Sandbox de ejecución, runner y debugger
**No uses `git worktree`.** Lo que se aísla es la ejecución, no la edición: el código fuente es el real, pero al correr o depurar, el proceso vive en un entorno cuyos cambios no tocan el proyecto oficial.

**Fase 13 — Sandbox y runner**
- Aislamiento: la tecnología depende del SO objetivo (sección 10). Opciones: contenedor Docker/Podman con el proyecto en solo lectura y capa de escritura efímera; `bubblewrap`/namespaces en Linux; WSL2 o Windows Sandbox en Windows. Detrás de una interfaz `Sandbox` con una implementación por opción.
- Salidas de build redirigidas fuera del proyecto oficial (`target/`, `bin/`, `obj/`). Promover un resultado al oficial es una acción explícita del usuario.
- Perfiles de ejecución por lenguaje (`mvn spring-boot:run`, `gradle bootRun`, `dotnet run`), logs en streaming, límites de CPU y memoria, botón de parada.
- Red y datos: sin acceso a producción por defecto.
- **Aceptación:** correr un Spring Boot de prueba, hacer que cree o modifique archivos y datos, y comprobar con hash o diff que el proyecto oficial queda intacto.

**Fase 14 — Debugger (DAP)**
- Cliente mínimo: breakpoints, step, variables, stack. Servidores: `java-debug`, `netcoredbg`.
- Panel de variables navegable por teclado.

## 10. Decisiones abiertas (detente y pregunta)

1. ~~Sistema operativo objetivo~~ — **resuelto**: Linux y macOS ahora, Windows después (ver `docs/decisions.md`).
2. ~~Nombre final~~ — **resuelto**: WARI.
3. ~~CLI del primer adaptador~~ — **resuelto**: `claude`; el segundo es `agy`.
4. ~~Editor propio o embebido~~ — **resuelto**: editor propio (ver `docs/decisions.md`).
5. **Aislamiento del sandbox** (Docker, bubblewrap, otro): se decide antes de la fase 13.

## 11. Fuera de alcance

- Diseñadores visuales (WinForms, XAML, Scene Builder). No caben en un TUI.
- Refactors profundos estilo IntelliJ/Visual Studio (cambiar firma, mover entre módulos).
- Extraer tokens OAuth de suscripciones o llamar APIs no oficiales.
- Soporte multilenguaje general más allá de Java, Kotlin y C# en el primer ciclo.
- Híbrido Go + Rust. Si más adelante el indexador lo exige, sería un binario aparte lanzado como subproceso, y requiere aprobación del usuario.

## 12. Riesgos y mitigaciones

| Riesgo | Mitigación |
|---|---|
| Choques entre agentes sin worktree | Propiedad de archivos y checkpoints (fase 6) |
| Cambios en flags o formato de las CLIs | Adaptadores aislados, versión mínima declarada, tests con salidas grabadas |
| Términos de uso de los proveedores | Solo CLIs oficiales; el usuario debe verificar que se permite uso automatizado con su suscripción |
| Consumo de RAM con varias CLIs | Límite de activos y cola obligatorios (fase 5) |
| Editor propio más costoso de lo previsto | Punto de decisión al cerrar la fase 3 |
| Diferencias Windows/Linux | Interfaces por plataforma desde la fase 1 |

## 13. Definición de "hecho" por fase

Una fase está hecha solo si: compila (`go build ./...`), pasa `go vet` y `go test ./...`, cumple todos sus criterios de aceptación con evidencia (comandos y salida), no deja procesos huérfanos, `docs/progress.md` está actualizado y el usuario recibió el resumen de la regla 10.
