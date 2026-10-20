package tui

import "testing"

func TestRelPathsQuitaLaRaiz(t *testing.T) {
	m := &transcript{root: "/home/u/WARI"}
	got := m.relPaths("[README.md](/home/u/WARI/README.md) y file:///home/u/WARI/a/b.go#L3 y /otro/x")
	want := "[README.md](README.md) y a/b.go#L3 y /otro/x"
	if got != want {
		t.Errorf("relPaths = %q, quiero %q", got, want)
	}
	if got := (&transcript{}).relPaths("/home/u/WARI/x"); got != "/home/u/WARI/x" {
		t.Errorf("sin raíz no cambia: %q", got)
	}
}
