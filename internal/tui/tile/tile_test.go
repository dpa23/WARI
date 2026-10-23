package tile

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// tres devuelve 1 | (2 / 3): columnas con la segunda dividida en filas.
func tres(t *testing.T) *Tree {
	t.Helper()
	tr := New(1)
	must(t, tr.Split(1, 2, Cols, true, 0.5))
	must(t, tr.Split(2, 3, Rows, true, 0.5))
	return tr
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewInvalid(t *testing.T) {
	if New(0) != nil || New(-1) != nil {
		t.Fatal("New con ID inválido debe devolver nil")
	}
	var z Tree
	if z.Len() != 0 || z.Has(1) || z.Split(1, 2, Cols, true, 0) == nil || z.Close(1) == nil {
		t.Fatal("el valor cero debe fallar sin pánico")
	}
	if l := z.Layout(10, 10); len(l.Rects) != 0 {
		t.Fatal("layout del valor cero debe estar vacío")
	}
}

func TestSplitErrorsAndRatio(t *testing.T) {
	tr := New(1)
	cases := []struct {
		name          string
		target, newID ID
		axis          Axis
	}{
		{"target ausente", 9, 2, Cols},
		{"nuevo cero", 1, 0, Cols},
		{"nuevo repetido", 1, 1, Cols},
		{"eje inválido", 1, 2, 0},
	}
	for _, c := range cases {
		if err := tr.Split(c.target, c.newID, c.axis, true, 0); err == nil {
			t.Errorf("%s: esperaba error", c.name)
		}
	}
	if tr.Len() != 1 {
		t.Fatal("los errores no deben mutar el árbol")
	}
	for _, c := range []struct{ in, wantA float64 }{{0, 0.5}, {0.3, 0.7}, {0.99, 0.1}, {0.01, 0.9}} {
		tr := New(1)
		must(t, tr.Split(1, 2, Cols, true, c.in))
		if got := tr.root.ratio; got < c.wantA-1e-9 || got > c.wantA+1e-9 {
			t.Errorf("ratio %v: primer hijo %v, quería %v", c.in, got, c.wantA)
		}
	}
	// Antes: la hoja nueva es la primera.
	tr = New(1)
	must(t, tr.Split(1, 2, Rows, false, 0.25))
	if !reflect.DeepEqual(tr.IDs(), []ID{2, 1}) || tr.root.ratio != 0.25 {
		t.Fatalf("split previo: %v %v", tr.IDs(), tr.root.ratio)
	}
}

func TestCloseAndFocus(t *testing.T) {
	tr := tres(t) // 1 | (2 / 3)
	must(t, tr.SetFocus(2))
	must(t, tr.Close(2))
	if tr.Focus() != 3 || !reflect.DeepEqual(tr.IDs(), []ID{1, 3}) {
		t.Fatalf("foco %d ids %v", tr.Focus(), tr.IDs())
	}
	if tr.Close(9) == nil {
		t.Fatal("cerrar ausente debe fallar")
	}
	must(t, tr.SetFocus(1))
	must(t, tr.Close(1))
	if tr.Focus() != 3 || tr.Len() != 1 {
		t.Fatalf("foco %d len %d", tr.Focus(), tr.Len())
	}
	if tr.Close(3) == nil {
		t.Fatal("la última hoja no se cierra")
	}
	// Foco en la hoja más cercana del hermano: cerrar 4 (segundo) → hoja más a la derecha de a.
	tr = New(1)
	must(t, tr.Split(1, 2, Cols, true, 0))
	must(t, tr.Split(1, 3, Cols, true, 0)) // 1 3 | 2
	must(t, tr.Split(2, 4, Cols, true, 0))
	must(t, tr.SetFocus(4))
	must(t, tr.Close(4))
	if tr.Focus() != 2 {
		t.Fatalf("foco %d, quería 2", tr.Focus())
	}
	must(t, tr.SetFocus(2))
	must(t, tr.Close(2)) // el hermano es (1,3); lo más cercano por la derecha es 3
	if tr.Focus() != 3 {
		t.Fatalf("foco %d, quería 3", tr.Focus())
	}
}

func TestSwap(t *testing.T) {
	tr := tres(t)
	tr.SetMin(1, 5, 5)
	must(t, tr.Swap(1, 3))
	if !reflect.DeepEqual(tr.IDs(), []ID{3, 2, 1}) {
		t.Fatalf("ids %v", tr.IDs())
	}
	if tr.mins[1] != [2]int{5, 5} {
		t.Fatal("el mínimo debe seguir al ID")
	}
	must(t, tr.Swap(2, 2))
	if tr.Swap(1, 9) == nil || tr.Swap(9, 1) == nil {
		t.Fatal("swap con ausente debe fallar")
	}
}

func TestMove(t *testing.T) {
	tr := tres(t)
	cases := []struct {
		id, target ID
		edge       Dir
		want       []ID
	}{
		{1, 3, Down, []ID{2, 3, 1}},
		{1, 3, Up, []ID{2, 1, 3}},
		{3, 1, Left, []ID{3, 1, 2}},
		{2, 1, Right, []ID{1, 2, 3}},
	}
	for _, c := range cases {
		tr := tres(t)
		must(t, tr.Move(c.id, c.target, c.edge))
		if got := tr.IDs(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Move(%d,%d,%d): %v, quería %v", c.id, c.target, c.edge, got, c.want)
		}
	}
	for _, e := range [](func() error){
		func() error { return tr.Move(1, 1, Left) },
		func() error { return tr.Move(9, 1, Left) },
		func() error { return tr.Move(1, 9, Left) },
		func() error { return tr.Move(1, 2, 0) },
		func() error { return New(1).Move(1, 2, Left) },
	} {
		if e() == nil {
			t.Error("esperaba error")
		}
	}
}

func TestMovePreservesIDs(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		tr := randomTree(rng, 2+rng.Intn(10))
		ids := tr.IDs()
		want := append([]ID(nil), ids...)
		sort.Slice(want, func(a, b int) bool { return want[a] < want[b] })
		a, b := ids[rng.Intn(len(ids))], ids[rng.Intn(len(ids))]
		if a == b {
			continue
		}
		must(t, tr.Move(a, b, Dir(1+rng.Intn(4))))
		got := tr.IDs()
		sort.Slice(got, func(x, y int) bool { return got[x] < got[y] })
		if !reflect.DeepEqual(got, want) || tr.Len() != len(want) {
			t.Fatalf("conjunto cambió: %v vs %v", got, want)
		}
		checkParents(t, tr)
	}
}

// checkParents verifica la coherencia interna del árbol.
func checkParents(t *testing.T, tr *Tree) {
	t.Helper()
	count := 0
	var walk func(n, p *node)
	walk = func(n, p *node) {
		if n.parent != p {
			t.Fatal("parent incoherente")
		}
		if n.leaf() {
			count++
			if tr.leaves[n.id] != n {
				t.Fatal("mapa de hojas incoherente")
			}
			return
		}
		walk(n.a, n)
		walk(n.b, n)
	}
	walk(tr.root, nil)
	if count != len(tr.leaves) {
		t.Fatal("número de hojas incoherente")
	}
}

func TestZoomAndFocus(t *testing.T) {
	tr := tres(t)
	must(t, tr.Zoom(2))
	if tr.Zoomed() != 2 || tr.Focus() != 2 {
		t.Fatal("zoom debe fijar foco")
	}
	must(t, tr.SetFocus(3))
	if tr.Zoomed() != 3 {
		t.Fatal("el zoom debe seguir al foco")
	}
	must(t, tr.Zoom(3))
	if tr.Zoomed() != 0 {
		t.Fatal("zoom alterna")
	}
	if tr.Zoom(9) == nil || tr.SetFocus(9) == nil {
		t.Fatal("ID ausente debe fallar")
	}
	must(t, tr.Zoom(3))
	must(t, tr.Split(3, 4, Cols, true, 0))
	if tr.Zoomed() != 0 {
		t.Fatal("Split del zoomado lo desactiva")
	}
	must(t, tr.Zoom(4))
	must(t, tr.Close(4))
	if tr.Zoomed() != 0 {
		t.Fatal("Close del zoomado lo desactiva")
	}
}

func TestGrid(t *testing.T) {
	if Grid(nil, 2) != nil || Grid([]ID{1, 1}, 2) != nil || Grid([]ID{0}, 1) != nil {
		t.Fatal("Grid inválido debe dar nil")
	}
	tr := Grid([]ID{1, 2, 3, 4, 5}, 2)
	checkParents(t, tr)
	if !reflect.DeepEqual(tr.IDs(), []ID{1, 2, 3, 4, 5}) || tr.Focus() != 1 {
		t.Fatalf("ids %v", tr.IDs())
	}
	l := tr.Layout(41, 21)
	if l.Rects[1].W != l.Rects[2].W+0 && l.Rects[1].W-l.Rects[2].W > 1 {
		t.Fatalf("cuadrícula desequilibrada: %v", l.Rects)
	}
	// Cols mayor que n, y menor que 1.
	if Grid([]ID{1, 2}, 9).Len() != 2 || Grid([]ID{1, 2}, 0).Len() != 2 {
		t.Fatal("cols fuera de rango")
	}
}

// randomTree arma un árbol de n hojas con IDs 1..n.
func randomTree(rng *rand.Rand, n int) *Tree {
	tr := New(1)
	for id := ID(2); int(id) <= n; id++ {
		ids := tr.IDs()
		_ = tr.Split(ids[rng.Intn(len(ids))], id, Axis(1+rng.Intn(2)), rng.Intn(2) == 0, rng.Float64())
	}
	return tr
}
