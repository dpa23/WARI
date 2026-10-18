//go:build unix

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain permite que el binario de test haga de `wari`: con WARI_AS_MAIN
// ejecuta main() con los argumentos de WARI_ARGS (separados por espacios).
func TestMain(m *testing.M) {
	if os.Getenv("WARI_AS_MAIN") == "1" {
		os.Args = append([]string{"wari"}, strings.Fields(os.Getenv("WARI_ARGS"))...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Si stdout se rompe en modo -prompt (por ejemplo `wari -prompt x | head -1`),
// Go mata el proceso con SIGPIPE sin limpiar y el agente quedaba huérfano.
func TestPromptModeBrokenStdoutLeavesNoOrphans(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	fixture, err := filepath.Abs("../../testdata/claude/simple.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// claude falso: anota su PID, espera a que stdout de wari esté cerrado,
	// vuelca una respuesta y se queda vivo.
	script := "#!/bin/sh\necho $$ > '" + pidFile + "'\nsleep 1\ncat '" + fixture + "'\nsleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "WARI_AS_MAIN=1", "WARI_ARGS=-prompt hola", "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-waited
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(-pid, syscall.SIGKILL)
			}
		}
	})

	out.Close() // el lector de stdout desaparece, como `| head -1`

	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("wari no terminó con stdout cerrado")
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("el claude falso no llegó a arrancar: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("el grupo del agente (%d) sigue vivo tras morir wari", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
