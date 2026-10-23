package tile

import (
	"math/rand"
	"testing"
)

// checkCover verifica que Rects y Dividers cubran w×h sin huecos ni solapes y
// que cada ID esté en Rects o en Collapsed, no en ambos.
func checkCover(t *testing.T, tr *Tree, l Layout, w, h int) {
	t.Helper()
	cells := make([]int, w*h)
	mark := func(r Rect, what string) {
		if r.W < 0 || r.H < 0 {
			t.Fatalf("%s con tamaño negativo %v", what, r)
		}
		if r.W == 0 || r.H == 0 {
			return
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > w || r.Y+r.H > h {
			t.Fatalf("%s %v fuera de %dx%d", what, r, w, h)
		}
		for y := r.Y; y < r.Y+r.H; y++ {
			for x := r.X; x < r.X+r.W; x++ {
				cells[y*w+x]++
			}
		}
	}
	for _, r := range l.Rects {
		mark(r, "rect")
	}
	for _, d := range l.Dividers {
		mark(d.Rect, "divisor")
	}
	for i, c := range cells {
		if c != 1 {
			t.Fatalf("%dx%d: celda %d,%d cubierta %d veces", w, h, i%w, i/w, c)
		}
	}
	seen := map[ID]int{}
	for id := range l.Rects {
		seen[id]++
	}
	for _, id := range l.Collapsed {
		seen[id]++
	}
	for _, id := range tr.IDs() {
		if seen[id] != 1 {
			t.Fatalf("ID %d contado %d veces", id, seen[id])
		}
	}
}

func sizes() [][2]int {
	var out [][2]int
	for w := 0; w <= 3; w++ {
		for h := 0; h <= 3; h++ {
			out = append(out, [2]int{w, h})
		}
	}
	for _, w := range []int{20, 37, 80, 121, 200} {
		for _, h := range []int{20, 24, 51, 200} {
			out = append(out, [2]int{w, h})
		}
	}
	return out
}

func TestLayoutCoverProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 150; i++ {
		tr := randomTree(rng, 1+rng.Intn(12))
		for _, id := range tr.IDs() {
			if rng.Intn(3) == 0 {
				tr.SetMin(id, rng.Intn(40), rng.Intn(12))
			}
		}
		for _, s := range sizes() {
			checkCover(t, tr, tr.Layout(s[0], s[1]), s[0], s[1])
		}
		if i%5 == 0 {
			ids := tr.IDs()
			must(t, tr.Zoom(ids[rng.Intn(len(ids))]))
			for _, s := range sizes() {
				l := tr.Layout(s[0], s[1])
				checkCover(t, tr, l, s[0], s[1])
				if len(l.Dividers) != 0 {
					t.Fatal("con zoom no hay divisores")
				}
			}
		}
	}
	if l := tr0().Layout(-5, -5); len(l.Rects) != 0 {
		t.Fatal("tamaño negativo")
	}
}

func tr0() *Tree { return New(1) }

func TestLayoutMinsRespected(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 100; i++ {
		tr := randomTree(rng, 2+rng.Intn(8))
		for _, id := range tr.IDs() {
			tr.SetMin(id, 10, 4)
		}
		for _, s := range sizes() {
			l := tr.Layout(s[0], s[1])
			for id, r := range l.Rects {
				// Un tile solo puede quedar bajo su mínimo si el espacio total ya
				// era menor que el mínimo en ese eje o si es el único que quedó.
				if (r.W < 10 && s[0] >= 10) || (r.H < 4 && s[1] >= 4) {
					t.Fatalf("tile %d con %v bajo su mínimo en %v", id, r, s)
				}
			}
		}
	}
}

func TestLayoutCollapseSecond(t *testing.T) {
	tr := New(1)
	must(t, tr.Split(1, 2, Cols, true, 0))
	tr.SetMin(1, 80, 24)
	tr.SetMin(2, 80, 24)
	l := tr.Layout(100, 30)
	if l.Rects[1] != (Rect{0, 0, 100, 30}) || len(l.Collapsed) != 1 || l.Collapsed[0] != 2 || len(l.Dividers) != 0 {
		t.Fatalf("%+v", l)
	}
	l = tr.Layout(161, 30)
	if l.Rects[1].W != 80 || l.Rects[2].W != 80 || len(l.Collapsed) != 0 {
		t.Fatalf("%+v", l)
	}
	// Con mínimos y espacio justo, el ratio se corrige.
	tr.root.ratio = 0.1
	if l = tr.Layout(170, 30); l.Rects[1].W < 80 {
		t.Fatalf("mínimo no respetado: %+v", l.Rects)
	}
}

func TestLayoutRatio(t *testing.T) {
	tr := New(1)
	must(t, tr.Split(1, 2, Rows, true, 0.5))
	l := tr.Layout(10, 21)
	if l.Rects[1] != (Rect{0, 0, 10, 10}) || l.Rects[2] != (Rect{0, 11, 10, 10}) || l.Dividers[0].Rect != (Rect{0, 10, 10, 1}) {
		t.Fatalf("%+v", l)
	}
}

func TestZoomLayout(t *testing.T) {
	tr := tres(t)
	must(t, tr.Zoom(3))
	l := tr.Layout(30, 10)
	if l.Rects[3] != (Rect{0, 0, 30, 10}) || len(l.Rects) != 1 || len(l.Collapsed) != 2 {
		t.Fatalf("%+v", l)
	}
}

func rejilla(t *testing.T) (*Tree, Layout) {
	t.Helper()
	tr := New(1) // 1 2 / 3 4
	must(t, tr.Split(1, 2, Cols, true, 0))
	must(t, tr.Split(1, 3, Rows, true, 0))
	must(t, tr.Split(2, 4, Rows, true, 0))
	return tr, tr.Layout(21, 21)
}

func TestNeighborGrid(t *testing.T) {
	tr, l := rejilla(t)
	cases := []struct {
		from ID
		dir  Dir
		want ID
		ok   bool
	}{
		{1, Right, 2, true}, {1, Down, 3, true}, {1, Left, 0, false}, {1, Up, 0, false},
		{4, Left, 3, true}, {4, Up, 2, true}, {3, Right, 4, true}, {2, Down, 4, true},
	}
	for _, c := range cases {
		must(t, tr.SetFocus(c.from))
		got, ok := tr.Neighbor(c.dir, l.Rects)
		if got != c.want || ok != c.ok {
			t.Errorf("%d dir %d: %d,%v quería %d,%v", c.from, c.dir, got, ok, c.want, c.ok)
		}
	}
}

func TestNeighborAsymmetric(t *testing.T) {
	// 1 | (2 / (3 | 4)): desde 1 a la derecha, 2 (arriba) y 3 (abajo) lo tocan;
	// gana la de mayor solapamiento; con 2 mucho más alto, es 2.
	tr := New(1)
	must(t, tr.Split(1, 2, Cols, true, 0))
	must(t, tr.Split(2, 3, Rows, true, 0.2))
	must(t, tr.Split(3, 4, Cols, true, 0))
	l := tr.Layout(41, 31)
	must(t, tr.SetFocus(1))
	if got, _ := tr.Neighbor(Right, l.Rects); got != 2 {
		t.Fatalf("vecino %d, quería 2", got)
	}
	// Desde 4 hacia la izquierda: 3 adyacente, no 1.
	must(t, tr.SetFocus(4))
	if got, _ := tr.Neighbor(Left, l.Rects); got != 3 {
		t.Fatalf("vecino %d, quería 3", got)
	}
	// Desde 3 hacia arriba: solo 2.
	must(t, tr.SetFocus(3))
	if got, _ := tr.Neighbor(Up, l.Rects); got != 2 {
		t.Fatalf("vecino %d, quería 2", got)
	}
	// Ignora colapsadas y focos sin rect.
	if _, ok := tr.Neighbor(Left, map[ID]Rect{1: {}, 3: {}}); ok {
		t.Fatal("sin rect de foco no hay vecino")
	}
	if got, ok := tr.Neighbor(Left, map[ID]Rect{1: {}, 3: {0, 0, 5, 5}}); ok {
		t.Fatalf("vecino colapsado %d", got)
	}
}

func TestHit(t *testing.T) {
	tr := New(1)
	must(t, tr.Split(1, 2, Cols, true, 0))
	l := tr.Layout(11, 5)
	if h, ok := l.Hit(0, 0); !ok || h.Tile != 1 || h.Divider != -1 {
		t.Fatalf("%+v", h)
	}
	if h, ok := l.Hit(5, 2); !ok || h.Tile != 0 || h.Divider != 0 {
		t.Fatalf("%+v", h)
	}
	if h, ok := l.Hit(10, 4); !ok || h.Tile != 2 {
		t.Fatalf("%+v", h)
	}
	if _, ok := l.Hit(11, 0); ok {
		t.Fatal("fuera")
	}
	if _, ok := l.Hit(-1, 0); ok {
		t.Fatal("fuera")
	}
}

func TestDragDivider(t *testing.T) {
	tr := New(1)
	must(t, tr.Split(1, 2, Cols, true, 0))
	l := tr.Layout(41, 10)
	tr.DragDivider(l.Dividers[0], 10, 3)
	l = tr.Layout(41, 10)
	if l.Rects[1].W != 10 || l.Dividers[0].Rect.X != 10 || l.Rects[2].W != 30 {
		t.Fatalf("%+v", l)
	}
	// Con mínimos se acota, y un handle obsoleto no hace nada.
	tr.SetMin(1, 15, 1)
	tr.SetMin(2, 8, 1)
	d := l.Dividers[0]
	tr.DragDivider(d, 2, 0)
	if l = tr.Layout(41, 10); l.Rects[1].W != 15 {
		t.Fatalf("mínimo izquierdo: %+v", l.Rects)
	}
	tr.DragDivider(d, 100, 0)
	if l = tr.Layout(41, 10); l.Rects[2].W != 8 {
		t.Fatalf("mínimo derecho: %+v", l.Rects)
	}
	tr.DragDivider(Divider{Handle: 999, Axis: Cols, Split: Rect{0, 0, 41, 10}}, 5, 0)
	if tr.Layout(41, 10).Rects[2].W != 8 {
		t.Fatal("handle obsoleto cambió el árbol")
	}
	// Filas, con desplazamiento.
	tr = New(1)
	must(t, tr.Split(1, 2, Rows, true, 0))
	must(t, tr.Split(2, 3, Cols, true, 0))
	l = tr.Layout(20, 21)
	for _, d := range l.Dividers {
		if d.Axis == Rows {
			tr.DragDivider(d, 0, 5)
		}
	}
	l = tr.Layout(20, 21)
	if l.Rects[1].H != 5 {
		t.Fatalf("%+v", l.Rects)
	}
	for _, d := range l.Dividers { // divisor anidado: el rectángulo padre tiene Y != 0
		if d.Axis == Cols {
			tr.DragDivider(d, 4, 0)
		}
	}
	if l = tr.Layout(20, 21); l.Rects[2].W != 4 {
		t.Fatalf("%+v", l.Rects)
	}
}

func TestResize(t *testing.T) {
	total := Rect{0, 0, 41, 21}
	tr := tres(t) // 1 | (2 / 3)
	tr.Resize(1, Right, 5, total)
	l := tr.Layout(41, 21)
	if l.Rects[1].W != 25 {
		t.Fatalf("1: %+v", l.Rects)
	}
	tr.Resize(1, Left, 10, total)
	if l = tr.Layout(41, 21); l.Rects[1].W != 15 {
		t.Fatalf("1: %+v", l.Rects)
	}
	// 3 sube: ancestro Rows; mueve el divisor entre 2 y 3.
	before := tr.Layout(41, 21).Rects[2].H
	tr.Resize(3, Up, 3, total)
	if l = tr.Layout(41, 21); l.Rects[2].H != before-3 {
		t.Fatalf("2: %+v", l.Rects)
	}
	// 1 no tiene ancestro Rows: nada.
	snap := tr.Layout(41, 21)
	tr.Resize(1, Down, 3, total)
	tr.Resize(9, Down, 3, total)
	tr.Resize(1, Right, 0, total)
	if l = tr.Layout(41, 21); l.Rects[1] != snap.Rects[1] || l.Rects[2] != snap.Rects[2] {
		t.Fatal("Resize sin ancestro cambió el árbol")
	}
	// Clamp a mínimos.
	tr.SetMin(2, 10, 1)
	tr.SetMin(3, 10, 1)
	tr.Resize(1, Right, 100, total)
	if l = tr.Layout(41, 21); l.Rects[2].W != 10 {
		t.Fatalf("clamp: %+v", l.Rects)
	}
}

func TestLowestPriorityLeavesHideFirst(t *testing.T) {
	// árbol | (editor | chat) con mínimos: el chat se oculta antes que el árbol
	// y el editor nunca.
	tr := New(2)
	_ = tr.Split(2, 1, Cols, false, 0.2)
	_ = tr.Split(2, 3, Cols, true, 0.45)
	tr.SetMin(1, 14, 1)
	tr.SetMin(2, 20, 1)
	tr.SetMin(3, 36, 1)
	tr.SetPriority(1, 20)
	tr.SetPriority(2, 100)
	tr.SetPriority(3, 10)

	for _, c := range []struct {
		w          int
		tree, chat bool
	}{{140, true, true}, {60, true, false}, {30, false, false}} {
		l := tr.Layout(c.w, 10)
		_, hasTree := l.Rects[1]
		_, hasChat := l.Rects[3]
		if _, ok := l.Rects[2]; !ok {
			t.Fatalf("w=%d: el editor nunca se oculta", c.w)
		}
		if hasTree != c.tree || hasChat != c.chat {
			t.Errorf("w=%d: árbol=%v chat=%v, quería %v %v (colapsados %v)", c.w, hasTree, hasChat, c.tree, c.chat, l.Collapsed)
		}
	}
}
