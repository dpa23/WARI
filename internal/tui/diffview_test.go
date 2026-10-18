package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// twoHunkDiff cambia las líneas 3 y 35 de un archivo de 40.
func twoHunkDiff() string {
	old := lines(40)
	cur := strings.Replace(old, "linea 3\n", "TRES\n", 1)
	cur = strings.Replace(cur, "linea 35\n", "TREINTA Y CINCO\n", 1)
	return unifiedDiff("a/x", "b/x", []byte(old), []byte(cur))
}

func newTestDiffView(text string, w, h int) *diffView {
	d := &diffView{numW: 1}
	d.setText(text)
	d.setArea(w, h)
	return d
}

func plainLines(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = ansiRe.ReplaceAllString(l, "")
	}
	return out
}

func TestParseDiffLineNumbers(t *testing.T) {
	rows, hunks := parseDiff("--- a\n+++ b\n@@ -2,3 +2,3 @@\n uno\n-dos\n+DOS\n tres\n")
	if len(hunks) != 1 || hunks[0] != 2 {
		t.Fatalf("hunks = %v", hunks)
	}
	want := []struct {
		op       byte
		old, new int
	}{{'h', 0, 0}, {'h', 0, 0}, {'@', 0, 0}, {' ', 2, 2}, {'-', 3, 0}, {'+', 0, 3}, {' ', 4, 4}}
	if len(rows) != len(want) {
		t.Fatalf("filas = %d", len(rows))
	}
	for i, w := range want {
		if rows[i].op != w.op || rows[i].old != w.old || rows[i].new != w.new {
			t.Errorf("fila %d = %+v, quería %+v", i, rows[i], w)
		}
	}
}

func TestParseDiffPlainMessage(t *testing.T) {
	rows, hunks := parseDiff("(sin cambios respecto al checkpoint)")
	if len(hunks) != 0 || len(rows) != 1 || rows[0].op != 'm' {
		t.Fatalf("rows=%v hunks=%v", rows, hunks)
	}
}

func TestDiffViewHunkNavigation(t *testing.T) {
	d := newTestDiffView(twoHunkDiff(), 60, 12)
	if len(d.hunks) != 2 {
		t.Fatalf("hunks = %v", d.hunks)
	}
	d.gotoHunk(d.cur + 1)
	if d.cur != 1 || d.yoff != min(d.hunks[1], d.maxY()) {
		t.Fatalf("cur=%d yoff=%d", d.cur, d.yoff)
	}
	d.gotoHunk(d.cur + 1) // no pasa del último
	if d.cur != 1 {
		t.Fatalf("cur = %d", d.cur)
	}
	d.gotoHunk(d.cur - 1)
	if d.cur != 0 || d.yoff != d.hunks[0] {
		t.Fatalf("cur=%d yoff=%d", d.cur, d.yoff)
	}
	d.scroll(1000, 0) // al final: el hunk actual pasa a ser el último visible
	if d.yoff != d.maxY() || d.cur != 1 {
		t.Fatalf("yoff=%d cur=%d", d.yoff, d.cur)
	}
}

func TestDiffViewRendersLineNumbersBothSides(t *testing.T) {
	d := newTestDiffView("--- a\n+++ b\n@@ -9,3 +9,3 @@\n uno\n-dos\n+DOS\n tres\n", 60, 10)
	out := strings.Join(plainLines(d.lines(defaultStyles(), DefaultKeyMap(), "", "", 60, 10)), "\n")
	for _, want := range []string{" 9  9  uno", "10    -dos", "   10 +DOS", "11 11  tres"} {
		if !strings.Contains(out, want) {
			t.Errorf("falta %q en:\n%s", want, out)
		}
	}
}

func TestDiffViewNeverOverflows(t *testing.T) {
	long := strings.Repeat("ancho ", 40)
	text := unifiedDiff("a/x", "b/x", []byte(lines(30)), []byte(strings.Replace(lines(30), "linea 5\n", "\tTAB\x1b[31m "+long+" 日本語\n", 1)))
	for w := 1; w <= 90; w += 3 {
		for _, h := range []int{1, 2, 3, 7, 25} {
			d := newTestDiffView(text, w, h)
			for _, step := range []func(){func() {}, func() { d.gotoHunk(1) }, func() { d.scroll(0, 1000) }, func() { d.scroll(1000, 0) }} {
				step()
				ls := d.lines(defaultStyles(), DefaultKeyMap(), "¿revertir algo? repite alt+r para confirmar", "¿revertir? repite alt+r", w, h)
				if len(ls) != h {
					t.Fatalf("w=%d h=%d: %d filas", w, h, len(ls))
				}
				for i, l := range ls {
					if got := lipgloss.Width(l); got != w {
						t.Fatalf("w=%d h=%d fila %d: ancho %d\n%q", w, h, i, got, plainLines([]string{l})[0])
					}
					if strings.ContainsRune(l, '\r') || strings.Contains(plainLines([]string{l})[0], "\x1b") {
						t.Fatalf("w=%d h=%d fila %d: control sin neutralizar: %q", w, h, i, l)
					}
				}
			}
		}
	}
}

func TestDiffViewGutterAdapts(t *testing.T) {
	d := newTestDiffView(twoHunkDiff(), 80, 10)
	if _, mode := d.gutter(); mode != 2 {
		t.Fatalf("ancho 80: modo %d", mode)
	}
	d.setArea(30, 10)
	if _, mode := d.gutter(); mode != 1 {
		t.Fatalf("ancho 30: modo %d", mode)
	}
	d.setArea(14, 10)
	if _, mode := d.gutter(); mode != 0 {
		t.Fatalf("ancho 14: modo %d", mode)
	}
}

func TestDiffViewHorizontalScroll(t *testing.T) {
	text := unifiedDiff("a", "b", []byte("x\n"), []byte("0123456789ABCDEFGHIJ0123456789\n"))
	d := newTestDiffView(text, 20, 8)
	if d.maxX() == 0 {
		t.Fatal("debía poder desplazarse en horizontal")
	}
	d.scroll(0, 5)
	out := strings.Join(plainLines(d.lines(defaultStyles(), DefaultKeyMap(), "", "", 20, 8)), "\n")
	if !strings.Contains(out, "56789ABC") || strings.Contains(out, "+0123") {
		t.Fatalf("no se desplazó:\n%s", out)
	}
	d.scroll(0, 1000)
	if d.xoff != d.maxX() {
		t.Fatalf("xoff = %d, max %d", d.xoff, d.maxX())
	}
}

func TestCutCellsWide(t *testing.T) {
	for skip := 0; skip < 8; skip++ {
		for n := 0; n < 8; n++ {
			if got := lipgloss.Width(cutCells("日本語abc", skip, n)); got > n {
				t.Fatalf("skip=%d n=%d: ancho %d", skip, n, got)
			}
		}
	}
	if got := cutCells("日本", 1, 4); got != " 本" {
		t.Fatalf("got %q", got)
	}
}

func TestDiffViewRevertModalLinesAndFooter(t *testing.T) {
	d := newTestDiffView(twoHunkDiff(), 60, 15)
	d.rm = &revertModal{
		kind:    revertFile,
		turn:    2,
		path:    "a/x.go",
		files:   []string{"a/x.go"},
		agentID: 1,
		name:    "agente 1",
	}

	mls := d.renderModalLines(60)
	if len(mls) != 4 {
		t.Fatalf("renderModalLines devolvió %d líneas, quería 4", len(mls))
	}
	for i, l := range mls {
		if gotW := lipgloss.Width(l); gotW != 60 {
			t.Errorf("línea %d del modal: ancho %d != 60", i, gotW)
		}
	}

	// lines con modal activo
	lines := d.lines(defaultStyles(), DefaultKeyMap(), "", "", 60, 15)
	if len(lines) != 15 {
		t.Fatalf("lines devolvió %d líneas, quería 15", len(lines))
	}
	joined := strings.Join(plainLines(lines), "\n")
	if !strings.Contains(joined, "Revertir archivo") {
		t.Errorf("el diffview debía incluir el título del modal: %s", joined)
	}
	if !strings.Contains(joined, "a/x.go") {
		t.Errorf("el diffview debía incluir el archivo afectado: %s", joined)
	}
	if !strings.Contains(joined, "[Enter] Confirmar · [Esc] Cancelar") {
		t.Errorf("el footer debía incluir la confirmación no volátil: %s", joined)
	}

	// Verificación de que no desborda con modal en múltiples anchos y alturas
	for w := 5; w <= 90; w += 3 {
		for _, h := range []int{2, 5, 10, 20} {
			d.setArea(w, h)
			ls := d.lines(defaultStyles(), DefaultKeyMap(), "", "", w, h)
			if len(ls) != h {
				t.Fatalf("w=%d h=%d: se esperaban %d líneas, got %d", w, h, h, len(ls))
			}
			for i, l := range ls {
				if gotW := lipgloss.Width(l); gotW != w {
					t.Fatalf("desborde con modal en w=%d h=%d fila %d: ancho %d != %d", w, h, i, gotW, w)
				}
			}
		}
	}
}
