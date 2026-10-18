package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/orch"
)

// fake es un agent.Agent en memoria: guarda lo que se le pidió y deja emitir
// eventos a mano.
type fake struct {
	mu     sync.Mutex
	events chan agent.Event
	once   sync.Once
	starts []agent.Task
	sends  []string
	pauses int
	last   string // último prompt recibido (Start o Send)
}

func newFake() *fake { return &fake{events: make(chan agent.Event, 64)} }

func (f *fake) Start(_ context.Context, t agent.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, t)
	f.last = t.Prompt
	return nil
}

func (f *fake) Send(msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, msg)
	f.last = msg
	return nil
}

func (f *fake) Events() <-chan agent.Event { return f.events }
func (f *fake) Pause() error               { f.mu.Lock(); f.pauses++; f.mu.Unlock(); return nil }
func (f *fake) Resume() error              { return nil }
func (f *fake) Cancel() error              { return nil }
func (f *fake) Close() error               { f.once.Do(func() { close(f.events) }); return nil }
func (f *fake) Status() agent.Status       { return agent.StatusIdle }

func (f *fake) emit(ev agent.Event) { f.events <- ev }

func (f *fake) prompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func (f *fake) nPauses() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauses
}

// factory crea un fabricante de agentes falsos que los apunta en una lista.
type factory struct {
	mu   sync.Mutex
	made []*fake
}

func (fc *factory) fn() func() agent.Agent {
	return func() agent.Agent {
		f := newFake()
		fc.mu.Lock()
		fc.made = append(fc.made, f)
		fc.mu.Unlock()
		return f
	}
}

func (fc *factory) count() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return len(fc.made)
}

func (fc *factory) at(t *testing.T, i int) *fake {
	t.Helper()
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if i >= len(fc.made) {
		t.Fatalf("solo hay %d agentes creados, se pidió el %d", len(fc.made), i)
	}
	return fc.made[i]
}

func newRunner(t *testing.T, p Pipeline) (*Runner, *orch.Supervisor) {
	t.Helper()
	sup := orch.New(2)
	r, err := New(sup, p, Config{})
	if err != nil {
		sup.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.Close()
		sup.Close()
	})
	return r, sup
}

// waitFor espera a que cond se cumpla sobre el Snapshot.
func waitFor(t *testing.T, r *Runner, what string, cond func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := r.Snapshot()
		if cond(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("no se cumplió %q; estado %s (%s), paso %d", what, s.State, s.Reason, s.Cur)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func waitState(t *testing.T, r *Runner, want State) Snapshot {
	t.Helper()
	return waitFor(t, r, "estado "+want.String(), func(s Snapshot) bool { return s.State == want })
}

// finish cierra el turno del agente con un resultado.
func finish(f *fake, result string) { f.emit(agent.Done{Result: result, Turns: 1}) }

func threeSteps(fc *factory, modes ...Mode) Pipeline {
	names := []string{"plan", "impl", "review"}
	prompts := []string{"planea la tarea", "implementa: {{prev}}", "revisa: {{prev}}"}
	p := Pipeline{Name: "demo"}
	for i, m := range modes {
		s := Step{Name: names[i], Agent: fc.fn(), Prompt: prompts[i], Mode: m}
		if m == Auto {
			s.Budget = orch.Budget{MaxTurns: 3}
		}
		p.Steps = append(p.Steps, s)
	}
	return p
}

func TestThreeStepPipelineStopsAtGatesAndContinuesOnApprove(t *testing.T) {
	fc := &factory{}
	r, _ := newRunner(t, threeSteps(fc, Semi, Semi, Semi))

	if s := r.Snapshot(); s.State != NotStarted || r.Status() != agent.StatusIdle {
		t.Fatalf("estado inicial = %s", s.State)
	}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	waitState(t, r, Running)
	if fc.count() != 1 {
		t.Fatalf("agentes creados = %d, se esperaba 1", fc.count())
	}
	a := fc.at(t, 0)
	if got := a.prompt(); got != "planea la tarea" {
		t.Fatalf("prompt del paso 1 = %q", got)
	}

	// Paso 1 termina tras editar un archivo: el pipeline se detiene solo en el
	// gate, con resumen, y no lanza el paso 2.
	a.emit(agent.FileEditProposed{ToolCallID: "e1", Tool: "Edit", Path: "main.go"})
	finish(a, "PLAN: tocar main.go")
	s := waitState(t, r, GateWaiting)
	if r.Status() != agent.StatusWaitingApproval {
		t.Fatalf("Status = %v", r.Status())
	}
	g := s.Gate
	if g == nil || g.From != 0 || g.To != 1 || g.FromName != "plan" || g.ToName != "impl" {
		t.Fatalf("gate = %+v", g)
	}
	if g.Result.Text != "PLAN: tocar main.go" || g.NextPrompt != "implementa: {{prev}}" {
		t.Fatalf("resumen del gate = %+v", g)
	}
	if len(g.Edits) != 1 || g.Edits[0].Path != "main.go" || g.AgentID == 0 {
		t.Fatalf("ediciones del gate = %+v (agente %d)", g.Edits, g.AgentID)
	}
	time.Sleep(30 * time.Millisecond)
	if fc.count() != 1 {
		t.Fatal("el paso 2 arrancó sin aprobación")
	}

	// Aprobar continúa con {{prev}} sustituido.
	if err := r.Approve(); err != nil {
		t.Fatal(err)
	}
	if fc.count() != 2 {
		t.Fatalf("tras aprobar hay %d agentes, se esperaba 2", fc.count())
	}
	b := fc.at(t, 1)
	if got := b.prompt(); got != "implementa: PLAN: tocar main.go" {
		t.Fatalf("prompt del paso 2 = %q", got)
	}
	s = r.Snapshot()
	if s.State != Running || s.Cur != 1 || s.Gate != nil || s.Steps[0].State != StepDone || s.Steps[1].State != StepRunning {
		t.Fatalf("tras aprobar: %+v", s)
	}

	finish(b, "impl hecha")
	s = waitFor(t, r, "segundo gate", func(s Snapshot) bool { return s.State == GateWaiting && s.Gate.Seq == 2 })
	if s.Gate.From != 1 || s.Gate.ToName != "review" {
		t.Fatalf("gate 2 = %+v", s.Gate)
	}
	if err := r.Approve(); err != nil {
		t.Fatal(err)
	}
	c := fc.at(t, 2)
	if got := c.prompt(); got != "revisa: impl hecha" {
		t.Fatalf("prompt del paso 3 = %q", got)
	}
	finish(c, "todo bien")
	s = waitState(t, r, Done)
	if r.Status() != agent.StatusDone || s.Gate != nil {
		t.Fatalf("final: %+v", s)
	}
	for i, st := range s.Steps {
		if st.State != StepDone {
			t.Fatalf("paso %d: %s", i, st.State)
		}
	}
}

func TestRejectStopsPipeline(t *testing.T) {
	fc := &factory{}
	r, _ := newRunner(t, threeSteps(fc, Semi, Semi, Semi))
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc.at(t, 0), "plan")
	waitState(t, r, GateWaiting)

	if err := r.Reject(); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.State != Stopped || s.Gate != nil || !strings.Contains(s.Reason, "rechazado") {
		t.Fatalf("tras rechazar: %+v", s)
	}
	if err := r.Approve(); !errors.Is(err, ErrState) {
		t.Fatalf("Approve tras rechazar = %v, se esperaba ErrState", err)
	}
	if err := r.Run(); !errors.Is(err, ErrState) {
		t.Fatalf("Run tras rechazar = %v, se esperaba ErrState", err)
	}
	time.Sleep(30 * time.Millisecond)
	if fc.count() != 1 {
		t.Fatalf("agentes creados = %d: rechazar no debe lanzar nada", fc.count())
	}
}

func TestEditPromptAtGate(t *testing.T) {
	fc := &factory{}
	r, _ := newRunner(t, threeSteps(fc, Semi, Semi, Semi))
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc.at(t, 0), "plan A")
	waitState(t, r, GateWaiting)

	if err := r.EditPrompt("  "); err == nil {
		t.Fatal("un prompt vacío debe rechazarse")
	}
	if err := r.EditPrompt("hazlo distinto, base: {{prev}}"); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.State != GateWaiting || s.Gate.NextPrompt != "hazlo distinto, base: {{prev}}" {
		t.Fatalf("el gate debe seguir abierto con el prompt nuevo: %+v", s)
	}
	if err := r.Approve(); err != nil {
		t.Fatal(err)
	}
	if got := fc.at(t, 1).prompt(); got != "hazlo distinto, base: plan A" {
		t.Fatalf("prompt = %q", got)
	}
}

func TestAutoStepPassesWithoutGate(t *testing.T) {
	fc := &factory{}
	r, sup := newRunner(t, threeSteps(fc, Auto, Semi, Semi))
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc.at(t, 0), "salida auto")
	// Sin gate: el paso 2 arranca solo.
	waitFor(t, r, "paso 2 en curso", func(s Snapshot) bool { return s.State == Running && s.Cur == 1 })
	if got := fc.at(t, 1).prompt(); got != "implementa: salida auto" {
		t.Fatalf("prompt = %q", got)
	}
	// El presupuesto del paso Auto se aplicó al agente nuevo.
	snaps := sup.Snapshots()
	if len(snaps) != 2 || snaps[0].Name != "1·plan" {
		t.Fatalf("agentes = %+v", snaps)
	}
	// El Semi del medio sí abre gate.
	finish(fc.at(t, 1), "impl")
	waitState(t, r, GateWaiting)
}

func TestManualStepWaitsForRun(t *testing.T) {
	fc := &factory{}
	r, _ := newRunner(t, threeSteps(fc, Semi, Manual, Semi))
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc.at(t, 0), "plan")
	waitState(t, r, GateWaiting)
	if err := r.Approve(); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.State != Ready || s.Cur != 1 || s.Steps[1].State != StepReady || fc.count() != 1 {
		t.Fatalf("un paso Manual espera a Run: %+v (agentes %d)", s, fc.count())
	}
	if err := r.Approve(); !errors.Is(err, ErrState) {
		t.Fatalf("Approve sin gate = %v", err)
	}
	if err := r.EditPrompt("a mano: {{prev}}"); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if got := fc.at(t, 1).prompt(); got != "a mano: plan" {
		t.Fatalf("prompt = %q", got)
	}
	waitState(t, r, Running)
}

func TestManualFirstStepNeedsRun(t *testing.T) {
	fc := &factory{}
	r, _ := newRunner(t, threeSteps(fc, Manual, Semi))
	time.Sleep(30 * time.Millisecond)
	if fc.count() != 0 {
		t.Fatal("nada debe arrancar antes de Run")
	}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if fc.count() != 1 {
		t.Fatalf("agentes = %d", fc.count())
	}
	if err := r.Run(); !errors.Is(err, ErrState) {
		t.Fatalf("segundo Run = %v", err)
	}
}

func TestFailedStepStopsPipeline(t *testing.T) {
	fc := &factory{}
	r, _ := newRunner(t, threeSteps(fc, Semi, Semi))
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	fc.at(t, 0).emit(agent.Done{Result: "se rompió", IsError: true})
	s := waitState(t, r, Failed)
	if r.Status() != agent.StatusError || s.Steps[0].State != StepFailed || !strings.Contains(s.Reason, "se rompió") {
		t.Fatalf("estado = %+v", s)
	}
	if s.Gate != nil || fc.count() != 1 {
		t.Fatal("un paso fallido no abre gate ni sigue")
	}
}

func TestCustomCondition(t *testing.T) {
	fc := &factory{}
	p := threeSteps(fc, Semi, Semi)
	p.Steps[0].Condition = func(r Result) bool { return strings.Contains(r.Text, "LISTO") }
	r, _ := newRunner(t, p)
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc.at(t, 0), "todavía no")
	s := waitState(t, r, Failed)
	if !strings.Contains(s.Reason, "condición") {
		t.Fatalf("razón = %q", s.Reason)
	}

	fc2 := &factory{}
	p2 := threeSteps(fc2, Semi, Semi)
	p2.Steps[0].Condition = func(r Result) bool { return strings.Contains(r.Text, "LISTO") }
	r2, _ := newRunner(t, p2)
	if err := r2.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc2.at(t, 0), "LISTO")
	waitState(t, r2, GateWaiting)
}

func TestStoppingTheAgentStopsPipeline(t *testing.T) {
	fc := &factory{}
	r, sup := newRunner(t, threeSteps(fc, Semi, Semi))
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	waitState(t, r, Running)
	id := waitFor(t, r, "agente asignado", func(s Snapshot) bool { return s.Steps[0].AgentID != 0 }).Steps[0].AgentID
	if err := sup.Stop(id); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, r, Stopped)
	if !strings.Contains(s.Reason, "detuvo") {
		t.Fatalf("razón = %q", s.Reason)
	}
}

func TestStop(t *testing.T) {
	fc := &factory{}
	r, sup := newRunner(t, threeSteps(fc, Semi, Semi))
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	waitState(t, r, Running)
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); s.State != Stopped {
		t.Fatalf("estado = %s", s.State)
	}
	id := r.Snapshot().Steps[0].AgentID
	deadline := time.Now().Add(3 * time.Second)
	for sup.Snapshots()[id-1].State != orch.Stopped {
		if time.Now().After(deadline) {
			t.Fatal("el agente del paso no se detuvo")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := r.Stop(); !errors.Is(err, ErrState) {
		t.Fatalf("segundo Stop = %v", err)
	}
}

func TestUseExistingAgent(t *testing.T) {
	sup := orch.New(2)
	existing := newFake()
	id := sup.Submit("revisor", existing, agent.Task{Prompt: "hola"}, orch.Budget{})
	existing.emit(agent.Done{Result: "hola!"})

	fc := &factory{}
	p := Pipeline{Steps: []Step{
		{Name: "plan", Agent: fc.fn(), Prompt: "planea", Mode: Semi},
		{Name: "rev", Use: "revisor", Prompt: "revisa: {{prev}}", Mode: Semi},
	}}
	r, err := New(sup, p, Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); sup.Close() })

	// Espera a que el revisor quede Idle (turno de Submit terminado).
	deadline := time.Now().Add(3 * time.Second)
	for sup.Snapshots()[0].State != orch.Idle {
		if time.Now().After(deadline) {
			t.Fatal("el agente existente no quedó Idle")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc.at(t, 0), "el plan")
	waitState(t, r, GateWaiting)
	if err := r.Approve(); err != nil {
		t.Fatal(err)
	}
	if got := existing.prompt(); got != "revisa: el plan" {
		t.Fatalf("prompt al agente existente = %q", got)
	}
	s := r.Snapshot()
	if s.Steps[1].AgentID != id || s.Steps[1].AgentName != "revisor" {
		t.Fatalf("paso 2 = %+v", s.Steps[1])
	}
	finish(existing, "ok")
	waitState(t, r, Done)
}

func TestUseMissingAgentFails(t *testing.T) {
	fc := &factory{}
	p := Pipeline{Steps: []Step{
		{Name: "plan", Agent: fc.fn(), Prompt: "planea", Mode: Semi},
		{Name: "rev", Use: "no existe", Prompt: "revisa", Mode: Semi},
	}}
	r, _ := newRunner(t, p)
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	finish(fc.at(t, 0), "x")
	waitState(t, r, GateWaiting)
	if err := r.Approve(); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.State != Failed || !strings.Contains(s.Reason, "no existe") {
		t.Fatalf("estado = %+v", s)
	}
}

// Un agente de un paso que entra en bucle (misma herramienta con los mismos
// argumentos) se pausa solo por la detección de orch; el pipeline espera y
// sigue al reanudarlo.
func TestLoopingStepAgentPausesAndPipelineWaits(t *testing.T) {
	fc := &factory{}
	r, sup := newRunner(t, threeSteps(fc, Semi, Semi))
	col := make(chan orch.Update, 4096)
	go func() {
		for u := range sup.Updates() {
			col <- u
		}
		close(col)
	}()
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	a := fc.at(t, 0)
	for range 3 {
		a.emit(agent.ToolCall{ID: "t", Name: "Bash", Input: json.RawMessage(`{"command":"make"}`)})
	}
	id := waitFor(t, r, "agente asignado", func(s Snapshot) bool { return s.Steps[0].AgentID != 0 }).Steps[0].AgentID

	var notice *orch.Notice
	deadline := time.After(3 * time.Second)
	for notice == nil {
		select {
		case u := <-col:
			if u.Notice != nil && u.Notice.Kind == orch.NoticeLoop {
				notice = u.Notice
			}
		case <-deadline:
			t.Fatal("sin aviso de bucle")
		}
	}
	if notice.Agent != id || notice.Loop != orch.LoopTool {
		t.Fatalf("aviso = %+v", notice)
	}
	for sup.Snapshots()[id-1].State != orch.Paused {
		select {
		case <-deadline:
			t.Fatal("el agente en bucle no se pausó")
		case <-time.After(2 * time.Millisecond):
		}
	}
	if a.nPauses() != 1 || r.Snapshot().State != Running {
		t.Fatalf("pauses=%d, pipeline=%s", a.nPauses(), r.Snapshot().State)
	}
	if err := sup.Resume(id); err != nil {
		t.Fatal(err)
	}
	finish(a, "listo")
	waitState(t, r, GateWaiting)
}

func TestChangedNotifiesAndClosesOnClose(t *testing.T) {
	fc := &factory{}
	sup := orch.New(2)
	defer sup.Close()
	r, err := New(sup, threeSteps(fc, Semi, Semi), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Changed():
	case <-time.After(3 * time.Second):
		t.Fatal("Changed no avisó")
	}
	r.Close()
	r.Close() // idempotente
	for range r.Changed() {
		// vacía lo pendiente hasta que el canal se cierre
	}
	if err := r.Run(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Run tras Close = %v", err)
	}
}

func TestValidate(t *testing.T) {
	ok := func() func() agent.Agent { return func() agent.Agent { return newFake() } }
	cases := []struct {
		name string
		p    Pipeline
		want string
	}{
		{"sin pasos", Pipeline{}, "sin pasos"},
		{"sin nombre", Pipeline{Steps: []Step{{Agent: ok(), Prompt: "x"}}}, "no tiene nombre"},
		{"repetido", Pipeline{Steps: []Step{
			{Name: "a", Agent: ok(), Prompt: "x"}, {Name: "a", Agent: ok(), Prompt: "x"},
		}}, "repetido"},
		{"sin prompt", Pipeline{Steps: []Step{{Name: "a", Agent: ok()}}}, "sin prompt"},
		{"sin agente", Pipeline{Steps: []Step{{Name: "a", Prompt: "x"}}}, "falta el agente"},
		{"ambos", Pipeline{Steps: []Step{{Name: "a", Agent: ok(), Use: "b", Prompt: "x"}}}, "excluyentes"},
		{"auto sin presupuesto", Pipeline{Steps: []Step{{Name: "a", Agent: ok(), Prompt: "x", Mode: Auto}}}, "Auto exige presupuesto"},
		{"auto con existente", Pipeline{Steps: []Step{
			{Name: "a", Use: "b", Prompt: "x", Mode: Auto, Budget: orch.Budget{MaxTurns: 1}},
		}}, "agente nuevo"},
		{"modo inválido", Pipeline{Steps: []Step{{Name: "a", Agent: ok(), Prompt: "x", Mode: 9}}}, "modo inválido"},
	}
	for _, c := range cases {
		err := c.p.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, se esperaba %q", c.name, err, c.want)
		}
		sup := orch.New(1)
		if r, err := New(sup, c.p, Config{}); err == nil {
			r.Close()
			t.Errorf("%s: New debe validar", c.name)
		}
		sup.Close()
	}
	good := Pipeline{Steps: []Step{
		{Name: "a", Agent: ok(), Prompt: "x", Mode: Auto, Budget: orch.Budget{MaxDuration: time.Minute}},
	}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}
