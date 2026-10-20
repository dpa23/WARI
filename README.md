# WARI

Editor de código para terminal (TUI) escrito en Go, con soporte integrado para ejecutar y supervisar múltiples agentes de inteligencia artificial en paralelo.

---

## 1. Descripción

WARI es un editor de texto en terminal diseñado para desarrolladores que trabajan en entornos locales o remotos, especialmente en equipos con recursos de cómputo limitados (pocos núcleos de CPU y memoria RAM reducida).

### El problema que resuelve

Al utilizar herramientas de asistencia basadas en agentes de IA desde la línea de comandos o pasar objetivos entre diferentes modelos, el desarrollador pierde habitualmente el control operativo:
- No es posible pausar un agente mientras procesa información.
- No hay límites estrictos sobre la cantidad de turnos, consumo de tokens o tiempo de ejecución.
- Si dos agentes intentan modificar los mismos archivos en paralelo, se generan colisiones y sobreescrituras destructivas.
- La cancelación de un proceso de IA frecuentemente deja procesos hijos huérfanos consumiendo recursos en segundo plano.

En WARI, el editor actúa como proceso padre y supervisor estricto de todos los subprocesos:
- Controla el ciclo de vida de los agentes (arranque, encolado, pausa cooperativa, cancelación y terminación forzada).
- Asigna cuotas de turnos, tokens y tiempo.
- Aplica mecanismos de seguridad automáticos mediante puntos de control (*checkpoints*) antes de cada edición en disco.
- Permite inspeccionar diffs unificados y revertir cambios por archivo, turno o agente completo.

### Interfaz de usuario (TUI)

La interfaz se divide en tres paneles principales:

```
 Archivos             │ Main.java                        │ Agentes
▾ proyecto/           │   1  package com.ejemplo;        │▸ agente 1  RUN   t3  US$0.02
  ▾ src/              │   2                              │  agente 2  PAU   t5  US$0.05
    Main.java ●1      │   3  public class Main {         │  agente 3  Q     t0
    Util.java         │   4      public static void ...  │ ───────────────────────────
                      │   5  }                           │ ▎ Revisa Main.java
                      │                                  │ Encontré 2 problemas...
                      │                                  │ ✎ src/Main.java
 activos 2/2 · en cola 1 · ctrl+k parar todo · F1 ayuda
```


1. **Panel izquierdo (Árbol de archivos):** Explorador de archivos del proyecto. Muestra indicadores junto a los nombres de archivo (`●` con el número del agente) cuando un agente tiene reclamado o editado un archivo, así como advertencias (`⚠`) en caso de conflicto de edición.
2. **Panel central (Editor de código / Vista de Diff):** Editor de texto con resaltado de sintaxis, números de línea, soporte de snippets con tabstops, resolución de imports y plantillas. Cuando se inspecciona una edición de un agente (`ctrl+r`), este panel conmuta a una vista de diff unificado lado a lado o unificado con saltos entre hunks (`n`/`p`) y diálogo modal de reversión.
3. **Panel derecho (Agentes):** Lista superior con todos los agentes configurados, sus badges de estado compactos (`RUN`, `PAU`, `Q`, `IDLE`, `STOP`, `ERR`, `$!`), indicadores de presupuesto y alertas de colisión. Debajo, el historial de conversación y herramientas del agente seleccionado, y la entrada de texto para enviar instrucciones o interactuar con las aprobaciones de pipelines (*gates*).

---

## 2. CLIs de agentes soportadas

WARI no implementa llamadas directas a las APIs de los proveedores ni requiere claves de API (API keys) en su configuración. Tampoco gestiona autenticación OAuth ni contraseñas. Cada adaptador ejecuta la CLI oficial correspondiente en modo desatendido (*headless*) con salida estructurada en streaming (JSON / stream-json), utilizando la sesión y credenciales que el usuario ya tenga activadas en su entorno.

| CLI | Identificador en WARI | Autenticación previa en la terminal |
|---|---|---|
| **Claude Code** | `claude` | Solicita inicio de sesión interactivo la primera vez que se ejecuta `claude` en la terminal. |
| **Antigravity CLI** | `agy` | Se autentica mediante la cuenta de Google al ejecutar `agy` por primera vez. |
| **OpenAI Codex CLI** | `codex` | Se autentica ejecutando `codex login`. |
| **OpenCode** | `opencode` | Se autentica ejecutando `opencode auth login`. |

Para instrucciones específicas de instalación de cada CLI, consulte la documentación oficial del proveedor correspondiente.

### Tabla de seguridad y permisos por CLI

| CLI | Comportamiento por omisión (modo seguro, sin `-accept-edits`) | Modo con `-accept-edits` | Checkpoints previos y detección de choques |
|---|---|---|---|
| `claude` | `--permission-mode manual` con `--permission-prompts none`. Las peticiones de edición o ejecución se deniegan automáticamente. Solo lectura. | `--permission-mode acceptEdits`. Edita archivos directamente. | **Sí**. El evento de propuesta de edición llega antes de que la herramienta toque el disco; WARI toma el checkpoint previo y detecta choques. |
| `agy` | `--mode=plan` como mejor esfuerzo. **No dispone de modo de solo lectura estricto**: si el modelo invoca herramientas de escritura, escribe. La protección real la proporcionan los checkpoints de WARI. | `--mode=accept-edits`. Edita archivos directamente. | **Sí**. El evento de herramienta llega antes de ejecutarse en disco; WARI toma el checkpoint previo. |
| `codex` | Aislamiento por sandbox en modo solo lectura: `-s read-only`. | `-s workspace-write`. Permite modificaciones dentro del espacio de trabajo. | **No previo**. El evento de cambio (`file_change`) solo se emite como completado (`item.completed`) tras modificar el disco. No hay checkpoint previo ni prevención de choque antes de la edición (se detecta a posteriori con el watcher). |
| `opencode` | Agente de planeación: `--agent plan`. En nuestras pruebas no creó archivos en el proyecto (sin `--agent plan`, opencode escribe aunque no se le pida permiso). | Sin `--agent`. Permite escrituras directas en el proyecto. | **No previo**. El evento `tool_use` llega únicamente cuando la herramienta ya finalizó (`state.status == "completed"`). No hay checkpoint previo ni prevención de choque antes de la edición (se detecta a posteriori con el watcher). |

---

## 3. Requisitos previos

### Requisitos obligatorios
- **Go 1.26 o superior** (necesario para compilar el proyecto; Bubble Tea v2 requiere Go 1.26).
- **Git** (para clonar el código y soporte de control de versiones).
- **Al menos una CLI de agente compatible instalada y autenticada** (`claude`, `agy`, `codex` u `opencode`).

### Herramientas opcionales
- **Podman o Docker**: Requerido para aislar la ejecución del proyecto y pruebas en contenedores mediante `run_project` (`alt+g`).
- **Servidores LSP** (para autocompletado avanzado, ir a definición y diagnósticos con `alt+l`):
  - `jdtls` (Java)
  - `kotlin-language-server` (Kotlin)
  - `csharp-ls` (C#)

  Compatibilidad medida en Linux (redactada por Antigravity a partir de las pruebas reales):
  - **jdtls** (build `latest` de snapshots): arranca con JDK 25 y falla con JDK 21. Lee `JAVA_HOME`: expórtelo apuntando a Java 25 al lanzarlo.
  - **kotlin-language-server 1.3.x** (fwcd): funciona con JDK 21 y falla con JDK 25.0.4.1 al leer la versión de Java. Fije `JAVA_HOME` a JDK 21 para este servidor.
  - **csharp-ls 0.28.0** (`dotnet tool install -g csharp-ls`): requiere el SDK de .NET 10; con solo el SDK 8 la instalación falla.
  - **Verificación:** la prueba `TestRealServers` ejercita los tres (initialize, completar, definición); se omite con `-short` o si el servidor no está en el PATH.
- **Depurador DAP**:
  - `netcoredbg` (depuración de C#)
  - `java-debug` (depuración de Java a través de JDTLS)
- **universal-ctags**: Indexación opcional de símbolos en proyectos grandes si se activa la opción `-ctags`.

---

## 4. Instalación por sistema operativo

Clone el repositorio oficial:

```sh
git clone https://github.com/dpa23/WARI.git
cd WARI
```

O vía SSH:

```sh
git clone git@github.com:dpa23/WARI.git
cd WARI
```

### 4.1. Linux

#### Compilación local
```sh
go build -o wari ./cmd/wari
```

#### Instalación con `go install`
```sh
go install ./cmd/wari
```
El binario compilado quedará ubicado en `$(go env GOPATH)/bin` (por defecto `~/go/bin`).

#### Incorporación al PATH
Agregue el directorio de binarios de Go a su intérprete de comandos:

- **Bash:** Añada la siguiente línea a `~/.bashrc`:
  ```sh
  export PATH="$PATH:$(go env GOPATH)/bin"
  ```
- **Zsh:** Añada la siguiente línea a `~/.zshrc`:
  ```sh
  export PATH="$PATH:$(go env GOPATH)/bin"
  ```
- **Fish:** Ejecute:
  ```fish
  fish_add_path (go env GOPATH)/bin
  ```

Lo más corto es el script de instalación, que deja el comando `wari` en `~/.local/bin` (o en `$GOBIN`, o en el directorio que le pase) sin ningún `./wari` en el repositorio:
```sh
scripts/install.sh
wari            # desde cualquier carpeta
```

### 4.2. macOS

#### Compilación local
```sh
go build -o wari ./cmd/wari
```

#### Instalación con `go install`
```sh
go install ./cmd/wari
```

#### Incorporación al PATH
- **Zsh (shell predeterminado):** Añada a `~/.zshrc`:
  ```sh
  export PATH="$PATH:$(go env GOPATH)/bin"
  ```
- **Bash:** Añada a `~/.bash_profile` o `~/.bashrc`:
  ```sh
  export PATH="$PATH:$(go env GOPATH)/bin"
  ```
- **Fish:** Ejecute:
  ```fish
  fish_add_path (go env GOPATH)/bin
  ```

#### Configuración de la tecla Alt / Option en macOS
En macOS no hace falta configurar nada: la tecla líder (`ctrl+e`) seguida de la letra reemplaza a `alt+` (ver «La tecla Alt no responde en macOS» más abajo). Lo que sigue es opcional, solo si prefiere `alt+` directo. Por defecto, en macOS la tecla Option produce caracteres tipográficos especiales en lugar de secuencias de escape de terminal (`Meta`). Para utilizar los atajos con `Alt` (`Alt+1`, `Alt+2`, `Alt+3`, `Alt+f`, etc.), debe habilitar el modo Meta en su emulador de terminal:
- **Terminal.app:** Vaya a *Ajustes* > *Perfiles* > pestaña *Teclado* > active la casilla **"Usar opción como tecla meta"** (*Use Option as Meta key*).
- **iTerm2:** Vaya a *Preferences* / *Settings* > *Profiles* > pestaña *Keys* > en **"Left Option key"**, seleccione la opción **`Esc+`**.

### 4.3. Windows

#### Compilación local
```powershell
go build -o wari.exe ./cmd/wari
```

#### Instalación con `go install`
```powershell
go install ./cmd/wari
```

#### Incorporación al PATH en Windows
- **PowerShell (para el usuario actual):**
  ```powershell
  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ";$((go env GOPATH))\bin", 'User')
  ```
- **Interfaz gráfica:** Abra el menú Inicio > escriba *Variables de entorno* > seleccione *Editar las variables de entorno de esta cuenta* > seleccione la variable `Path` > *Editar* > *Nuevo* > agregue `%USERPROFILE%\go\bin`.

#### Limitación en Windows nativo
WARI compila y ejecuta su editor TUI en Windows de manera nativa. Sin embargo, **el control de subprocesos es parcial**:
- La plataforma Windows carece de señales POSIX equivalentes a `SIGSTOP`/`SIGCONT` y grupos de procesos Unix estándar.
- WARI no puede pausar agentes en Windows (`errors.ErrUnsupported`).
- Al terminar o cancelar un turno, WARI solo puede finalizar el proceso principal inmediato, sin garantizar la recolección automática de subprocesos descendientes lanzados por las CLIs (la implementación avanzada con *Job Objects* y llamadas de sistema no documentadas está en desarrollo).

**Recomendación:** Para flujos de trabajo multiagente serios en entornos Windows, se aconseja ejecutar WARI bajo **WSL2** (Windows Subsystem for Linux), donde dispone del soporte Unix completo.

### 4.4. Verificación y compilación cruzada

Verifique que el binario sea accesible y responda correctamente:
```sh
wari -version
```

Para realizar compilación cruzada hacia otras plataformas desde cualquier máquina:
```sh
# Linux amd64
GOOS=linux GOARCH=amd64 go build -o wari-linux ./cmd/wari

# macOS Apple Silicon (M1/M2/M3/M4)
GOOS=darwin GOARCH=arm64 go build -o wari-darwin-arm64 ./cmd/wari

# Windows amd64
GOOS=windows GOARCH=amd64 go build -o wari.exe ./cmd/wari
```

### Terminales recomendadas
- **Linux:** Kitty, Alacritty, GNOME Terminal, Foot, WezTerm.
- **macOS:** iTerm2, Terminal.app, Alacritty, Kitty.
- **Windows:** Windows Terminal (con soporte de colores verdaderos y secuencia VT).

---

## 5. Tutorial de primer uso paso a paso

1. **Abrir el espacio de trabajo:**
   Inicie WARI apuntando a un directorio de código o proyecto de prueba:
   ```sh
   wari ./mi-proyecto
   ```
   *Qué se observa:* Se abre la terminal en modo alternativo con tres paneles: el árbol de archivos a la izquierda, el editor en el centro y la columna de agentes a la derecha con un `agente 1` en estado inicial.

2. **Navegar entre paneles:**
   - Presione `alt+1` para poner el foco en el árbol de archivos.
   - Presione `alt+2` para poner el foco en el editor de código.
   - Presione `alt+3` para poner el foco en la entrada de comandos del agente.
   - Con el foco fuera del editor, `Tab` y `Shift+Tab` también cambian de panel; dentro del editor, `Tab` inserta espacios (o expande un snippet), por eso existen `alt+1`, `alt+2` y `alt+3`.

3. **Abrir un archivo con la paleta difusa:**
   Presione `ctrl+p`. Escriba parte del nombre de un archivo (por ejemplo `Main.java`) y presione `Enter`. El archivo se cargará en el editor central con numeración de líneas.

4. **Enviar una consulta a un agente:**
   Presione `alt+3` para situarse en la caja de texto inferior. Escriba `Explica brevemente qué hace este proyecto` y presione `Enter`.
   *Qué se observa:* El agente pasa a estado `RUN` en la lista, se activa el spinner de actividad y el texto de la respuesta se despliega en el historial.

5. **Cancelar un turno en curso:**
   Mientras el agente esté respondiendo, presione `Esc`.
   *Qué se observa:* El proceso del turno se interrumpe de inmediato sin dejar subprocesos en ejecución y el agente vuelve al estado `IDLE`.

6. **Crear agentes adicionales y verificar la cola:**
   Presione `ctrl+t` para crear un segundo agente (`agente 2`). Si envía tareas simultáneas superando el límite de agentes activos (`-max-active 2` por omisión), el agente excedente mostrará el badge `Q` (encolado) y arrancará automáticamente cuando el anterior finalice su turno.

7. **Pausar y detener agentes:**
   - Presione `ctrl+o` para pausar el turno del agente seleccionado (`PAU`). Presione `ctrl+o` nuevamente para reanudar.
   - Presione `ctrl+x` para detener el turno del agente seleccionado.
   - Presione `ctrl+k` para detener la totalidad de los agentes en ejecución.

8. **Permitir edición de código con `-accept-edits`:**
   Salga con `ctrl+c` e inicie WARI con permisos de edición:
   ```sh
   wari -accept-edits ./mi-proyecto
   ```
   Envíe una instrucción que requiera modificar código, por ejemplo: `Añade un método helper de validación en Main.java`.
   *Qué se observa:* El agente edita el archivo y en su historial aparece la línea `✎ ruta`. Con `claude` y `agy`, WARI guardó antes una copia en `.wari/checkpoints`; con `codex` y `opencode` el aviso llega cuando la edición ya ocurrió y no hay copia previa (en ese caso, prueba el paso 9 con `claude` o `agy`).

9. **Inspeccionar diffs y revertir cambios:**
   - Presione `ctrl+r` para abrir la vista central de diff. El editor es reemplazado por la comparación unificada contra el checkpoint previo al turno.
   - Presione `n` o `p` para desplazarse entre bloques de cambios (*hunks*).
   - Presione `alt+t` para revertir todas las modificaciones de ese turno (o `alt+r` para revertir solo el archivo actual).
   - *Qué se observa:* Se despliega un modal de confirmación no volátil indicando la lista de archivos que serán restaurados. Presione `Enter` para confirmar la reversión o `Esc` para cancelarla.

10. **Expandir snippets con `Tab`:**
    En el editor (`alt+2`), abra un archivo `.java`, ubique el cursor en una línea vacía dentro de una clase, escriba `ctor` y presione `Tab`.
    *Qué se observa:* La palabra clave se expande automáticamente al constructor de la clase, infiriendo el nombre de la clase a partir del contexto del archivo.

11. **Resolver e importar tipos con `alt+i`:**
    Escriba un tipo estándar sin importar (por ejemplo `List` o `ResponseEntity`) y, con el cursor sobre la palabra, presione `alt+i`.
    *Qué se observa:* WARI consulta el mapa de imports de su biblioteca interna e inserta la línea `import` correspondiente en la cabecera del archivo de forma ordenada.

12. **Crear archivos desde plantilla con `alt+f`:**
    Presione `alt+f`. Seleccione una plantilla predefinida (ej. *Controller*, *Service*, *Repository* o *Record*). Introduzca la ruta de destino (ej. `src/com/ejemplo/UsuarioController.java`). WARI creará el archivo deduciendo automáticamente el paquete y nombre de clase correspondientes.

13. **Consultar el panel de ayuda:**
    Presione `F1` (o `?`) en cualquier momento para desplegar la ayuda interactiva en pantalla con todos los comandos y atajos asignados.

---

## 6. Opciones de línea de comandos

Sintaxis general:
```sh
wari [opciones] [ruta-al-directorio-o-archivo]
```

Si el argumento posicional es un archivo, WARI toma su directorio contenedor como raíz del proyecto y abre dicho archivo directamente en el editor central.

| Opción | Tipo / Valor predeterminado | Descripción |
|---|---|---|
| `-agent` | `string` (`"claude"`) | CLI que se usará para el chat interactivo y el modo `-prompt`. Opciones válidas: `claude`, `agy`, `codex`, `opencode`. Los pasos de `.wari/pipeline.json` definen su propio agente con el campo `"agent"`. |
| `-model` | `string` (`""`) | Modelo específico a solicitar a la CLI (si se deja vacío, la CLI utiliza su modelo predeterminado). |
| `-prompt` | `string` (`""`) | Ejecución en modo desatendido (sin interfaz TUI): lanza un turno con el agente seleccionado, envía este texto, vuelca los eventos por `stderr`, la respuesta final por `stdout` y sale del programa. |
| `-kill-after` | `duration` (`0`) | Utilizado únicamente junto con `-prompt`: termina el turno y todos sus subprocesos si se supera esta duración de tiempo (ej. `-kill-after 30s`). |
| `-max-active` | `int` (`2`) | Número máximo de agentes que pueden estar ejecutándose activamente en paralelo. Los agentes adicionales esperan en cola. Debe ser al menos 1. |
| `-max-turns` | `int` (`0`) | Límite máximo de turnos permitidos por agente (`0`: sin límite). Si se excede, el agente pasa a estado `$!` (*BudgetExceeded*). |
| `-max-tokens` | `int64` (`0`) | Límite máximo acumulado de tokens (entrada + salida) por agente (`0`: sin límite). |
| `-max-time` | `duration` (`0`) | Límite máximo acumulado de tiempo activo en ejecución por agente (`0`: sin límite). |
| `-accept-edits` | `bool` (`false`) | Habilita que los agentes apliquen cambios y creen archivos directamente en disco (`PermissionAcceptEdits`). Sin esta opción, `claude`, `codex` y `opencode` no editan; `agy` no tiene un modo de solo lectura (ver la tabla de la sección 2). Con `claude` y `agy` cada edición queda respaldada en `.wari/checkpoints` antes de ejecutarse; con `codex` y `opencode` no hay respaldo previo. |
| `-loop-calls` | `int` (`0`) | Cantidad de llamadas consecutivas a la misma herramienta con los mismos argumentos que disparan la detección de bucle infinito y pausan al agente (`0`: valor por omisión de 3 llamadas; valores negativos desactivan la detección). |
| `-loop-turns` | `int` (`0`) | Cantidad de turnos consecutivos sin cambios detectados en archivos que disparan la detección de bucle (aplica solo con `-accept-edits`; `0`: valor por omisión de 3 turnos; valores negativos desactivan la detección). |
| `-no-index` | `bool` (`false`) | Desactiva el motor interno de indexación de símbolos en segundo plano (árbol de símbolos, ir a definición, autocompletado y tipos de imports). |
| `-ctags` | `bool` (`false`) | Utiliza la herramienta externa `universal-ctags` (si se encuentra disponible en el PATH) para generar el índice de símbolos, en sustitución del analizador léxico nativo de WARI. |
| `-lsp` | `bool` (`false`) | Enciende los servidores de lenguaje de fondo (`jdtls`, `kotlin-language-server`, `csharp-ls` si están en PATH) para autocompletado, definiciones y diagnósticos. Por omisión está apagado y puede conmutarse dentro del editor con `alt+l`. |
| `-icons` | `string` (`auto`) | Iconos junto a cada archivo y carpeta del árbol: `auto` (Nerd Font si la terminal la trae de serie —kitty, WezTerm, Ghostty— y si no emoji, que no necesitan fuente), `nerd` (requiere una Nerd Font; sin ella se ven cuadros), `emoji` u `off`. También `WARI_ICONS` y `WARI_NERD_FONT=1`. |
| `-version` | `bool` (`false`) | Imprime la versión compilada de WARI y sale. |

---

## 7. Configuración

### 7.1. Atajos de teclado (`keys.json`)

Los atajos de teclado pueden redefinirse mediante archivos JSON. WARI busca la configuración en dos niveles y aplica las reglas en cascada:
1. Configuración predeterminada interna de WARI.
2. Archivo de configuración global del usuario en `<config-usuario>/wari/keys.json`:
   - **Linux:** `~/.config/wari/keys.json` (o `$XDG_CONFIG_HOME/wari/keys.json`)
   - **macOS:** `~/Library/Application Support/wari/keys.json`
   - **Windows:** `%AppData%\wari\keys.json`
3. Archivo de configuración por proyecto en `<raiz-proyecto>/.wari/keys.json`.

Ejemplo de `keys.json`:
```json
{
  "focus_tree": ["alt+1"],
  "focus_editor": ["alt+2"],
  "focus_agents": ["alt+3"],
  "palette": ["ctrl+p"],
  "save": ["ctrl+s"],
  "diff_file": ["ctrl+r"],
  "revert_file": ["alt+r"],
  "revert_turn": ["alt+t"],
  "agent_pause": ["ctrl+o"],
  "agent_stop": ["ctrl+x"]
}
```

### 7.2. Pipelines de objetivos (`.wari/pipeline.json`)

WARI permite encadenar objetivos entre múltiples agentes mediante el archivo de proyecto `.wari/pipeline.json`. Cada paso define qué agente se encarga de la tarea, su prompt, modo de ejecución y presupuesto.

Los modos de paso disponibles son:
- `"manual"`: Solo arranca cuando el usuario lo ejecuta explícitamente (`alt+p`).
- `"semi"`: Arranca automáticamente al llegar su turno, pero al terminar se detiene en un *gate* de aprobación antes de continuar con el siguiente paso.
- `"auto"`: Arranca y avanza automáticamente al paso siguiente sin pedir confirmación. Exige que el paso defina su propio presupuesto (`budget`) y use un agente nuevo.

El prompt de un paso puede incluir la macro `{{prev}}`, que se reemplaza por el texto final producido por el paso anterior.

El campo opcional `"require"` es una condición de paso: el pipeline solo continúa si el texto final del paso contiene esa palabra (y el paso no terminó en error). Por eso el prompt del ejemplo le pide al agente que termine con `LISTO`.

Ejemplo de `.wari/pipeline.json` encadenando un paso de planificación con `claude` y un paso de implementación con `codex`:

```json
{
  "name": "modulo-autenticacion",
  "steps": [
    {
      "name": "plan-arquitectura",
      "agent": "claude",
      "prompt": "Analiza los requerimientos de autenticacion JWT y elabora un plan paso a paso indicando clases e interfaces a crear. Termina tu respuesta con la palabra LISTO.",
      "mode": "semi",
      "require": "LISTO",
      "budget": {
        "max_turns": 4,
        "max_tokens": 50000,
        "max_seconds": 180
      }
    },
    {
      "name": "implementacion-codigo",
      "agent": "codex",
      "prompt": "Implementa los componentes definidos en el siguiente plan:\n\n{{prev}}",
      "mode": "manual"
    }
  ]
}
```

Durante un *gate* de aprobación (modo `semi` o `manual`), el usuario puede:
- Aprobar el avance (`alt+y`).
- Rechazar y detener el pipeline (`alt+n`).
- Editar el prompt del siguiente paso antes de autorizarlo (`alt+e`).

### 7.3. Depuración (`.wari/launch.json`)

Para depurar con `F5` (`alt+s`), WARI lee `.wari/launch.json`. El bloque `args` se envía **tal cual** al adaptador (`netcoredbg` para C#, `java-debug` para Java): WARI no lo interpreta ni expande variables como `${workspaceFolder}`, así que use rutas reales. Los nombres de los argumentos de cada adaptador no están verificados con adaptadores reales; los del ejemplo son ilustrativos, consulte la documentación del adaptador. Depurar Java además requiere integrar el plugin java-debug en jdtls, que WARI todavía no hace.

```json
{
  "configurations": [
    {
      "name": "Lanzar API C#",
      "adapter": "netcoredbg",
      "request": "launch",
      "args": {
        "program": "/ruta/absoluta/a/bin/Debug/net8.0/MiServicio.dll",
        "cwd": "/ruta/absoluta/al/proyecto"
      }
    },
    {
      "name": "Lanzar Servicio Java",
      "adapter": "java-debug",
      "request": "launch",
      "args": {
        "mainClass": "com.ejemplo.Application",
        "projectName": "mi-proyecto"
      }
    }
  ]
}
```

---

## 8. Solución de problemas

### La CLI del agente no es encontrada (`executable file not found in $PATH`)
- Asegúrese de que el comando de la CLI (`claude`, `agy`, `codex` u `opencode`) funcione directamente en su terminal fuera de WARI.
- Verifique que la ruta de instalación de la CLI esté exportada en la variable de entorno `PATH` de su shell (`which claude`, `which codex`, etc.).

### Sesión no iniciada (errores 401 o de autenticación)
- Si una CLI responde con `401 Unauthorized` o un error de autenticación, abra una terminal aparte e inicie sesión en esa CLI (`codex login`, `opencode auth login`, o abrir `claude` o `agy`) y vuelva a intentarlo. WARI no puede iniciar sesión por usted.

### "No se pudo reanudar la sesión"
- Es otro problema: la CLI no encontró la sesión que WARI quiso reanudar (por ejemplo `Session not found` en opencode o `no rollout found for thread id` en codex). Pulse `ctrl+n` para empezar una sesión nueva.

### Error de imagen de sandbox ausente (`run_project`)
- Si al pulsar `alt+g` para ejecutar el proyecto en el sandbox se reporta que no se encuentra la imagen o el motor de contenedores:
  - Verifique que `podman` o `docker` esté instalado y el demonio esté corriendo.
  - Asegúrese de contar con la imagen base del contenedor configurada en su entorno.

### La tecla Alt no responde en macOS
- macOS no tiene Win y Option escribe caracteres tipográficos. No hace falta tocar la terminal: pulse la **tecla líder** (`ctrl+e`) y después la letra del atajo. `ctrl+e` `f` equivale a `alt+f`; `ctrl+e` `1` a `alt+1`; `esc` cancela. La ayuda (`F1` o `?`) y la barra de estado muestran los atajos de esa forma en macOS.
- Si prefiere `alt+`, active *"Usar opción como tecla meta"* en Terminal.app o configure *"Left Option key = Esc+"* en iTerm2 (vea la sección 4.2).
- La tecla líder se cambia en `keys.json` con la acción `leader`.

### Comprobación de procesos huérfanos
WARI aísla a cada agente y proceso hijo en un grupo de procesos independiente (`Setpgid` en sistemas Unix). Si desea verificar en Linux o macOS que no hayan quedado procesos residuales activos tras una salida imprevista:
```sh
# Obtenga el PGID reportado en los logs o revise los procesos vivos:
scripts/orphans.sh <pgid>
```

---

## 9. Cómo agregar un adaptador de CLI nuevo

WARI está estructurado de manera que los componentes del editor (`internal/editor`) y los agentes (`internal/agent`) están completamente desacoplados: no se importan entre sí y se comunican únicamente mediante eventos normalizados a través de `internal/orch` y `internal/tui`.

Para incorporar soporte para una nueva CLI oficial:
1. Cree un paquete en `internal/agent/<nombre-cli>/`.
2. Implemente la interfaz [`turn.Driver`](internal/agent/turn/turn.go) con los métodos:
   - `Spec(task agent.Task) (proc.Spec, error)`: define el comando y argumentos para invocar la CLI en modo streaming sin interfaz interactiva.
   - `Parse(line []byte) ([]agent.Event, error)`: traduce cada línea JSON emitida por la CLI a los eventos normalizados de WARI (`Started`, `TextDelta`, `ToolCall`, `Usage`, `Error`).
   - `Name() string`: nombre de la CLI.
3. Si la CLI no emite un evento estructurado de fin de turno, implemente también [`turn.Finisher`](internal/agent/turn/turn.go) para sintetizar el evento `agent.Done`.
4. Grabe fixtures reales de la salida de la CLI en `testdata/<nombre-cli>/*.jsonl` y sanitícelos con `scripts/sanitize-fixture.sh`. Nunca diseñe parsers basándose en memoria o suposiciones.
5. Registre la fábrica del agente en la variable `agentFactories` de `cmd/wari/main.go`.

Consulte `.claude/skills/wari-cli-adapter/SKILL.md` para ver el protocolo de desarrollo de adaptadores y `docs/codex-events.md` como ejemplo de documentación de esquema.

---

## 10. Estado del proyecto y limitaciones honestas

WARI es un proyecto en desarrollo activo enfocado en estabilidad y bajo consumo de recursos. Con el fin de mantener expectativas transparentes:

- **Pruebas de LSP y Debugger (DAP):** Los clientes LSP y DAP están implementados en el núcleo, pero en el entorno de desarrollo y pruebas continuas (CI) han sido validados principalmente contra servidores simulados y stubs. La compatibilidad con versiones específicas de servidores reales (`jdtls`, `netcoredbg`, etc.) continúa en proceso de calibración.
- **Soporte de Windows:** Compila y permite editar código y ejecutar agentes, pero el control de procesos es parcial (falta soporte de *Job Objects* para recolección de subprocesos y suspensión real de procesos en Windows nativo). Se recomienda WSL2 para entornos de producción.
- **Integración Continua:** Las suites de pruebas automatizadas y análisis estático (`go test -race`, `go vet`, `staticcheck`) están activadas para Linux y macOS, y validación de compilación para Windows.
- **Documentación de arquitectura:** El plan detallado por fases se encuentra en `docs/PLAN.md`, el historial de decisiones técnicas en `docs/decisions.md`, y las normas de contribución en `AGENTS.md`.

---

## 11. Licencia

WARI se distribuye bajo los términos de la licencia **GNU Affero General Public License v3.0 (AGPL-3.0)**. Consulte el archivo `LICENSE` para conocer los términos completos.
