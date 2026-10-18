//go:build unix

package proc

import (
	"errors"
	"os/exec"
	"runtime"
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

// groupAlive indica si el grupo todavía tiene miembros a los que señalar.
// La señal 0 solo comprueba existencia.
func groupAlive(p *Process) bool {
	return !noSignalableMembers(syscall.Kill(-p.PGID(), 0), runtime.GOOS)
}

// noSignalableMembers dice si el error de kill(-pgid, ...) significa que el
// grupo ya no tiene procesos a los que señalar. ESRCH es el caso general. En
// macOS, un grupo cuyos únicos miembros son zombis responde EPERM en vez de
// ESRCH: como el grupo es nuestro, EPERM solo puede venir de ahí, y no hay
// nada que matar. En Linux EPERM sí es un permiso denegado real.
func noSignalableMembers(err error, goos string) bool {
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	return goos == "darwin" && errors.Is(err, syscall.EPERM)
}

func signalGroup(p *Process, sig syscall.Signal) error {
	err := syscall.Kill(-p.PGID(), sig)
	if noSignalableMembers(err, runtime.GOOS) {
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
