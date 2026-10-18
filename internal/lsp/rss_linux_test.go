//go:build linux

package lsp

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestParseVmRSS(t *testing.T) {
	status := "Name:\tjava\nVmPeak:\t  999 kB\nVmRSS:\t  2048 kB\nThreads:\t3\n"
	if got := parseVmRSS([]byte(status)); got != 2048<<10 {
		t.Errorf("got %d", got)
	}
	if got := parseVmRSS([]byte("Name:\tkthreadd\n")); got != 0 {
		t.Errorf("hilo del kernel: %d", got)
	}
	if got := parseVmRSS([]byte("VmRSS:\tabc kB\n")); got != 0 {
		t.Errorf("ilegible: %d", got)
	}
	if got := parseVmRSS(nil); got != 0 {
		t.Errorf("vacío: %d", got)
	}
}

func TestParsePgrp(t *testing.T) {
	tests := []struct {
		stat string
		want int
	}{
		{"1234 (bash) S 1 777 777 0 -1", 777},
		{"42 (a (b) c) R 1 99 99 0", 99}, // paréntesis dentro del nombre
		{"7 (x)", -1},
		{"sin paréntesis", -1},
		{"5 (x) S 1 notnum 3", -1},
	}
	for _, tt := range tests {
		if got := parsePgrp([]byte(tt.stat)); got != tt.want {
			t.Errorf("%q -> %d, quería %d", tt.stat, got, tt.want)
		}
	}
}

func TestGroupRSSOfOwnGroup(t *testing.T) {
	rss, err := groupRSS("/proc", syscall.Getpgrp())
	if err != nil {
		t.Fatal(err)
	}
	if rss <= 0 {
		t.Errorf("el grupo del test debía tener memoria residente: %d", rss)
	}
	own, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	if rss < parseVmRSS(own)/2 { // la suma incluye a este proceso (margen por variación)
		t.Errorf("suma %d menor que el propio %d", rss, parseVmRSS(own))
	}
}

func TestGroupRSSEmptyGroup(t *testing.T) {
	if _, err := groupRSS("/proc", 1<<30); err != ErrNoProcess {
		t.Errorf("err=%v", err)
	}
	if _, err := groupRSS("/no/existe", 1); err == nil {
		t.Error("quería error")
	}
}

func TestMemoryRSSOfRealGroup(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	c, _ := newPair(t, Options{}, defaultHandle(nil))
	c.proc = &fakeProc{pgid: cmd.Process.Pid, done: make(chan struct{})}
	n, err := c.MemoryRSS()
	if err != nil || n <= 0 {
		t.Errorf("n=%d err=%v", n, err)
	}
}
