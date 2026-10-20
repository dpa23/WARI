package orch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitShowHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está en el PATH")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("uno\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-q", "-m", "x")

	got, err := gitShowHead(context.Background(), dir, "a.txt")
	if err != nil || string(got) != "uno\n" {
		t.Fatalf("a.txt en HEAD = %q, %v", got, err)
	}
	// Un archivo nuevo no está en HEAD: nil sin error.
	if err := os.WriteFile(filepath.Join(dir, "nuevo.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = gitShowHead(context.Background(), dir, "nuevo.txt")
	if err != nil || got != nil {
		t.Fatalf("archivo nuevo = %q, %v", got, err)
	}
	// Fuera de un repo: ErrNoGit.
	if _, err := gitShowHead(context.Background(), t.TempDir(), "a.txt"); err == nil {
		t.Error("sin repo debía fallar")
	}
	if cur, err := readInside(dir, "a.txt"); err != nil || string(cur) != "uno\n" {
		t.Errorf("readInside = %q, %v", cur, err)
	}
	if _, err := readInside(dir, "../x"); err == nil {
		t.Error("readInside no debe salir de la raíz")
	}
}
