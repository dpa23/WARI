//go:build linux

package layout

import (
	"regexp"
	"syscall"
	"testing"
)

func TestLSPStatusShowsMemory(t *testing.T) {
	f := newFakeLSP(t)
	f.pgid = syscall.Getpgrp() // el grupo del propio test: tiene memoria residente
	s := lspSetup(t, true, f, true)
	s.ready()
	re := regexp.MustCompile(`LSP jdtls · \d+(\.\d)? (MB|GB)`)
	s.until("memoria en la barra", func(m Model) bool { return re.MatchString(m.lspInfo()) })
	if bar := plain(lines(s.m)[s.m.h-1]); !re.MatchString(bar) {
		t.Errorf("barra: %q", bar)
	}
}
