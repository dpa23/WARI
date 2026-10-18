package editorview

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDebugMarksGutter(t *testing.T) {
	m, _ := newFile(t, "A.java", "uno\ndos\ntres\ncuatro\n", 30, 6)
	before := stripSGR(m.View())
	rows := func() []string { return strings.Split(m.View(), "\n") }

	m.SetBreakpoints(map[int]BreakpointMark{0: BreakpointVerified, 1: BreakpointPending})
	r := rows()
	if !strings.Contains(r[0], sgrBreakGlyph+glyphBreak) || !strings.Contains(r[1], sgrBreakWait+glyphBreakWait) {
		t.Fatalf("faltan los breakpoints:\n%q\n%q", r[0], r[1])
	}
	if strings.ContainsAny(stripSGR(r[2]), "◉○▶") {
		t.Fatalf("línea sin breakpoint marcada: %q", r[2])
	}

	// La línea actual gana al breakpoint y se resalta entera, sin mover el texto.
	m.SetExecLine(0)
	r = rows()
	if !strings.Contains(r[0], sgrExecGlyph+glyphExec) || strings.Contains(r[0], glyphBreak) {
		t.Fatalf("la flecha debe ganar al breakpoint: %q", r[0])
	}
	if !strings.Contains(r[0], sgrExecLineBg) || strings.Contains(r[1], sgrExecLineBg) {
		t.Fatalf("solo la línea actual lleva fondo:\n%q\n%q", r[0], r[1])
	}
	if m.ExecLine() != 0 {
		t.Fatalf("ExecLine = %d", m.ExecLine())
	}
	for i, l := range rows() {
		if w := lipgloss.Width(l); w != 30 {
			t.Fatalf("fila %d mide %d celdas, quería 30: %q", i, w, l)
		}
	}
	norm := strings.NewReplacer("▶", " ", "◉", " ", "○", " ").Replace(stripSGR(m.View()))
	if norm != before {
		t.Fatalf("las marcas mueven el texto:\n%q\n%q", norm, before)
	}

	// Una línea vacía (el fondo debe cubrir su ancho) y una línea sin texto visible.
	m2, _ := newFile(t, "B.java", "\nx\n", 20, 3)
	m2.SetExecLine(0)
	first := strings.Split(m2.View(), "\n")[0]
	if lipgloss.Width(first) != 20 || !strings.Contains(first, sgrExecLineBg) {
		t.Fatalf("línea vacía resaltada: %q", first)
	}

	m.SetExecLine(-1)
	m.SetBreakpoints(nil)
	if stripSGR(m.View()) != before || strings.Contains(m.View(), sgrExecLineBg) || m.ExecLine() != -1 {
		t.Fatal("sin marcas debe verse como antes")
	}
}

func TestDebugMarksBeatDiagnostics(t *testing.T) {
	m, _ := newFile(t, "A.java", "uno\ndos\n", 30, 4)
	m.SetLineMarks(map[int]LineMark{0: MarkError, 1: MarkError})
	m.SetBreakpoints(map[int]BreakpointMark{0: BreakpointVerified})
	r := strings.Split(m.View(), "\n")
	if !strings.Contains(r[0], glyphBreak) || strings.Contains(r[0], "●") {
		t.Fatalf("el breakpoint gana al diagnóstico: %q", r[0])
	}
	if !strings.Contains(r[1], "●") {
		t.Fatalf("el diagnóstico sigue visible donde no hay breakpoint: %q", r[1])
	}
}
