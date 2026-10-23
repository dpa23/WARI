package tile

import "math"

// Divider es el divisor de una división. Handle es opaco: identifica la
// división para DragDivider mientras el árbol no cambie de estructura.
type Divider struct {
	Rect   Rect // la celda-línea del divisor
	Axis   Axis // Cols: divisor vertical; Rows: horizontal
	Split  Rect // rectángulo de la división padre
	Handle int
}

// Layout es el reparto de un tamaño entre los tiles. Rects y Dividers cubren
// w×h exactamente, sin huecos ni solapes. Los tiles que no caben (o los que
// quedan tapados por un zoom) no tienen Rect y figuran en Collapsed.
type Layout struct {
	Rects     map[ID]Rect
	Dividers  []Divider
	Collapsed []ID
}

// Hit es el resultado de una prueba de ratón: un tile, o un divisor (Tile 0).
type Hit struct {
	Tile    ID
	Divider int // índice en Layout.Dividers, o -1
}

// Layout calcula el reparto de w×h. Cada división reparte según su ratio
// respetando los mínimos de los hijos; si no caben, el segundo colapsa.
func (t *Tree) Layout(w, h int) Layout {
	l, _ := t.compute(w, h, true)
	return l
}

func (t *Tree) compute(w, h int, useZoom bool) (Layout, map[*node]Rect) {
	l := Layout{Rects: map[ID]Rect{}}
	nr := map[*node]Rect{}
	if t.root == nil {
		return l, nr
	}
	w, h = max(w, 0), max(h, 0)
	if useZoom && t.zoom != 0 {
		for _, id := range t.IDs() {
			if id == t.zoom && w > 0 && h > 0 {
				l.Rects[id] = Rect{0, 0, w, h}
			} else {
				l.Collapsed = append(l.Collapsed, id)
			}
		}
		return l, nr
	}
	t.hide(w, h)
	t.place(t.root, Rect{0, 0, w, h}, &l, nr)
	return l, nr
}

// vis indica que bajo n queda alguna hoja visible.
func (t *Tree) vis(n *node) bool {
	if n.leaf() {
		return !t.hidden[n.id]
	}
	return t.vis(n.a) || t.vis(n.b)
}

// need es el mínimo de un subárbol en un eje (w: ancho; si no, alto). Una
// hoja necesita al menos una celda; las ocultas no necesitan nada.
func (t *Tree) need(n *node, w bool) int {
	if n.leaf() {
		if t.hidden[n.id] {
			return 0
		}
		m := t.mins[n.id]
		if w {
			return max(m[0], 1)
		}
		return max(m[1], 1)
	}
	if !t.vis(n.a) {
		return t.need(n.b, w)
	}
	if !t.vis(n.b) {
		return t.need(n.a, w)
	}
	a, b := t.need(n.a, w), t.need(n.b, w)
	if (n.axis == Cols) == w {
		return a + b + 1
	}
	return max(a, b)
}

// hide oculta, de una en una, las hojas de menor prioridad hasta que lo que
// queda cabe en w×h (o solo queda una).
func (t *Tree) hide(w, h int) {
	t.hidden = map[ID]bool{}
	ids := t.IDs()
	for len(ids)-len(t.hidden) > 1 {
		if t.need(t.root, true) <= w && t.need(t.root, false) <= h {
			return
		}
		victim := ID(0)
		for _, id := range ids {
			if t.hidden[id] {
				continue
			}
			if victim == 0 || t.prios[id] <= t.prios[victim] {
				victim = id
			}
		}
		t.hidden[victim] = true
	}
}

func (t *Tree) collapse(n *node, l *Layout) {
	if n.leaf() {
		l.Collapsed = append(l.Collapsed, n.id)
		return
	}
	t.collapse(n.a, l)
	t.collapse(n.b, l)
}

func (t *Tree) place(n *node, r Rect, l *Layout, nr map[*node]Rect) {
	if r.W <= 0 || r.H <= 0 {
		t.collapse(n, l)
		return
	}
	if !t.vis(n) {
		t.collapse(n, l)
		return
	}
	nr[n] = r
	if n.leaf() {
		l.Rects[n.id] = r
		return
	}
	// Con un hijo oculto, el otro ocupa todo el sitio.
	if !t.vis(n.a) || !t.vis(n.b) {
		if t.vis(n.a) {
			t.place(n.a, r, l, nr)
			t.collapse(n.b, l)
		} else {
			t.collapse(n.a, l)
			t.place(n.b, r, l, nr)
		}
		return
	}
	cols := n.axis == Cols
	total := r.H
	if cols {
		total = r.W
	}
	avail := total - 1
	ma, mb := t.need(n.a, cols), t.need(n.b, cols)
	if avail < ma+mb { // no caben: el segundo colapsa
		t.place(n.a, r, l, nr)
		t.collapse(n.b, l)
		return
	}
	pa := min(max(int(math.Round(float64(avail)*n.ratio)), ma), avail-mb)
	var ra, rb Rect
	var d Rect
	if cols {
		ra, d, rb = Rect{r.X, r.Y, pa, r.H}, Rect{r.X + pa, r.Y, 1, r.H}, Rect{r.X + pa + 1, r.Y, avail - pa, r.H}
	} else {
		ra, d, rb = Rect{r.X, r.Y, r.W, pa}, Rect{r.X, r.Y + pa, r.W, 1}, Rect{r.X, r.Y + pa + 1, r.W, avail - pa}
	}
	l.Dividers = append(l.Dividers, Divider{Rect: d, Axis: n.axis, Split: r, Handle: n.uid})
	t.place(n.a, ra, l, nr)
	t.place(n.b, rb, l, nr)
}

// setPos mueve el divisor de n para que el primer hijo mida pos celdas en el
// eje de la división, acotado a los mínimos. Sin espacio suficiente no hace nada.
func (t *Tree) setPos(n *node, split Rect, pos int) {
	cols := n.axis == Cols
	avail := split.H - 1
	if cols {
		avail = split.W - 1
	}
	ma, mb := t.need(n.a, cols), t.need(n.b, cols)
	if avail < ma+mb {
		return
	}
	pos = min(max(pos, ma), avail-mb)
	n.ratio = float64(pos) / float64(avail)
}

func (t *Tree) byHandle(h int) *node {
	var find func(n *node) *node
	find = func(n *node) *node {
		if n == nil || n.leaf() {
			return nil
		}
		if n.uid == h {
			return n
		}
		if f := find(n.a); f != nil {
			return f
		}
		return find(n.b)
	}
	return find(t.root)
}

// DragDivider reajusta el ratio de la división de div para que el divisor
// quede en la celda (x, y). Un Handle obsoleto no hace nada.
func (t *Tree) DragDivider(div Divider, x, y int) {
	n := t.byHandle(div.Handle)
	if n == nil || n.axis != div.Axis {
		return
	}
	if n.axis == Cols {
		t.setPos(n, div.Split, x-div.Split.X)
	} else {
		t.setPos(n, div.Split, y-div.Split.Y)
	}
}

// Resize mueve cells celdas hacia dir el divisor del ancestro más cercano de
// id cuyo eje coincide (Left/Right: Cols; Up/Down: Rows). total es el
// rectángulo con el que se calcula el Layout vigente. Sin ancestro del eje, o
// con zoom activo, no hace nada.
func (t *Tree) Resize(id ID, dir Dir, cells int, total Rect) {
	n := t.leaves[id]
	if n == nil || cells <= 0 || t.zoom != 0 {
		return
	}
	axis := Cols
	if dir == Up || dir == Down {
		axis = Rows
	}
	for n.parent != nil && n.parent.axis != axis {
		n = n.parent
	}
	anc := n.parent
	if anc == nil {
		return
	}
	_, nr := t.compute(total.W, total.H, false)
	r, ra := nr[anc], nr[anc.a]
	if r.W == 0 {
		return
	}
	cur := ra.H
	if axis == Cols {
		cur = ra.W
	}
	if dir == Left || dir == Up {
		cells = -cells
	}
	t.setPos(anc, r, cur+cells)
}

// Hit busca qué hay en (x, y): un tile, o un divisor (Tile 0, Divider >= 0).
func (l Layout) Hit(x, y int) (Hit, bool) {
	in := func(r Rect) bool { return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H }
	for id, r := range l.Rects {
		if in(r) {
			return Hit{Tile: id, Divider: -1}, true
		}
	}
	for i, d := range l.Dividers {
		if in(d.Rect) {
			return Hit{Divider: i}, true
		}
	}
	return Hit{}, false
}

// Neighbor devuelve la hoja visible adyacente al foco en la dirección dada.
// Entre las más cercanas elige la de mayor solapamiento transversal y, en
// empate, la de centro más próximo al del foco. Ignora los tiles sin Rect.
func (t *Tree) Neighbor(dir Dir, rects map[ID]Rect) (ID, bool) {
	f, ok := rects[t.focus]
	if !ok || f.W <= 0 || f.H <= 0 {
		return 0, false
	}
	horiz := dir == Left || dir == Right
	// Coordenadas en el eje de avance (s, e) y en el transversal (ts, te).
	span := func(r Rect) (s, e, ts, te int) {
		if horiz {
			return r.X, r.X + r.W, r.Y, r.Y + r.H
		}
		return r.Y, r.Y + r.H, r.X, r.X + r.W
	}
	fs, fe, fts, fte := span(f)
	var best ID
	bestGap, bestOv, bestDist := 0, 0, 0
	for id, c := range rects {
		if id == t.focus || c.W <= 0 || c.H <= 0 {
			continue
		}
		cs, ce, cts, cte := span(c)
		gap := cs - fe
		if dir == Left || dir == Up {
			gap = fs - ce
		}
		ov := min(fte, cte) - max(fts, cts)
		if gap < 0 || ov <= 0 {
			continue
		}
		dist := cts + cte - fts - fte
		if dist < 0 {
			dist = -dist
		}
		better := best == 0 || gap < bestGap ||
			(gap == bestGap && (ov > bestOv ||
				(ov == bestOv && (dist < bestDist || (dist == bestDist && id < best)))))
		if better {
			best, bestGap, bestOv, bestDist = id, gap, ov, dist
		}
	}
	return best, best != 0
}
