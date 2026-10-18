package tui

import (
	"fmt"
	"strings"
	"testing"
)

func lines(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "linea %d\n", i)
	}
	return sb.String()
}

func TestUnifiedDiffIdentical(t *testing.T) {
	if d := unifiedDiff("a", "b", []byte("x\ny\n"), []byte("x\ny\n")); d != "" {
		t.Fatalf("diff = %q", d)
	}
	if d := unifiedDiff("a", "b", nil, nil); d != "" {
		t.Fatalf("diff = %q", d)
	}
}

func TestUnifiedDiffSimpleChange(t *testing.T) {
	old := "uno\ndos\ntres\n"
	cur := "uno\nDOS\ntres\ncuatro\n"
	want := "--- a\n+++ b\n@@ -1,3 +1,4 @@\n uno\n-dos\n+DOS\n tres\n+cuatro\n"
	if got := unifiedDiff("a", "b", []byte(old), []byte(cur)); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnifiedDiffNewAndDeletedFile(t *testing.T) {
	got := unifiedDiff("a", "b", nil, []byte("x\ny\n"))
	want := "--- a\n+++ b\n@@ -0,0 +1,2 @@\n+x\n+y\n"
	if got != want {
		t.Fatalf("nuevo:\n%s", got)
	}
	got = unifiedDiff("a", "b", []byte("x\n"), nil)
	want = "--- a\n+++ b\n@@ -1 +0,0 @@\n-x\n"
	if got != want {
		t.Fatalf("borrado:\n%s", got)
	}
}

func TestUnifiedDiffHunksSplitAndContext(t *testing.T) {
	old := lines(40)
	cur := strings.Replace(old, "linea 3\n", "TRES\n", 1)
	cur = strings.Replace(cur, "linea 35\n", "TREINTA Y CINCO\n", 1)
	got := unifiedDiff("a", "b", []byte(old), []byte(cur))
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Fatalf("se esperaban 2 hunks, hay %d:\n%s", n, got)
	}
	if !strings.Contains(got, "@@ -1,6 +1,6 @@") || !strings.Contains(got, "@@ -32,7 +32,7 @@") {
		t.Fatalf("rangos incorrectos:\n%s", got)
	}
	if strings.Contains(got, " linea 20\n") {
		t.Fatal("contexto de más")
	}
	// Cambios cercanos van en un solo hunk.
	cur2 := strings.Replace(old, "linea 10\n", "X\n", 1)
	cur2 = strings.Replace(cur2, "linea 14\n", "Y\n", 1)
	if n := strings.Count(unifiedDiff("a", "b", []byte(old), []byte(cur2)), "@@ -"); n != 1 {
		t.Fatalf("hunks = %d", n)
	}
}

// applyDiff reconstruye el nuevo texto a partir del viejo y de las líneas del
// diff, para comprobar que el diff es correcto.
func TestLineDiffReconstructs(t *testing.T) {
	cases := [][2][]string{
		{{"a", "b", "c"}, {"a", "c"}},
		{{"a", "b", "c"}, {"x", "b", "y", "z"}},
		{{}, {"a"}},
		{{"a"}, {}},
		{{"a", "a", "a"}, {"a", "a"}},
		{{"a", "b", "a", "b"}, {"b", "a", "b", "a"}},
	}
	for _, c := range cases {
		var oldRec, newRec []string
		for _, l := range lineDiff(c[0], c[1]) {
			if l.op != opAdd {
				oldRec = append(oldRec, l.text)
			}
			if l.op != opDel {
				newRec = append(newRec, l.text)
			}
		}
		if strings.Join(oldRec, "|") != strings.Join(c[0], "|") || strings.Join(newRec, "|") != strings.Join(c[1], "|") {
			t.Errorf("diff de %v -> %v no reconstruye: %v / %v", c[0], c[1], oldRec, newRec)
		}
	}
}

func TestLineDiffLargeFallsBack(t *testing.T) {
	a := make([]string, 3000)
	b := make([]string, 3000)
	for i := range a {
		a[i] = fmt.Sprintf("a%d", i)
		b[i] = fmt.Sprintf("b%d", i)
	}
	ops := lineDiff(a, b) // 9M celdas: usa el atajo, sin reservar la tabla
	if len(ops) != 6000 {
		t.Fatalf("ops = %d", len(ops))
	}
}

func TestDiffText(t *testing.T) {
	if got := diffText("x.go", 2, []byte("a\x00b"), []byte("c")); !strings.Contains(got, "binario") {
		t.Fatalf("binario: %q", got)
	}
	if got := diffText("x.go", 2, []byte("a\n"), []byte("a\n")); !strings.Contains(got, "sin cambios") {
		t.Fatalf("igual: %q", got)
	}
	got := diffText("x.go", 2, nil, []byte("a\n"))
	if !strings.Contains(got, "/dev/null") || !strings.Contains(got, "+a") {
		t.Fatalf("nuevo: %q", got)
	}
	got = diffText("x.go", 2, []byte("a\n"), []byte("b\n"))
	if !strings.Contains(got, "a/x.go (checkpoint, turno 2)") || !strings.Contains(got, "-a\n+b") {
		t.Fatalf("cambio: %q", got)
	}
	if !strings.Contains(diffText("x", 1, []byte("a\n"), nil), "ya no existe") {
		t.Fatal("borrado")
	}
}
