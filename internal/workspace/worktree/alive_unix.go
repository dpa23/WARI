//go:build unix

package worktree

import (
	"errors"
	"syscall"
)

// alive indica que el proceso pid existe (aunque no sea nuestro).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
