//go:build windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Stub de Windows: compila y termina al líder, pero todavía no controla a
// los descendientes (pendiente: Job Objects) ni sabe pausar (pendiente:
// validar NtSuspendProcess). Ver docs/decisions.md.

func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// groupAlive: sin Job Objects no hay forma de seguir a los descendientes.
func groupAlive(*Process) bool { return false }

func stopGroup(*Process) error { return errors.ErrUnsupported }

func contGroup(*Process) error { return errors.ErrUnsupported }

// terminateGroup no tiene una señal ordenada equivalente: termina al líder.
func terminateGroup(p *Process) error { return killGroup(p) }

func killGroup(p *Process) error {
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
