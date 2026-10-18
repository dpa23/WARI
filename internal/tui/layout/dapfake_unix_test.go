//go:build unix

package layout

import (
	"errors"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// assertNoOrphans comprueba que el grupo de procesos ya no tiene miembros (un
// descendiente huérfano lo mantendría vivo).
func assertNoOrphans(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("quedan procesos vivos en el grupo %d", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDebugRealProcessStopLeavesNoOrphans lanza el adaptador falso como
// proceso de verdad, con un descendiente en su grupo, y comprueba que cerrar
// la sesión (por la tecla, por salir y por Shutdown) termina el grupo entero.
func TestDebugRealProcessStopLeavesNoOrphans(t *testing.T) {
	installAdapter(t)
	for _, how := range []string{"debug_stop", "quit", "shutdown"} {
		t.Run(how, func(t *testing.T) {
			root, main, helper := dbgProject(t, launchOK)
			l := &realLauncher{main: main, helper: helper}
			d := dbgLayout(t, root, main, DebugConfig{Launcher: l.launch})
			d.send(press(tea.KeyF9, 0), press(tea.KeyF5, 0))
			d.until("parada", stoppedWithTree)
			l.mu.Lock()
			pgid := l.p.PGID()
			l.mu.Unlock()
			if pgid <= 0 {
				t.Fatalf("pgid = %d", pgid)
			}
			if err := syscall.Kill(-pgid, 0); err != nil {
				t.Fatalf("el grupo del adaptador debería estar vivo: %v", err)
			}
			switch how {
			case "debug_stop":
				d.send(press(tea.KeyF5, tea.ModShift))
				d.until("sesión cerrada", func(m Model) bool { return m.dbg.sess == nil })
			case "quit":
				d.send(press('c', tea.ModCtrl))
			case "shutdown":
				d.m.Shutdown()
			}
			assertNoOrphans(t, pgid)
		})
	}
}
