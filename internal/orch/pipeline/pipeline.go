// Package pipeline encadena goals entre agentes (fase 7): una lista ordenada
// de pasos, cada uno con su agente, su prompt, su modo y su condición de
// paso, ejecutada sobre un orch.Supervisor. Conoce el contrato de
// internal/agent y la API de internal/orch; nunca sabe nada de ninguna CLI ni
// del editor.
//
// # Pasos y modos
//
// Cada paso lanza un turno de un agente: uno nuevo (Step.Agent, una fábrica) o
// uno que ya existe en el supervisor (Step.Use, por nombre; recibe el prompt
// con Send). El prompt puede contener {{prev}}, que se sustituye por el
// resultado (agent.Done.Result) del paso anterior; es texto de un modelo
// colado en el prompt del siguiente, por eso los pasos Manual y Semi pasan
// por un gate donde el usuario lo ve antes de que se use.
//
//   - Manual: el paso solo arranca cuando el usuario lo lanza con Run, que es
//     también lo que arranca el pipeline.
//   - Semi: arranca solo (al llegar su turno) y, al terminar, el pipeline queda
//     en el gate.
//   - Auto: arranca y pasa al siguiente sin gate. Exige presupuesto propio
//     (Step.Budget no vacío) y un agente nuevo: sin límites que cortarlo, Auto
//     no se permite.
//
// El pipeline no arranca solo: el usuario lo lanza con Run, que pone en marcha
// el primer paso sea cual sea su modo.
//
// # Gate
//
// Al terminar un paso Manual o Semi con otro paso por delante, el pipeline
// queda en GateWaiting (Status: agent.StatusWaitingApproval) con un resumen
// (Gate): el resultado del paso, los archivos que editó (orch.Edits; el diff
// contra el checkpoint se pide con orch.Diff) y el prompt del siguiente. De ahí
// sale con Approve (sigue; si el siguiente es Manual queda Ready hasta que se
// lo lance con Run), Reject (detiene el pipeline) o EditPrompt (cambia el
// prompt del siguiente paso y sigue esperando). Tras el último paso no hay
// gate: el pipeline termina.
//
// # Condición de paso
//
// Un paso pasa si su turno termina con agent.Done sin IsError. Step.Condition
// la sustituye por otra función del resultado. Si no pasa, el pipeline
// termina en Failed. Un agente detenido, pasado de presupuesto o que no se
// pudo lanzar también detiene el pipeline. Un agente pausado (por el
// usuario, un choque de archivos o la detección de bucles de orch) deja el
// pipeline esperando: reanudarlo lo continúa.
//
// # Archivo .wari/pipeline.json
//
// Load y Parse leen un pipeline de un JSON (no se admiten campos
// desconocidos):
//
//	{
//	  "name": "feature",
//	  "steps": [
//	    {
//	      "name": "plan",                  // obligatorio y único
//	      "agent": "claude",               // fábrica por nombre (ver Registry)
//	      "prompt": "Planea: ...",         // obligatorio; admite {{prev}}
//	      "mode": "semi",                  // manual (predeterminado), semi o auto
//	      "model": "",                     // opcional
//	      "budget": {"max_turns": 5, "max_tokens": 50000, "max_seconds": 300}
//	    },
//	    {
//	      "name": "impl",
//	      "use": "agente 1",               // alternativa a "agent": un agente existente
//	      "prompt": "Implementa este plan:\n{{prev}}",
//	      "mode": "manual",
//	      "require": "LISTO"               // opcional: el resultado debe contener este texto
//	    }
//	  ]
//	}
//
// "agent" y "use" son excluyentes y uno es obligatorio. Los comentarios de
// arriba son solo explicativos: JSON no los admite.
package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
)

var (
	// ErrState indica que la operación no es válida en el estado actual.
	ErrState = errors.New("pipeline: operación no válida en el estado actual")
	// ErrClosed indica que el Runner ya se cerró.
	ErrClosed = errors.New("pipeline: cerrado")
)

// Mode es cómo arranca un paso y cómo pasa al siguiente.
type Mode int

const (
	// Manual: arranca solo cuando el usuario lo lanza (Run).
	Manual Mode = iota
	// Semi: arranca solo; al terminar queda en el gate.
	Semi
	// Auto: arranca y pasa al siguiente sin gate; exige presupuesto.
	Auto
)

func (m Mode) String() string {
	switch m {
	case Manual:
		return "manual"
	case Semi:
		return "semi"
	case Auto:
		return "auto"
	}
	return "unknown"
}

// Result es lo que dejó el turno de un paso.
type Result struct {
	// Text es agent.Done.Result.
	Text string
	// IsError es agent.Done.IsError (o se sintetizó porque el agente falló).
	IsError bool
	// Turns es agent.Done.Turns.
	Turns int
}

// Step es un paso del pipeline.
type Step struct {
	// Name identifica al paso; único dentro del pipeline.
	Name string
	// Agent crea el agente del paso (uno nuevo por paso). Excluyente con Use.
	Agent func() agent.Agent
	// Use es el nombre de un agente que ya existe en el supervisor: el paso le
	// envía su prompt con Send. Excluyente con Agent.
	Use string
	// Prompt es el mensaje del turno; {{prev}} se sustituye por el resultado
	// del paso anterior.
	Prompt string
	// Model sustituye el modelo de la tarea base; vacío lo deja.
	Model string
	// Mode es Manual, Semi o Auto.
	Mode Mode
	// Budget limita al agente nuevo del paso. Vacío usa el de Config. En Auto
	// es obligatorio.
	Budget orch.Budget
	// Condition decide si el paso pasó; nil: el turno terminó sin IsError.
	Condition func(Result) bool
}

// Pipeline es una lista ordenada de pasos.
type Pipeline struct {
	Name  string
	Steps []Step
}

// Validate comprueba las reglas del pipeline.
func (p Pipeline) Validate() error {
	if len(p.Steps) == 0 {
		return errors.New("pipeline: sin pasos")
	}
	seen := make(map[string]bool, len(p.Steps))
	for i, s := range p.Steps {
		at := fmt.Sprintf("pipeline: paso %d (%q)", i+1, s.Name)
		switch {
		case strings.TrimSpace(s.Name) == "":
			return fmt.Errorf("pipeline: el paso %d no tiene nombre", i+1)
		case seen[s.Name]:
			return fmt.Errorf("%s: nombre repetido", at)
		case strings.TrimSpace(s.Prompt) == "":
			return fmt.Errorf("%s: sin prompt", at)
		case s.Agent == nil && s.Use == "":
			return fmt.Errorf("%s: falta el agente (fábrica o nombre de uno existente)", at)
		case s.Agent != nil && s.Use != "":
			return fmt.Errorf("%s: agente y agente existente son excluyentes", at)
		case s.Mode < Manual || s.Mode > Auto:
			return fmt.Errorf("%s: modo inválido", at)
		case s.Mode == Auto && s.Use != "":
			return fmt.Errorf("%s: Auto exige un agente nuevo, el presupuesto de uno existente ya está fijado", at)
		case s.Mode == Auto && s.Budget == (orch.Budget{}):
			return fmt.Errorf("%s: Auto exige presupuesto (turnos, tiempo o tokens)", at)
		}
		seen[s.Name] = true
	}
	return nil
}

// State es el estado del pipeline.
type State int

const (
	// NotStarted: todavía no se lanzó (Run).
	NotStarted State = iota
	// Running: hay un paso en curso.
	Running
	// Ready: el siguiente paso es Manual y espera a que lo lancen (Run).
	Ready
	// GateWaiting: espera la aprobación del usuario (Gate).
	GateWaiting
	// Done: terminó el último paso.
	Done
	// Stopped: lo detuvo el usuario (Reject, Stop, o parando al agente).
	Stopped
	// Failed: un paso falló, no cumplió su condición o no se pudo lanzar.
	Failed
)

func (s State) String() string {
	switch s {
	case NotStarted:
		return "not-started"
	case Running:
		return "running"
	case Ready:
		return "ready"
	case GateWaiting:
		return "gate"
	case Done:
		return "done"
	case Stopped:
		return "stopped"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// StepState es el estado de un paso.
type StepState int

const (
	StepPending StepState = iota
	StepReady
	StepRunning
	StepDone
	StepFailed
)

func (s StepState) String() string {
	switch s {
	case StepPending:
		return "pending"
	case StepReady:
		return "ready"
	case StepRunning:
		return "running"
	case StepDone:
		return "done"
	case StepFailed:
		return "failed"
	}
	return "unknown"
}

// StepSnapshot es una copia del estado de un paso.
type StepSnapshot struct {
	Name  string
	Mode  Mode
	State StepState
	// Prompt es la plantilla (con {{prev}}); Sent, el prompt ya expandido que
	// se envió ("" mientras no arrancó).
	Prompt string
	Sent   string
	// AgentName y AgentID identifican al agente del paso en el supervisor
	// (AgentID es 0 hasta que se lanza).
	AgentName string
	AgentID   orch.ID
	// Turn es el turno del agente en que corrió el paso (0 hasta que termina).
	Turn   int
	Result Result
	// Reason explica un paso Failed.
	Reason string
}

// Gate es el resumen que espera la aprobación del usuario al pasar de un paso
// al siguiente.
type Gate struct {
	// Seq crece con cada gate nuevo, para que quien lo muestre sepa cuándo
	// cambió.
	Seq int
	// From es el paso que terminó y To, el que sigue (índices).
	From, To         int
	FromName, ToName string
	// Result es lo que dejó el paso que terminó.
	Result Result
	// AgentID y Turn identifican el turno de From, para pedir los diffs con
	// orch.Supervisor.Diff(AgentID, edit.Turn, edit.Path).
	AgentID orch.ID
	Turn    int
	// Edits son los archivos que el agente propuso editar en ese turno o que
	// el watcher le atribuyó.
	Edits []orch.Edit
	// NextPrompt es la plantilla del prompt del paso siguiente.
	NextPrompt string
	// NextMode es el modo del paso siguiente.
	NextMode Mode
}

// Snapshot es una copia del estado del pipeline.
type Snapshot struct {
	Name  string
	State State
	// Reason explica Stopped y Failed.
	Reason string
	// Cur es el paso en curso, el que espera ser lanzado o el último que
	// terminó (en Done, Failed y GateWaiting).
	Cur   int
	Steps []StepSnapshot
	// Gate es el resumen pendiente; nil fuera de GateWaiting.
	Gate *Gate
}

// Config configura al Runner.
type Config struct {
	// Task es la tarea base de los pasos con agente nuevo (Dir, Model,
	// Permissions); el Runner rellena Prompt y Model.
	Task agent.Task
	// Budget es el presupuesto de los pasos con agente nuevo que no traen el
	// suyo.
	Budget orch.Budget
}

type stepRun struct {
	Step
	prompt    string
	sent      string
	state     StepState
	agentName string
	agentID   orch.ID
	turn      int
	result    Result
	hasDone   bool
	reason    string
}

// Runner ejecuta un pipeline sobre un supervisor.
//
// Concurrencia: una única goroutine (loop) cambia el estado; las operaciones
// públicas se le envían como comandos y esperan su resultado. Las novedades
// del supervisor llegan por Observe a una cola propia sin límite (el gancho se
// llama con el mutex del supervisor y no puede esperar) y el bucle las vacía
// antes de cada comando. Nunca se llama al supervisor con mu tomado.
type Runner struct {
	sup  *orch.Supervisor
	name string
	cfg  Config

	mu      sync.Mutex // protege el estado para los lectores (Snapshot)
	state   State
	reason  string
	cur     int
	steps   []stepRun
	gate    *Gate
	gateSeq int

	qmu   sync.Mutex
	queue []orch.Update

	wake    chan struct{}
	cmds    chan func()
	quit    chan struct{}
	done    chan struct{}
	changed chan struct{}
	closed  atomic.Bool
}

// New valida el pipeline y crea un Runner sobre sup. No lanza nada hasta Run.
// Hay que cerrarlo con Close.
func New(sup *orch.Supervisor, p Pipeline, cfg Config) (*Runner, error) {
	if sup == nil {
		return nil, errors.New("pipeline: sin supervisor")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	r := &Runner{
		sup:     sup,
		name:    p.Name,
		cfg:     cfg,
		steps:   make([]stepRun, len(p.Steps)),
		wake:    make(chan struct{}, 1),
		cmds:    make(chan func()),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		changed: make(chan struct{}, 1),
	}
	for i, s := range p.Steps {
		r.steps[i] = stepRun{Step: s, prompt: s.Prompt}
	}
	sup.Observe(r.observe)
	go r.loop()
	return r, nil
}

// Changed avisa de que el estado cambió: léelo con Snapshot. Los avisos se
// funden (hay como mucho uno pendiente) y el canal se cierra con Close.
func (r *Runner) Changed() <-chan struct{} { return r.changed }

// Close detiene al Runner. No toca a los agentes ni al supervisor. Es
// idempotente.
func (r *Runner) Close() {
	if r.closed.Swap(true) {
		<-r.done
		return
	}
	close(r.quit)
	<-r.done
	close(r.changed)
}

// Status resume el estado como un agent.Status: GateWaiting es
// StatusWaitingApproval.
func (r *Runner) Status() agent.Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state {
	case Running:
		return agent.StatusRunning
	case GateWaiting:
		return agent.StatusWaitingApproval
	case Done, Stopped:
		return agent.StatusDone
	case Failed:
		return agent.StatusError
	}
	return agent.StatusIdle
}

// Snapshot devuelve una copia del estado.
func (r *Runner) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{
		Name: r.name, State: r.state, Reason: r.reason, Cur: r.cur,
		Steps: make([]StepSnapshot, len(r.steps)),
	}
	for i, st := range r.steps {
		s.Steps[i] = StepSnapshot{
			Name: st.Name, Mode: st.Mode, State: st.state,
			Prompt: st.prompt, Sent: st.sent,
			AgentName: st.agentName, AgentID: st.agentID,
			Turn: st.turn, Result: st.result, Reason: st.reason,
		}
	}
	if r.gate != nil {
		g := *r.gate
		g.Edits = append([]orch.Edit(nil), g.Edits...)
		s.Gate = &g
	}
	return s
}

// Run lanza el paso que toca: el primero si el pipeline no empezó, o el
// Manual que esperaba (Ready). Es la decisión explícita del usuario de
// arrancar, sea cual sea el modo del paso.
func (r *Runner) Run() error {
	return r.do(func() error {
		if r.state != NotStarted && r.state != Ready {
			return fmt.Errorf("%w: el pipeline está %s", ErrState, r.state)
		}
		r.launch(r.cur)
		return nil
	})
}

// Approve aprueba el gate: el pipeline pasa al paso siguiente (si es Manual,
// queda Ready hasta que lo lancen con Run).
func (r *Runner) Approve() error {
	return r.do(func() error {
		if r.state != GateWaiting {
			return fmt.Errorf("%w: no hay un gate esperando (el pipeline está %s)", ErrState, r.state)
		}
		to := r.gate.To
		r.mutate(func() { r.gate = nil })
		r.begin(to)
		return nil
	})
}

// Reject rechaza el gate y detiene el pipeline.
func (r *Runner) Reject() error {
	return r.do(func() error {
		if r.state != GateWaiting {
			return fmt.Errorf("%w: no hay un gate esperando (el pipeline está %s)", ErrState, r.state)
		}
		from := r.gate.FromName
		r.mutate(func() {
			r.gate = nil
			r.state = Stopped
			r.reason = fmt.Sprintf("rechazado en el gate tras %q", from)
		})
		return nil
	})
}

// EditPrompt cambia la plantilla del prompt del próximo paso: el que sigue al
// gate, o el que espera ser lanzado. Con un gate abierto, sigue abierto.
func (r *Runner) EditPrompt(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return errors.New("pipeline: el prompt no puede estar vacío")
	}
	return r.do(func() error {
		var n int
		switch r.state {
		case GateWaiting:
			n = r.gate.To
		case NotStarted, Ready:
			n = r.cur
		default:
			return fmt.Errorf("%w: ningún paso por lanzar (el pipeline está %s)", ErrState, r.state)
		}
		r.mutate(func() {
			r.steps[n].prompt = prompt
			if r.gate != nil {
				r.gate.NextPrompt = prompt
			}
		})
		return nil
	})
}

// Stop detiene el pipeline: cancela el turno del paso en curso (el agente
// queda Stopped) y no lanza nada más. Con un gate abierto equivale a Reject.
func (r *Runner) Stop() error {
	return r.do(func() error {
		switch r.state {
		case GateWaiting, Ready, NotStarted:
			r.mutate(func() {
				r.gate = nil
				r.state = Stopped
				r.reason = "detenido por el usuario"
			})
		case Running:
			id := r.steps[r.cur].agentID
			r.mutate(func() {
				r.state = Stopped
				r.reason = "detenido por el usuario"
			})
			if id != 0 {
				_ = r.sup.Stop(id)
			}
		default:
			return fmt.Errorf("%w: el pipeline ya terminó (%s)", ErrState, r.state)
		}
		return nil
	})
}

// --- bucle ---

// do ejecuta f en la goroutine del bucle y espera su resultado.
func (r *Runner) do(f func() error) error {
	res := make(chan error, 1)
	select {
	case r.cmds <- func() { res <- f() }:
		return <-res
	case <-r.quit:
		return ErrClosed
	}
}

func (r *Runner) loop() {
	defer close(r.done)
	for {
		select {
		case <-r.quit:
			return
		case <-r.wake:
			r.drain()
		case f := <-r.cmds:
			r.drain()
			f()
		}
	}
}

// observe es el gancho del supervisor: encola lo único que importa (el fin de
// turno y los cambios de estado) y despierta al bucle.
func (r *Runner) observe(u orch.Update) {
	if r.closed.Load() || u.Notice != nil {
		return
	}
	if u.Event != nil {
		if _, ok := u.Event.(agent.Done); !ok {
			return
		}
	}
	r.qmu.Lock()
	r.queue = append(r.queue, u)
	r.qmu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runner) drain() {
	for {
		r.qmu.Lock()
		q := r.queue
		r.queue = nil
		r.qmu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, u := range q {
			r.onUpdate(u)
		}
	}
}

// mutate cambia el estado bajo mu y avisa a quien escuche Changed.
func (r *Runner) mutate(f func()) {
	r.mu.Lock()
	f()
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// onUpdate aplica una novedad del supervisor al paso en curso.
func (r *Runner) onUpdate(u orch.Update) {
	if r.state != Running {
		return
	}
	st := &r.steps[r.cur]
	if st.agentID == 0 || u.ID != st.agentID {
		return
	}
	if d, ok := u.Event.(agent.Done); ok {
		r.mutate(func() {
			st.result = Result{Text: d.Result, IsError: d.IsError, Turns: d.Turns}
			st.hasDone = true
			st.turn = u.Snapshot.Turns
		})
		return
	}
	if u.Event != nil {
		return
	}
	switch snap := u.Snapshot; snap.State {
	case orch.Idle, orch.Failed:
		r.finish(snap)
	case orch.Stopped:
		r.stopWith(fmt.Sprintf("el agente del paso %q se detuvo: %s", st.Name, snap.Reason))
	case orch.BudgetExceeded:
		r.failStep(r.cur, fmt.Sprintf("el agente del paso %q superó su presupuesto: %s", st.Name, snap.Reason))
	}
}

// finish cierra el turno del paso en curso: evalúa su condición y avanza.
func (r *Runner) finish(snap orch.Snapshot) {
	n := r.cur
	st := &r.steps[n]
	if !st.hasDone && snap.State == orch.Failed {
		r.mutate(func() { st.result = Result{Text: snap.Reason, IsError: true} })
	}
	r.mutate(func() { st.turn = snap.Turns })
	cond := st.Condition
	if cond == nil {
		cond = func(res Result) bool { return !res.IsError }
	}
	if !cond(st.result) {
		why := strings.TrimSpace(st.result.Text)
		if why == "" {
			why = "sin texto"
		}
		r.failStep(n, fmt.Sprintf("el paso %q no cumplió su condición: %s", st.Name, truncate(why, 200)))
		return
	}
	r.mutate(func() { st.state = StepDone })
	switch {
	case n == len(r.steps)-1:
		r.mutate(func() { r.state = Done })
	case st.Mode == Auto:
		r.begin(n + 1)
	default:
		r.openGate(n)
	}
}

// openGate deja el pipeline esperando la aprobación para pasar de from al
// siguiente paso.
func (r *Runner) openGate(from int) {
	st := &r.steps[from]
	var edits []orch.Edit
	if st.agentID != 0 {
		for _, e := range r.sup.Edits(st.agentID) {
			if e.Turn == st.turn {
				edits = append(edits, e)
			}
		}
	}
	next := &r.steps[from+1]
	r.mutate(func() {
		r.gateSeq++
		r.gate = &Gate{
			Seq: r.gateSeq, From: from, To: from + 1,
			FromName: st.Name, ToName: next.Name,
			Result: st.result, AgentID: st.agentID, Turn: st.turn, Edits: edits,
			NextPrompt: next.prompt, NextMode: next.Mode,
		}
		r.cur = from
		r.state = GateWaiting
	})
}

// begin pasa al paso n: si es Manual espera a que lo lancen; si no, lo lanza.
func (r *Runner) begin(n int) {
	if r.steps[n].Mode == Manual {
		r.mutate(func() {
			r.cur = n
			r.state = Ready
			r.steps[n].state = StepReady
		})
		return
	}
	r.launch(n)
}

// launch lanza el turno del paso n en su agente.
func (r *Runner) launch(n int) {
	st := &r.steps[n]
	prev := ""
	if n > 0 {
		prev = r.steps[n-1].result.Text
	}
	prompt := strings.ReplaceAll(st.prompt, "{{prev}}", prev)
	name := st.Use
	if st.Agent != nil {
		name = fmt.Sprintf("%d·%s", n+1, st.Name)
	}
	r.mutate(func() {
		r.cur = n
		r.state = Running
		r.reason = ""
		st.state = StepRunning
		st.sent, st.agentName, st.agentID = prompt, name, 0
		st.hasDone, st.result, st.reason, st.turn = false, Result{}, "", 0
	})

	var id orch.ID
	var err error
	if st.Agent != nil {
		if a := st.Agent(); a == nil {
			err = errors.New("la fábrica devolvió un agente nil")
		} else {
			task := r.cfg.Task
			task.Prompt = prompt
			if st.Model != "" {
				task.Model = st.Model
			}
			b := st.Budget
			if b == (orch.Budget{}) {
				b = r.cfg.Budget
			}
			if id = r.sup.Submit(name, a, task, b); id == 0 {
				_ = a.Close()
				err = orch.ErrClosed
			}
		}
	} else if id, err = r.lookup(st.Use); err == nil {
		err = r.sup.Send(id, prompt)
	}
	if err != nil {
		r.failStep(n, fmt.Sprintf("no se pudo lanzar el paso %q: %v", st.Name, err))
		return
	}
	// Las novedades de este agente ya están en la cola, pero se procesan
	// después de esto (el bucle es el único que lanza): el ID ya se conoce.
	r.mutate(func() { st.agentID = id })
}

// lookup busca un agente del supervisor por nombre (el más reciente).
func (r *Runner) lookup(name string) (orch.ID, error) {
	snaps := r.sup.Snapshots()
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Name == name {
			return snaps[i].ID, nil
		}
	}
	return 0, fmt.Errorf("no hay un agente llamado %q", name)
}

func (r *Runner) failStep(n int, reason string) {
	r.mutate(func() {
		r.steps[n].state = StepFailed
		r.steps[n].reason = reason
		r.state = Failed
		r.reason = reason
	})
}

func (r *Runner) stopWith(reason string) {
	r.mutate(func() {
		r.state = Stopped
		r.reason = reason
	})
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
