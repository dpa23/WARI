//go:build windows

package worktree

// alive es permisivo en Windows: nunca se limpia un worktree ajeno.
func alive(int) bool { return true }
