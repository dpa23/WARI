package orch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dpa23/wari/internal/agent"
)

// Detección de bucles (fase 7). Se aplica a cualquier agente supervisado, sea
// de un pipeline o no, con dos señales:
//
//   - la misma herramienta con los mismos argumentos N veces seguidas dentro
//     de un turno: el agente se pausa en el acto (LoopTool);
//   - N turnos seguidos sin ningún cambio de archivo, solo para agentes con
//     Task.Permissions == PermissionAcceptEdits (los únicos que pueden
//     editar): al terminar el turno N el agente ya está Idle y no hay nada
//     que pausar, así que el turno siguiente arranca pausado (LoopTurns). Un
//     turno cuenta como "con cambios" si el agente propuso editar un archivo
//     o el watcher le atribuyó uno.
//
// En ambos casos se emite un Notice de clase NoticeLoop con el motivo, antes
// de la pausa. Reanudar al agente (Resume) es la decisión del usuario de
// seguir; los contadores se reinician al detectar el bucle, así que hacen
// falta otras N repeticiones para volver a avisar.

const (
	// LoopTool y LoopTurns son los valores de Notice.Loop.
	LoopTool  = "tool"
	LoopTurns = "turns"

	// defaultLoopLimit es N cuando el límite vale 0.
	defaultLoopLimit = 3
)

// LoopLimits configura la detección de bucles. Para cada campo, 0 usa el valor
// predeterminado (3) y un valor negativo desactiva esa señal.
type LoopLimits struct {
	// SameTool es cuántas llamadas idénticas seguidas son un bucle.
	SameTool int
	// IdleTurns es cuántos turnos seguidos sin cambios de archivo son un bucle.
	IdleTurns int
}

func resolveLimit(n int) int {
	switch {
	case n == 0:
		return defaultLoopLimit
	case n < 0:
		return 0
	}
	return n
}

// SetLoopLimits fija los límites de la detección de bucles. Vale para los
// turnos que se lancen después.
func (s *Supervisor) SetLoopLimits(l LoopLimits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loops = l
}

// toolKey identifica una llamada por herramienta y argumentos; el JSON se
// compacta para que el espaciado no la distinga.
func toolKey(name string, in json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, in) == nil {
		return name + "\x00" + b.String()
	}
	return name + "\x00" + string(in)
}

// noteToolCallLocked cuenta las llamadas idénticas seguidas y, al llegar al
// límite, avisa y pausa al agente.
func (s *Supervisor) noteToolCallLocked(e *entry, ev agent.ToolCall) {
	n := resolveLimit(s.loops.SameTool)
	if n == 0 {
		return
	}
	key := toolKey(ev.Name, ev.Input)
	if key == e.lastTool {
		e.toolRun++
	} else {
		e.lastTool, e.toolRun = key, 1
	}
	if e.toolRun < n {
		return
	}
	e.lastTool, e.toolRun = "", 0
	s.pushNoticeLocked(e, Notice{
		Kind: NoticeLoop, Agent: e.id, Loop: LoopTool,
		Text: fmt.Sprintf("bucle: %s llamó a %s con los mismos argumentos %d veces seguidas; pausado",
			e.name, ev.Name, n),
	})
	if e.state == Running {
		s.goLocked(func() { s.pauseForLoop(e) })
	}
}

// noteTurnEndLocked cuenta los turnos seguidos sin cambios de archivo; al
// llegar al límite deja marcado que el próximo turno arranque pausado.
func (s *Supervisor) noteTurnEndLocked(e *entry) {
	n := resolveLimit(s.loops.IdleTurns)
	if n == 0 || e.task.Permissions != agent.PermissionAcceptEdits {
		return
	}
	if e.turnEdited {
		e.idleTurns = 0
		return
	}
	e.idleTurns++
	if e.idleTurns >= n {
		e.idleTurns = 0
		e.pauseNext = fmt.Sprintf("bucle: %s hizo %d turnos seguidos sin cambiar ningún archivo; pausado antes de seguir",
			e.name, n)
	}
}

// pauseIfLooping pausa el turno recién lanzado si el anterior dejó marcado un
// bucle de turnos sin cambios. Se llama con el mutex suelto.
func (s *Supervisor) pauseIfLooping(e *entry, seq int) {
	s.mu.Lock()
	reason := e.pauseNext
	e.pauseNext = ""
	if reason == "" || s.closing || e.turnSeq != seq || e.state != Running {
		s.mu.Unlock()
		return
	}
	s.pushNoticeLocked(e, Notice{Kind: NoticeLoop, Agent: e.id, Loop: LoopTurns, Text: reason})
	s.mu.Unlock()
	s.pauseForLoop(e)
}

// pauseForLoop pausa al agente. Si ya no corre (el turno terminó mientras
// tanto) no hace nada; si el agente falla al pausar, avisa.
func (s *Supervisor) pauseForLoop(e *entry) {
	err := s.Pause(e.id)
	if err == nil || errors.Is(err, ErrState) || errors.Is(err, ErrClosed) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushNoticeLocked(e, Notice{
		Kind: NoticeLoop, Agent: e.id, Late: true,
		Text: fmt.Sprintf("no se pudo pausar a %s tras detectar un bucle: %v", e.name, err),
	})
}

// Observe registra f para que reciba cada novedad (eventos, cambios de estado
// y avisos) justo antes de encolarla en Updates, en el mismo orden y sin
// descartar ninguna aunque nadie lea Updates. f se llama con el mutex del
// supervisor tomado: debe volver enseguida y no puede llamar al supervisor
// (encolar en una estructura propia y despertar a otra goroutine, nada más).
// Hay que llamarlo antes de Submit.
func (s *Supervisor) Observe(f func(Update)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observers = append(s.observers, f)
}
