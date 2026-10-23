//go:build windows

package worktree

import "math"

// freeBytes es permisivo en Windows: no se comprueba el espacio libre.
func freeBytes(string) (uint64, error) { return math.MaxUint64, nil }
