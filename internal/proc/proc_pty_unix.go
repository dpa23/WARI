//go:build unix

package proc

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/creack/pty"
)

// startPTY lanza spec en un terminal virtual. pty.Start crea una sesión nueva
// (Setsid) con el hijo de líder, así que su PGID es su PID, como en el modo
// normal: Pause, Resume y Kill señalan al grupo completo.
func startPTY(ctx context.Context, spec Spec) (*Process, error) {
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: spec.PTY.Cols, Rows: spec.PTY.Rows})
	if err != nil {
		return nil, fmt.Errorf("proc: lanzar %s en un PTY: %w", spec.Path, err)
	}
	grace := spec.KillGrace
	if grace <= 0 {
		grace = DefaultKillGrace
	}
	p := &Process{
		cmd:    cmd,
		stdin:  ptmx,
		stdout: ptmx,
		pty:    ptmx,
		tail:   &tailBuffer{max: stderrTail},
		grace:  grace,
		done:   make(chan struct{}),
	}
	go func() {
		p.waitErr = cmd.Wait()
		cleanupGroup(p)
		close(p.done)
	}()
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				p.Kill()
			case <-p.done:
			}
		}()
	}
	return p, nil
}

// Resize cambia el tamaño del terminal virtual (el programa recibe SIGWINCH).
// Solo vale para procesos lanzados con Spec.PTY.
func (p *Process) Resize(cols, rows uint16) error {
	if p.pty == nil {
		return ErrNoPTY
	}
	return pty.Setsize(p.pty, &pty.Winsize{Cols: cols, Rows: rows})
}
