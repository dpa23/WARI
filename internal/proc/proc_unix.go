//go:build unix

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

// setGroup hace que el hijo sea líder de un grupo nuevo, con PGID = PID.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopGroup(p *Process) error {
	return signalGroup(p, syscall.SIGSTOP)
}

func contGroup(p *Process) error {
	return signalGroup(p, syscall.SIGCONT)
}

// terminateGroup pide la terminación ordenada. Un proceso detenido no
// atiende SIGTERM hasta continuar, por eso va seguido de SIGCONT.
func terminateGroup(p *Process) error {
	if err := signalGroupIfAny(p, syscall.SIGTERM); err != nil {
		return err
	}
	return signalGroupIfAny(p, syscall.SIGCONT)
}

func killGroup(p *Process) error {
	return signalGroupIfAny(p, syscall.SIGKILL)
}

// groupAlive indica si el grupo todavía tiene miembros (incluidos zombis).
// La señal 0 solo comprueba existencia.
func groupAlive(p *Process) bool {
	return !errors.Is(syscall.Kill(-p.PGID(), 0), syscall.ESRCH)
}

func signalGroup(p *Process, sig syscall.Signal) error {
	err := syscall.Kill(-p.PGID(), sig)
	if errors.Is(err, syscall.ESRCH) {
		return ErrNotRunning
	}
	return err
}

// signalGroupIfAny es como signalGroup, pero un grupo vacío no es error.
func signalGroupIfAny(p *Process, sig syscall.Signal) error {
	if err := signalGroup(p, sig); err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	return nil
}
