// Package term aloja una CLI interactiva de verdad en un terminal virtual: el
// proceso corre en un PTY (internal/proc, en su propio grupo) y un emulador de
// terminal (charmbracelet/x/vt) mantiene la pantalla que dibuja. El panel que
// la muestre solo pide la pantalla y le reenvía teclas, pegado y tamaño; todo
// lo demás (comandos "/", permisos, skills, modelo) es de la propia CLI.
package term

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"

	"github.com/dpa23/wari/internal/proc"
)

// notifyEvery agrupa los avisos de pantalla cambiada: la CLI puede escribir
// cientos de veces por segundo y la interfaz no necesita redibujar tanto.
const notifyEvery = 16 * time.Millisecond

// Pane es una CLI corriendo en un terminal virtual.
type Pane struct {
	em   *vt.SafeEmulator
	proc *proc.Process

	mu        sync.Mutex
	cols      int
	rows      int
	notify    func()
	notifying bool
	// Lo que el emulador avisa por sus callbacks (se guarda con mu).
	curX, curY int
	curHidden  bool
	title      string
	altScreen  bool

	done chan struct{}
}

// Start lanza spec (sin PTY: se pone aquí) con el tamaño dado. notify se llama,
// agrupado, cada vez que la pantalla cambia; puede ser nil.
func Start(ctx context.Context, spec proc.Spec, cols, rows int, notify func()) (*Pane, error) {
	cols, rows = max(cols, 1), max(rows, 1)
	spec.PTY = &proc.PTYSize{Cols: uint16(cols), Rows: uint16(rows)}
	p, err := proc.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	t := &Pane{em: vt.NewSafeEmulator(cols, rows), proc: p, cols: cols, rows: rows, notify: notify, done: make(chan struct{})}
	t.em.SetCallbacks(vt.Callbacks{
		CursorPosition:   func(_, pos uv.Position) { t.set(func() { t.curX, t.curY = pos.X, pos.Y }) },
		CursorVisibility: func(visible bool) { t.set(func() { t.curHidden = !visible }) },
		Title:            func(s string) { t.set(func() { t.title = s }) },
		AltScreen:        func(on bool) { t.set(func() { t.altScreen = on }) },
	})
	// Lo que la CLI dibuja entra al emulador.
	go func() {
		defer close(t.done)
		buf := make([]byte, 32<<10)
		for {
			n, err := p.Stdout().Read(buf)
			if n > 0 {
				t.em.Write(buf[:n])
				t.changed()
			}
			if err != nil {
				return
			}
		}
	}()
	// Lo que el emulador responde a las consultas de la CLI (posición del
	// cursor, atributos del terminal) y la codificación de las teclas vuelven
	// al PTY.
	go func() {
		io.Copy(p.Stdin(), t.em)
	}()
	return t, nil
}

func (t *Pane) set(f func()) {
	t.mu.Lock()
	f()
	t.mu.Unlock()
}

// changed avisa de un cambio de pantalla, como mucho una vez por notifyEvery.
func (t *Pane) changed() {
	if t.notify == nil {
		return
	}
	t.mu.Lock()
	if t.notifying {
		t.mu.Unlock()
		return
	}
	t.notifying = true
	t.mu.Unlock()
	time.AfterFunc(notifyEvery, func() {
		t.mu.Lock()
		t.notifying = false
		t.mu.Unlock()
		t.notify()
	})
}

// Process es el proceso de la CLI, para pausarlo, reanudarlo o matarlo con su
// grupo.
func (t *Pane) Process() *proc.Process { return t.proc }

// Done se cierra cuando la CLI dejó de escribir (terminó).
func (t *Pane) Done() <-chan struct{} { return t.done }

// Resize cambia el tamaño del terminal: la CLI recibe SIGWINCH y redibuja.
func (t *Pane) Resize(cols, rows int) {
	cols, rows = max(cols, 1), max(rows, 1)
	t.mu.Lock()
	same := cols == t.cols && rows == t.rows
	t.cols, t.rows = cols, rows
	t.mu.Unlock()
	if same {
		return
	}
	t.em.Resize(cols, rows)
	t.proc.Resize(uint16(cols), uint16(rows))
}

// Size devuelve el tamaño actual en celdas.
func (t *Pane) Size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows
}

// SendKey reenvía una tecla a la CLI.
func (t *Pane) SendKey(k tea.KeyPressMsg) {
	t.em.SendKey(uv.KeyPressEvent(uv.Key{
		Text: k.Text, Mod: uv.KeyMod(k.Mod), Code: k.Code,
		ShiftedCode: k.ShiftedCode, BaseCode: k.BaseCode, IsRepeat: k.IsRepeat,
	}))
}

// Paste reenvía un pegado (con las marcas de pegado si la CLI las pidió).
func (t *Pane) Paste(text string) { t.em.Paste(text) }

// SendText escribe texto tal cual, como si se tecleara. Sirve para mandarle un
// mensaje desde fuera (otro agente, el orquestador).
func (t *Pane) SendText(text string) { t.em.SendText(text) }

// Lines devuelve la pantalla actual como filas con su color y estilo (ANSI),
// exactamente rows filas de hasta cols celdas.
func (t *Pane) Lines() []string {
	s := t.em.Render()
	lines := strings.Split(s, "\n")
	_, rows := t.Size()
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return lines[:rows]
}

// Cursor devuelve la posición del cursor del terminal virtual y si se ve: las
// CLIs de pantalla completa lo ocultan y dibujan el suyo.
func (t *Pane) Cursor() (x, y int, visible bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.curX, t.curY, !t.curHidden
}

// Title es el título que la CLI puso al terminal (OSC 0/2). Las CLIs lo usan
// para mostrar si están trabajando.
func (t *Pane) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// AltScreen indica si la CLI usa la pantalla alternativa (interfaz completa).
func (t *Pane) AltScreen() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.altScreen
}

// Close mata la CLI con todo su grupo y libera el terminal.
func (t *Pane) Close() error {
	err := t.proc.Kill()
	t.proc.Close()
	// Emulator.Close escribe un campo que Read lee sin sincronizar (una carrera
	// dentro de x/vt v0.1.0): se cierra la tubería de entrada, que también
	// desbloquea la lectura que reenvía al PTY.
	if c, ok := t.em.InputPipe().(io.Closer); ok {
		c.Close()
	}
	return err
}
