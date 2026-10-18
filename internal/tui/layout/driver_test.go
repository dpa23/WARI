package layout

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// driver es un bucle de eventos mínimo para los tests de ejecución y
// depuración: ejecuta los Cmd en goroutines, como bubbletea, y entrega sus
// mensajes a Update en la goroutine del test.
type driver struct {
	t  *testing.T
	m  Model
	ch chan tea.Msg
}

func newDriver(t *testing.T, m Model) *driver {
	t.Helper()
	t.Cleanup(m.Shutdown)
	return &driver{t: t, m: m, ch: make(chan tea.Msg, 1024)}
}

func (d *driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		d.deliver(cmd())
	}()
}

func (d *driver) deliver(msg tea.Msg) {
	switch v := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, c := range v {
			d.run(c)
		}
		return
	}
	select {
	case d.ch <- msg:
	case <-d.t.Context().Done():
	}
}

// send aplica los mensajes en orden y lanza los Cmd resultantes.
func (d *driver) send(msgs ...tea.Msg) {
	d.t.Helper()
	for _, msg := range msgs {
		m, cmd := d.m.update(msg)
		d.m = m
		d.run(cmd)
	}
}

// until procesa mensajes hasta que cond se cumple o vence el plazo.
func (d *driver) until(what string, cond func(Model) bool) {
	d.t.Helper()
	deadline := time.After(15 * time.Second)
	for !cond(d.m) {
		select {
		case msg := <-d.ch:
			d.send(msg)
		case <-deadline:
			d.t.Fatalf("plazo vencido esperando: %s\npantalla:\n%s\nsalida: %v", what, screen(d.m), d.m.out)
		}
	}
}

// drain procesa los mensajes que ya llegaron o lleguen en d.
func (d *driver) drain(d0 time.Duration) {
	d.t.Helper()
	end := time.After(d0)
	for {
		select {
		case msg := <-d.ch:
			d.send(msg)
		case <-end:
			return
		}
	}
}
