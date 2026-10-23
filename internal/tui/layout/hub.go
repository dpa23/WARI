package layout

import (
	"context"
	"sync"
	"time"

	"github.com/dpa23/wari/internal/dap"
	"github.com/dpa23/wari/internal/sandbox"
)

// hub reúne lo que el layout lanza fuera de Update y debe cerrarse al salir:
// la ejecución del sandbox y la sesión de depuración. El layout es un valor
// que se copia en cada Update; el hub es el único puntero compartido, y por
// eso Model.Shutdown funciona desde cualquier copia (incluida la inicial,
// que cmd/wari conserva para cerrarlo aunque el programa muera por una señal).
type hub struct {
	// ctx es el contexto de vida larga de los procesos hijos: cancelarlo los
	// termina (red de seguridad tras los Stop ordenados).
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	run    *sandbox.Run
	sess   *dap.Session
	closed bool
}

func newHub() *hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &hub{ctx: ctx, cancel: cancel}
}

// shutdownTimeout acota el disconnect de la sesión al salir.
const shutdownTimeout = 5 * time.Second

// setRun registra la ejecución en curso. Si el hub ya se cerró, la detiene en
// el acto y devuelve false.
func (h *hub) setRun(r *sandbox.Run) bool {
	h.mu.Lock()
	closed := h.closed
	if !closed {
		h.run = r
	}
	h.mu.Unlock()
	if closed {
		_ = r.Stop()
	}
	return !closed
}

// setSession registra la sesión de depuración (misma regla que setRun).
func (h *hub) setSession(s *dap.Session) bool {
	h.mu.Lock()
	closed := h.closed
	if !closed {
		h.sess = s
	}
	h.mu.Unlock()
	if closed {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = s.Stop(ctx)
	}
	return !closed
}

// clearSession olvida la sesión s si sigue siendo la registrada.
func (h *hub) clearSession(s *dap.Session) {
	h.mu.Lock()
	if h.sess == s {
		h.sess = nil
	}
	h.mu.Unlock()
}

// shutdown detiene la sesión y la ejecución, esperando a que terminen sus
// grupos de procesos, y cancela el contexto. Es idempotente.
func (h *hub) shutdown() {
	h.mu.Lock()
	h.closed = true
	run, sess := h.run, h.sess
	h.run, h.sess = nil, nil
	h.mu.Unlock()

	var wg sync.WaitGroup
	if sess != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			_ = sess.Stop(ctx)
		}()
	}
	if run != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = run.Stop()
		}()
	}
	wg.Wait()
	h.cancel()
}

// Shutdown detiene la depuración y la ejecución en el sandbox que sigan en
// marcha, sin dejar procesos huérfanos. Quien crea el layout lo llama al
// salir, por la vía que sea; es idempotente.
func (m Model) Shutdown() {
	m.closeTerms()
	if m.hub != nil {
		m.hub.shutdown()
	}
}
