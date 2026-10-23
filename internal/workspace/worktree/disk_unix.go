//go:build unix

package worktree

import "syscall"

// freeBytes devuelve el espacio libre, para un usuario normal, del volumen
// que contiene path.
func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
