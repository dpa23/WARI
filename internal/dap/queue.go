package dap

import "sync"

// DefaultEventBuffer es cuántos eventos sin leer se retienen por defecto.
const DefaultEventBuffer = 1024

// eventQueue entrega eventos al consumidor sin bloquear nunca al productor
// (la goroutine de lectura del cliente).
//
// Política de desbordamiento: la cola retiene como máximo max eventos sin
// leer. Si llega uno nuevo con la cola llena, se descarta el evento más
// antiguo de tipo output (la salida del depurado es lo más prescindible y lo
// más abundante); si no queda ninguno, el más antiguo de cualquier tipo. Así
// los eventos de control (stopped, terminated...) sobreviven mientras haya
// salida que sacrificar. Los descartes se cuentan en dropped.
type eventQueue struct {
	max int

	mu      sync.Mutex
	q       []Event
	dropped int
	closed  bool // el productor terminó

	wake chan struct{} // capacidad 1
	out  chan Event
	quit chan struct{}
}

func newEventQueue(max int) *eventQueue {
	if max <= 0 {
		max = DefaultEventBuffer
	}
	q := &eventQueue{
		max:  max,
		wake: make(chan struct{}, 1),
		out:  make(chan Event),
		quit: make(chan struct{}),
	}
	go q.pump()
	return q
}

// push encola un evento; nunca bloquea.
func (q *eventQueue) push(e Event) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if len(q.q) >= q.max {
		q.dropLocked()
	}
	q.q = append(q.q, e)
	q.mu.Unlock()
	q.signal()
}

func (q *eventQueue) dropLocked() {
	idx := 0
	for i, e := range q.q {
		if _, ok := e.(OutputEvent); ok {
			idx = i
			break
		}
	}
	copy(q.q[idx:], q.q[idx+1:])
	q.q[len(q.q)-1] = nil
	q.q = q.q[:len(q.q)-1]
	q.dropped++
}

// finish indica que no llegarán más eventos; el canal se cierra cuando se
// hayan entregado los pendientes (o al llamar a stop).
func (q *eventQueue) finish() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

// stop abandona los eventos pendientes y termina la goroutine de entrega.
func (q *eventQueue) stop() {
	select {
	case <-q.quit:
	default:
		close(q.quit)
	}
}

func (q *eventQueue) droppedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

func (q *eventQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *eventQueue) pump() {
	defer close(q.out)
	for {
		q.mu.Lock()
		if len(q.q) > 0 {
			e := q.q[0]
			q.q[0] = nil
			q.q = q.q[1:]
			q.mu.Unlock()
			select {
			case q.out <- e:
			case <-q.quit:
				return
			}
			continue
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-q.wake:
		case <-q.quit:
			return
		}
	}
}
