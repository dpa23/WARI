// Package tile es un motor puro de tiling: un árbol de divisiones binarias
// cuyas hojas son tiles identificados por ID. No conoce Bubble Tea, lipgloss
// ni E/S; solo reparte celdas. El layout de WARI lo usa para colocar el árbol
// de archivos, el editor y N terminales de agentes.
//
// Un Tree no es seguro para uso concurrente: el llamante serializa el acceso.
//
// Convenciones. Cols pone los hijos lado a lado (divisor vertical) y Rows los
// apila (divisor horizontal). El ratio de cada división es la fracción del
// primer hijo (izquierdo o superior); el segundo recibe el resto. Los
// divisores ocupan una celda entre hermanos.
package tile

import (
	"errors"
	"fmt"
	"math"
)

// ID identifica un tile. El cero es inválido.
type ID int

// Axis es el eje de una división.
type Axis int

const (
	// Cols coloca los hijos lado a lado, separados por un divisor vertical.
	Cols Axis = iota + 1
	// Rows apila los hijos, separados por un divisor horizontal.
	Rows
)

// Dir es una dirección.
type Dir int

const (
	Left Dir = iota + 1
	Right
	Up
	Down
)

// Rect es un rectángulo de celdas.
type Rect struct{ X, Y, W, H int }

const (
	minRatio = 0.1
	maxRatio = 0.9
)

// node es una hoja (id != 0) o una división (a y b no nulos).
type node struct {
	id     ID
	axis   Axis
	ratio  float64 // fracción del primer hijo, en (0,1)
	a, b   *node
	parent *node
	uid    int // identidad de una división, para Divider.Handle
}

func (n *node) leaf() bool { return n.a == nil }

// Tree es el árbol de tiles. El valor cero es inválido: créalo con New,
// Grid o UnmarshalJSON.
type Tree struct {
	root   *node
	leaves map[ID]*node
	mins   map[ID][2]int // ancho y alto mínimos por hoja
	prios  map[ID]int    // prioridad de cada hoja (mayor: se oculta después)
	hidden map[ID]bool   // hojas ocultas por falta de sitio en el último Layout
	focus  ID
	zoom   ID
	serial int
}

var (
	errInvalid = errors.New("tile: árbol no inicializado")
	errLast    = errors.New("tile: no se puede quitar la última hoja")
)

func errMissing(id ID) error { return fmt.Errorf("tile: el tile %d no existe", id) }

// New crea un árbol con una sola hoja, que recibe el foco. Si root es 0
// devuelve nil.
func New(root ID) *Tree {
	if root <= 0 {
		return nil
	}
	t := &Tree{leaves: map[ID]*node{}, mins: map[ID][2]int{}}
	n := &node{id: root}
	t.root, t.leaves[root], t.focus = n, n, root
	return t
}

// Grid arma una cuadrícula equilibrada de cols columnas (para la vista de
// visión general). Devuelve nil si ids está vacío, tiene un ID inválido o
// repetido. El foco queda en el primer ID.
func Grid(ids []ID, cols int) *Tree {
	if len(ids) == 0 {
		return nil
	}
	seen := map[ID]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil
		}
		seen[id] = true
	}
	cols = max(1, min(cols, len(ids)))
	t := &Tree{leaves: map[ID]*node{}, mins: map[ID][2]int{}, focus: ids[0]}
	var rows []*node
	for i := 0; i < len(ids); i += cols {
		var items []*node
		for _, id := range ids[i:min(i+cols, len(ids))] {
			n := &node{id: id}
			t.leaves[id] = n
			items = append(items, n)
		}
		rows = append(rows, t.chain(items, Cols))
	}
	t.root = t.chain(rows, Rows)
	return t
}

// chain une nodos en un árbol equilibrado del eje dado.
func (t *Tree) chain(ns []*node, axis Axis) *node {
	if len(ns) == 1 {
		return ns[0]
	}
	k := (len(ns) + 1) / 2
	p := &node{axis: axis, ratio: float64(k) / float64(len(ns)), a: t.chain(ns[:k], axis), b: t.chain(ns[k:], axis)}
	p.a.parent, p.b.parent = p, p
	t.serial++
	p.uid = t.serial
	return p
}

// Has informa si el tile existe.
func (t *Tree) Has(id ID) bool { return t.leaves[id] != nil }

// Len es el número de hojas.
func (t *Tree) Len() int { return len(t.leaves) }

// IDs devuelve las hojas en orden de recorrido (primer hijo antes que segundo).
func (t *Tree) IDs() []ID {
	var out []ID
	var walk func(n *node)
	walk = func(n *node) {
		if n == nil {
			return
		}
		if n.leaf() {
			out = append(out, n.id)
			return
		}
		walk(n.a)
		walk(n.b)
	}
	walk(t.root)
	return out
}

// replace pone nw en el lugar de old.
func (t *Tree) replace(old, nw *node) {
	p := old.parent
	nw.parent = p
	switch {
	case p == nil:
		t.root = nw
	case p.a == old:
		p.a = nw
	default:
		p.b = nw
	}
}

// attach parte la hoja tgt y deja nl antes o después. frac es la fracción
// que ocupa nl.
func (t *Tree) attach(tgt, nl *node, axis Axis, after bool, frac float64) {
	t.serial++
	p := &node{axis: axis, uid: t.serial}
	t.replace(tgt, p)
	if after {
		p.a, p.b, p.ratio = tgt, nl, 1-frac
	} else {
		p.a, p.b, p.ratio = nl, tgt, frac
	}
	p.a.parent, p.b.parent = p, p
}

// detach saca la hoja n del árbol y devuelve su hermano, que ocupa su sitio.
func (t *Tree) detach(n *node) *node {
	p := n.parent
	s := p.a
	if s == n {
		s = p.b
	}
	t.replace(p, s)
	n.parent = nil
	return s
}

// Split parte la hoja target dejando newID después (after) o antes. ratio es
// la fracción que ocupa la hoja nueva: 0 vale 0.5 y se acota a 0.1–0.9. El
// foco no cambia. Partir el tile con zoom lo desactiva.
func (t *Tree) Split(target, newID ID, axis Axis, after bool, ratio float64) error {
	if t.root == nil {
		return errInvalid
	}
	tg := t.leaves[target]
	switch {
	case tg == nil:
		return errMissing(target)
	case newID <= 0:
		return fmt.Errorf("tile: ID inválido %d", newID)
	case t.leaves[newID] != nil:
		return fmt.Errorf("tile: el tile %d ya existe", newID)
	case axis != Cols && axis != Rows:
		return fmt.Errorf("tile: eje inválido %d", axis)
	}
	if ratio == 0 || math.IsNaN(ratio) {
		ratio = 0.5
	}
	ratio = math.Min(maxRatio, math.Max(minRatio, ratio))
	nl := &node{id: newID}
	t.leaves[newID] = nl
	t.attach(tg, nl, axis, after, ratio)
	if t.zoom == target {
		t.zoom = 0
	}
	return nil
}

// Close quita la hoja; su hermano ocupa el sitio. Si tenía el foco, este pasa
// a la hoja más cercana del hermano. Cerrar el tile con zoom lo desactiva.
func (t *Tree) Close(id ID) error {
	if t.root == nil {
		return errInvalid
	}
	n := t.leaves[id]
	if n == nil {
		return errMissing(id)
	}
	if len(t.leaves) == 1 {
		return errLast
	}
	wasFirst := n.parent.a == n
	s := t.detach(n)
	delete(t.leaves, id)
	delete(t.mins, id)
	delete(t.prios, id)
	if t.zoom == id {
		t.zoom = 0
	}
	if t.focus == id {
		for !s.leaf() {
			if wasFirst {
				s = s.a
			} else {
				s = s.b
			}
		}
		t.focus = s.id
	}
	return nil
}

// Swap intercambia las posiciones de dos tiles. Los mínimos y el foco siguen
// al ID, no a la posición.
func (t *Tree) Swap(a, b ID) error {
	na, nb := t.leaves[a], t.leaves[b]
	if na == nil {
		return errMissing(a)
	}
	if nb == nil {
		return errMissing(b)
	}
	na.id, nb.id = b, a
	t.leaves[a], t.leaves[b] = nb, na
	return nil
}

// Move saca id y lo reinserta partiendo target por el borde indicado
// (Left/Up lo dejan antes, Right/Down después; mitad y mitad). Falla si
// id == target, falta alguno o id es la única hoja. Conserva foco y zoom.
func (t *Tree) Move(id, target ID, edge Dir) error {
	if t.root == nil {
		return errInvalid
	}
	n, tg := t.leaves[id], t.leaves[target]
	switch {
	case n == nil:
		return errMissing(id)
	case tg == nil:
		return errMissing(target)
	case id == target:
		return errors.New("tile: no se puede mover un tile sobre sí mismo")
	case len(t.leaves) == 1:
		return errLast
	}
	var axis Axis
	switch edge {
	case Left, Right:
		axis = Cols
	case Up, Down:
		axis = Rows
	default:
		return fmt.Errorf("tile: dirección inválida %d", edge)
	}
	t.detach(n)
	t.attach(tg, n, axis, edge == Right || edge == Down, 0.5)
	return nil
}

// Focus devuelve el tile con foco.
func (t *Tree) Focus() ID { return t.focus }

// SetFocus da el foco a id. Con zoom activo, el zoom pasa a id.
func (t *Tree) SetFocus(id ID) error {
	if !t.Has(id) {
		return errMissing(id)
	}
	t.focus = id
	if t.zoom != 0 {
		t.zoom = id
	}
	return nil
}

// Zoom alterna maximizar id (y le da el foco).
func (t *Tree) Zoom(id ID) error {
	if !t.Has(id) {
		return errMissing(id)
	}
	if t.zoom == id {
		t.zoom = 0
	} else {
		t.zoom, t.focus = id, id
	}
	return nil
}

// Zoomed devuelve el tile maximizado, o 0.
func (t *Tree) Zoomed() ID { return t.zoom }

// SetMin fija el tamaño mínimo de una hoja (0 = sin mínimo). Se ignora si el
// tile no existe. No se serializa.
func (t *Tree) SetMin(id ID, w, h int) {
	if !t.Has(id) {
		return
	}
	if w <= 0 && h <= 0 {
		delete(t.mins, id)
		return
	}
	t.mins[id] = [2]int{max(w, 0), max(h, 0)}
}

// SetPriority fija cuánto importa una hoja cuando no hay sitio para todas: se
// oculta primero la de menor prioridad (0 por omisión; a igual prioridad, la
// última en el orden de recorrido). No se serializa.
func (t *Tree) SetPriority(id ID, p int) {
	if !t.Has(id) {
		return
	}
	if t.prios == nil {
		t.prios = map[ID]int{}
	}
	t.prios[id] = p
}
