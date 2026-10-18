# Revisión de UX (Gemini 3.8, vía herdr, 2026-10-17)

Revisión de solo lectura del TUI. Las mejoras se implementan por fases; ver docs/progress.md.

  ### 1. Resolución de colisiones críticas en terminales comunes (XON/XOFF, macOS Option y Fullscreen)

  • Problema:
      • save está asignado por defecto a ctrl+s (keymap.go:42). En terminales Unix/Linux estándar y sesiones SSH con control de flujo por software habilitado (IXON), ctrl+s emite
      XOFF, congelando la salida del terminal hasta que el usuario pulse ctrl+q (atajo no documentado ni mapeado).
      • agent_next / agent_prev (alt+down / alt+up) y las reversiones revert_file / revert_turn / revert_agent (alt+r, alt+t, alt+a) fallan sistemáticamente en macOS (Terminal.app
      e iTerm2 por defecto envían secuencias de caracteres internacionales Option como ®, †, å en vez de prefijo ESC).
      • newline (alt+enter, keymap.go:33) colisiona con el atajo global de pantalla completa en Windows Terminal y varios emuladores de Linux.
  • Propuesta:
      1. Configurar en keymap.go atajos alternativos libres de conflicto: añadir ctrl+x ctrl+s o alias en paleta (:w), y asegurar desactivación de IXON en termios si la plataforma
      lo permite.
      2. Añadir alternativas estándar para macOS sin depender de Alt: alt+j / alt+k o ctrl+] / ctrl+[ para navegar agentes, y prefijos modales seguros para revertir.
      3. Promover ctrl+j como alternativa primaria documentada para newline en el placeholder del textarea.
  • Archivo afectado: keymap.go
  • Esfuerzo: Bajo
  ──────
  ### 2. Atrapamiento del foco en el Editor ("Tab Trap") y navegación global entre paneles

  • Problema:
      • En layout.go:242-247, cuando el editor tiene el foco, la tecla Tab (focus_next) queda absorbida para insertar 4 espacios de indentación.
      • Como consecuencia, el usuario que está editando queda atrapado: no puede avanzar al panel de Agentes con Tab. Se ve forzado a recordar que debe hacer Shift+Tab (ir en
      reversa al árbol de archivos) o abrir la paleta con ctrl+p.
  • Propuesta:
      • Implementar atajos de salto directo omnidireccionales inspirados en tmux/helix/lazygit:
          • Atajos numéricos globales (alt+1 Árbol, alt+2 Editor, alt+3 Agentes).
          • O saltos direccionales con prefijo estilo Vim/Helix (ctrl+w h / ctrl+w l o F6).
      • En caso de pulsar Esc dentro del editor, desarmar el modo de inserción o permitir que el siguiente Tab cambie de panel.
  • Archivo afectado: layout.go
  • Esfuerzo: Bajo
  ──────
  ### 3. Rediseño responsivo y badges compactos para la lista de agentes

  • Problema:
      • En panel.go:696-698, cada línea de la lista de agentes formatea rígidamente: marker + name(10) + stateLabel(15) + turns + tokens + costUSD + alert.
      • La geometría en layout.go:337 asigna al panel de agentes el 35% del ancho (típicamente entre 24 y 35 columnas). A ese tamaño, MaxWidth corta la línea antes de llegar a los
      tokens, el costo o el indicador ⚠ choque. Además, los nombres de estado en inglés (running, queued) desentonan con el resto de la interfaz en español.
  • Propuesta:
      • Diseñar un formato adaptativo con dos densidades:
          • Modo ultra-compacto (< 42 columnas): usar chips/glifos de 1 celda para el estado (● verde pensando, ⏸ amarillo pausa, ⏳ cian cola, ✓ libre, ✗ error/tope), seguidos
          del identificador corto y costo condensado (ej: ▸ #1 ● run  US$0.02).
          • Modo extendido (≥ 42 columnas): tabla alineada con etiquetas en español (activo, pausa, en cola, fallo).

  • Archivo afectado: panel.go
  • Esfuerzo: Medio
  ──────
  ### 4. Proyección del Diff al panel central del Editor en lugar de la columna de Chat

  • Problema:
      • En panel.go:610-613 y panel.go:883-891, al presionar diff_file (ctrl+r), el diff unificado sustituye el historial del chat dentro del viewport derecho (de apenas 25–35
      columnas).
      • Inspeccionar diffs de código con líneas envueltas en 30 columnas es frustrante e ininteligible. Mientras tanto, el panel central del editor (con 40–50 columnas, números de
      línea y soporte de scroll horizontal) permanece ocioso.
  • Propuesta:
      • Mostrar la vista del diff del turno en el panel central (editorview) en modo lectura, usando la infraestructura de chroma/diff existente.
      • El panel de agentes conserva el chat visible con los controles de decisión (alt+r / alt+t / esc), permitiendo leer las modificaciones con el ancho completo que un
      desarrollador necesita.
  • Archivo afectado: layout.go e panel.go
  • Esfuerzo: Alto
  ──────
  ### 5. Diálogo de confirmación interactivo no volátil para Reversiones (File / Turn / Agent)

  • Problema:
      • En panel.go:974-979, la reversión pide repetir la misma tecla (alt+r, alt+t o alt+a).
      • Sin embargo, cualquier pulsación intermedia —incluso teclas no destructivas como PageUp/PageDown para revisar el diff— cancela el armado de inmediato y en silencio
      (panel.go:273-275). Además, la similitud entre revertir un archivo, un turno o todo el agente genera alto riesgo cognitivo de revertir trabajo accidentalmente.
  • Propuesta:
      • Sustituir el armado volátil por una barra/modal explícito de confirmación en el pie del panel:
        ┌ Revertir cambios ──────────────────────────────────────────┐
        │ Alcance: [Archivo: main.go] (Turno 3)                      │
        │ ¿Deseas descartar las ediciones de este archivo?           │
        │  [ Enter: Confirmar ]   [ Tab: Cambiar alcance ]  [ Esc ]  │
        └────────────────────────────────────────────────────────────┘

      • El usuario puede seguir navegando y leyendo el diff mientras el diálogo permanece activo.
  • Archivo afectado: panel.go
  • Esfuerzo: Medio
  ──────
  ### 6. Indicadores de archivos reclamados y editados por agentes en el Árbol de archivos y Editor

  • Problema:
      • model.go y la cabecera del editor en layout.go:570-580 son ajenos al trabajo concurrente de los agentes.
      • Si el Agente 1 está editando internal/proc/proc.go y el Agente 2 tiene reclamado internal/proc/group.go, el árbol de archivos no muestra ningún glifo ni color. El
      desarrollador no sabe si un archivo está siendo modificado concurrentemente hasta que guarda o sufre un conflicto tardío.
  • Propuesta:
      • Comunicar los reclamos/ediciones del supervisor al layout y representarlos visualmente:
          • En el árbol de archivos: añadir glifos sutiles junto al nombre (ej. ✎ cian si un agente lo tiene en edición activa; ● amarillo si tiene un checkpoint no confirmado).
          • En el título del editor: si el archivo en pantalla coincide con el que un agente está tocando, añadir un chip informativo: proc.go ● [Agente 1 editando].

  • Archivo afectado: model.go e layout.go
  • Esfuerzo: Medio
  ──────
  ### 7. Banner de choque de archivos con resolución asistida inmediata

  • Problema:
      • Ante una colisión (NoticeConflict), el aviso se imprime como texto plano en el chat del agente y en la barra de estado inferior con fondo amarillo (layout.go:679-684).
      • Si el panel de agentes está oculto (ctrl+g) o si el usuario está enfocado en otro agente, el aviso de choque en la barra de estado es fácil de pasar por alto. Además, el
      usuario debe recordar manualmente que debe ir a ese agente y usar agent_pause para ceder el archivo o agent_stop para abortar el turno.
  • Propuesta:
      • Convertir la alerta de choque en un banner interactivo prioritario:
          • Mostrar en la barra superior o sobre el editor: ⚠ Choque en 'parser.go' entre Agente 1 y Agente 2 · [F5 / Alt+C para resolver].
          • Al activar el atajo, enfocar directamente el agente en pausa y mostrar un menú de acción simple:
          [1] Ceder archivo a Agente 2 (Reanudar) | [2] Mantener Agente 1 (Detener Agente 2).

  • Archivo afectado: layout.go e panel.go
  • Esfuerzo: Medio
  ──────
  ### 8. Descubribilidad: Overlay de ayuda contextual (?) y ampliación de la Paleta de comandos

  • Problema:
      • La barra de estado solo expone 4 atajos globales (layout.go:689-695). Si el ancho no alcanza, se omiten. Atajos vitales como parada de agentes (ctrl+x), parada global
      (ctrl+k), diff (ctrl+r) y conmutación de paneles (tab) no son descubribles sin inspeccionar el código o keys.json.
      • La paleta (palette.go:69-86) carece de comandos para pausar/reanudar agente, detener agente seleccionado, ver diff o revertir, y las entradas de cambio de foco tienen
      pista de tecla vacía "".
  • Propuesta:
      1. Implementar un overlay modal de ayuda rápida activable con ? o F1 (estándar en herramientas como lazygit, helix y btop) que liste los atajos activos organizados por
      categorías (Navegación, Editor, Agentes, Diffs).
      2. Añadir a palette.go las acciones faltantes de gestión de agentes y poblar las pistas de los atajos de cambio de foco (tab, shift+tab).
  • Archivo afectado: palette.go e layout.go
  • Esfuerzo: Bajo
  ──────
  ### 9. Medidor proactivo de consumo y alerta de umbral de presupuestos

  • Problema:
      • En panel.go:687-698, el consumo (turns, tokens, costUSD) se expone como texto plano sin jerarquía ni indicadores de advertencia.
      • No existe aviso preventivo al aproximarse al límite (Budget). El agente simplemente se detiene de forma súbita con estado BudgetExceeded, interrumpiendo el flujo de
      trabajo sin previo aviso.
  • Propuesta:
      • Introducir señalización semántica por umbrales:
          • Cuando el uso supere el 75% de los turnos o tokens máximos, colorear el indicador en amarillo ámbar.
          • Al superar el 90%, activar alerta visual en rojo bold o texto parpadeante.
          • Incorporar una barra compacta de progreso en la línea de estado del agente seleccionado (ej: Turnos [■■■■■■■░░░] 7/10 · 85%).

  • Archivo afectado: panel.go y render.go
  • Esfuerzo: Bajo
  ──────
  ### 10. Accesibilidad de color, eliminación de TrueColor en crudo y compatibilidad con fondos claros

  • Problema:
      • En model.go:29-33, los colores del gutter y selección están cableados en secuencias de escape ANSI TrueColor de 24 bits (\x1b[38;2;110;118;129m y \x1b[48;2;38;79;120m).
      • En emuladores de terminal con fondo claro o configurados en paletas básicas de 16/256 colores, los números de línea quedan casi invisibles y la selección azul oscuro
      genera un contraste deficiente con el texto.
      • Del mismo modo, en render.go:65-70, el color amarillo (11) utilizado para avisos y ediciones resulta ilegible sobre terminales con fondo blanco/beige (incumpliendo WCAG
      AA).
  • Propuesta:
      • Migrar los estilos del editor a Lipgloss utilizando lipgloss.AdaptiveColor{Light: "...", Dark: "..."} o colores indexados semánticos que respeten la luminosidad de fondo
      del emulador.
      • Sustituir el amarillo puro en advertencias por tonos ámbar/naranja de alto contraste en temas claros para asegurar legibilidad universal.
  • Archivo afectado: model.go, render.go e layout.go
  • Esfuerzo: Medio
  ──────
  ### Resumen de Priorización y Esfuerzo

   #                        │ Mejora UI/UX                                                  │ Área                    │ Archivo principal                │ Esfuerzo
  ──────────────────────────┼───────────────────────────────────────────────────────────────┼─────────────────────────┼──────────────────────────────────┼─────────────────────────
   1                        │ Resolver colisiones de atajos (ctrl+s, alt, alt+enter)        │ Atajos en terminales    │ internal/config/keymap.go        │ Bajo
   2                        │ Escape de foco en editor y salto directo entre paneles        │ Jerarquía / Foco        │ internal/tui/layout/layout.go    │ Bajo
   3                        │ Lista de agentes adaptable con badges compactos               │ Estados de agentes      │ internal/tui/panel.go            │ Medio
   4                        │ Proyección del Diff al panel del Editor                       │ Diff y Revert           │ internal/tui/layout/layout.go    │ Alto
   5                        │ Modal de confirmación persistente para reversiones            │ Diff y Revert           │ internal/tui/panel.go            │ Medio
   6                        │ Indicadores de agentes en Árbol de archivos y Editor          │ Jerarquía / Estados     │ internal/tui/filetree/model.go   │ Medio
   7                        │ Banner interactivo de resolución de choques de archivos       │ Choques / Conflictos    │ internal/tui/layout/layout.go    │ Medio
   8                        │ Overlay de atajos (?) y acciones de agentes en Paleta         │ Descubribilidad         │ internal/tui/layout/palette.go   │ Bajo
   9                        │ Alerta visual proactiva de umbral de presupuesto              │ Presupuestos            │ internal/tui/panel.go            │ Bajo
   10                       │ Accesibilidad, AdaptiveColor y eliminación de TrueColor crudo │ Color y contraste       │ internal/tui/editorview/model.go │ Medio

────────────────────────────────────────────────────────────
> /plan Nota del usuario: puedes usar herdr o tus propios subagentes/multiagentes para esta revisión si te ayuda (por ejemplo, para leer en paralelo). Sigue en modo de solo lectura:
  sin editar archivos ni pasar a modo auto.

○ Thought for 11.4s (The user is encouraging parallel processing via subagents or multiagents within this review context. This suggests a potential complexity or volume of infor...)
● Explored 5 files (decisions.md, progress.md, types.go, +2) (ctrl+o to expand)

▸ Thought for 24s
  A contextual help overlay, triggered by a specific key, will be implemented to improve discoverability of agent controls within the TUI. The palette will be updated to include...
   Informe de Diseño UI/UX: Control Multiagente en WARI (Modo Solo Lectura)

  He realizado una revisión integral y en profundidad de la arquitectura visual y de interacción de WARI, examinando no solo los componentes visuales de presentación (layout.go,
  palette.go, panel.go, editorview/model.go, filetree/model.go, diff.go y keymap.go), sino también su acoplamiento con el orquestador (orch.go, workspace.go) y las directrices
  técnicas del proyecto (docs/decisions.md y docs/progress.md).

  A continuación se detalla la propuesta de diseño priorizada (10 mejoras concretas), estructurada para mantener la estricta separación de responsabilidades (el editor y los
  agentes no se conocen entre sí), optimizada para máquinas de recursos moderados (2 núcleos, renderizado en sub-milisegundos) y adaptada a emuladores de terminal reales (Linux,
  macOS y Windows).
  ──────
  ## Representación Esquemática del Layout Actual vs. Propuesto

  ### Estado Actual en Terminal Estándar (80x24)

    ┌ Archivos ─────┬─ internal/proc/proc.go ● ───────┬─ Agentes ──────────────────┐
    │ ▾ internal/   │ 45 func Kill(pgid int) error {  │ ▸ agente 1  running  t2/10…│ <- Truncado
    │   ▾ proc/     │ 46     if pgid <= 1 {           │ ───────────────────────────│
    │       proc.go │ 47         return ErrInvalid    │ (historial del chat        │
    │       group.go│ 48     }                        │  muy comprimido)           │
    │   ▾ tui/      │ 49     return syscall.Kill(-pgi…│ ✎ internal/proc/proc.go    │
    │               │                                 │ [Pausado por choque]       │
    │               │                                 │ > Escribe un mensaje…      │
    └───────────────┴─────────────────────────────────┴────────────────────────────┘
      proc.go ● sin guardar │ ⚠ choque: agente 2 reclamó … │ ctrl+p paleta · activos 1/2
      ^ Colisión de atajo: Tab no sale del editor    ^ Reversión se desarma con cualquier tecla

  ### Estado Propuesto con las 10 Mejoras Aplicadas

    ┌ [1] Archivos ─┬─ [2] proc.go ● [Agente 1 editando] ─┬─ [3] Agentes ──────────────┐
    │ ▾ internal/   │ 45 func Kill(pgid int) error {      │ ▸ #1 ● actv  $0.012 [70%]  │ <- Badges
    │   ▾ proc/     │ 46     if pgid <= 1 {               │   #2 ⏸ choc  $0.004 [20%]  │
    │     ✎ proc.go │ 47         return ErrInvalid        │ ───────────────────────────│
    │       group.go│ 48     }                            │ [Agente 1]: Propuse cambio │
    │               │ 49     return syscall.Kill(-pgi…    │ en el manejo de señales.   │
    │               │                                     │ ───────────────────────────│
    │               │ [Ver Diff en Editor: Alt+D]         │ [1] Ceder archivo  [2] Stop│
    └───────────────┴─────────────────────────────────────┴────────────────────────────┘
      Alt+1/2/3 Foco │ ? Ayuda │ ⚠ Choque en proc.go [Alt+C Resolver] │ Activos 1/2 · Cola 0
  ──────
  ## Lista Priorizada de Mejoras UI/UX
  ──────
