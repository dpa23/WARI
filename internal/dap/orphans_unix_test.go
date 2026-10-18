//go:build unix

package dap

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// assertNoOrphans comprueba que el grupo de procesos ya no tiene miembros
// (un descendiente huérfano lo mantendría vivo).
func assertNoOrphans(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
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
