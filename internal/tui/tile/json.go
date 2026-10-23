package tile

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// Límites al leer un árbol guardado.
const (
	maxDepth  = 32
	maxLeaves = 256
)

type jnode struct {
	ID    ID      `json:"id,omitempty"`
	Axis  string  `json:"axis,omitempty"`
	Ratio float64 `json:"ratio,omitempty"`
	A     *jnode  `json:"a,omitempty"`
	B     *jnode  `json:"b,omitempty"`
}

type jtree struct {
	Focus ID     `json:"focus"`
	Zoom  ID     `json:"zoom,omitempty"`
	Root  *jnode `json:"root"`
}

func (t *Tree) toJSON(n *node) *jnode {
	if n.leaf() {
		return &jnode{ID: n.id}
	}
	axis := "cols"
	if n.axis == Rows {
		axis = "rows"
	}
	return &jnode{Axis: axis, Ratio: n.ratio, A: t.toJSON(n.a), B: t.toJSON(n.b)}
}

// MarshalJSON serializa el árbol anidado con el foco y el zoom. Los mínimos
// no se guardan: los vuelve a fijar quien conoce cada tile.
func (t *Tree) MarshalJSON() ([]byte, error) {
	if t == nil || t.root == nil {
		return nil, errInvalid
	}
	return json.Marshal(jtree{Focus: t.focus, Zoom: t.zoom, Root: t.toJSON(t.root)})
}

// UnmarshalJSON lee un árbol y lo valida: IDs únicos y positivos, ratios en
// (0,1), profundidad máxima 32 y hasta 256 hojas. Con datos inválidos
// devuelve error y deja t intacto. Los mínimos previos se descartan.
func (t *Tree) UnmarshalJSON(data []byte) error {
	var j jtree
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	if j.Root == nil {
		return errors.New("tile: falta la raíz")
	}
	nt := &Tree{leaves: map[ID]*node{}, mins: map[ID][2]int{}}
	root, err := nt.fromJSON(j.Root, 1)
	if err != nil {
		return err
	}
	nt.root = root
	nt.focus = j.Focus
	if nt.focus == 0 {
		nt.focus = nt.IDs()[0]
	}
	if !nt.Has(nt.focus) {
		return fmt.Errorf("tile: el foco %d no existe", j.Focus)
	}
	if j.Zoom != 0 && !nt.Has(j.Zoom) {
		return fmt.Errorf("tile: el zoom %d no existe", j.Zoom)
	}
	nt.zoom = j.Zoom
	*t = *nt
	return nil
}

func (t *Tree) fromJSON(j *jnode, depth int) (*node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("tile: profundidad mayor que %d", maxDepth)
	}
	if j.A == nil && j.B == nil {
		if j.ID <= 0 || j.Axis != "" || j.Ratio != 0 {
			return nil, fmt.Errorf("tile: hoja inválida (id %d)", j.ID)
		}
		if t.leaves[j.ID] != nil {
			return nil, fmt.Errorf("tile: ID repetido %d", j.ID)
		}
		if len(t.leaves) >= maxLeaves {
			return nil, fmt.Errorf("tile: más de %d hojas", maxLeaves)
		}
		n := &node{id: j.ID}
		t.leaves[j.ID] = n
		return n, nil
	}
	if j.A == nil || j.B == nil || j.ID != 0 {
		return nil, errors.New("tile: división inválida")
	}
	n := &node{}
	switch j.Axis {
	case "cols":
		n.axis = Cols
	case "rows":
		n.axis = Rows
	default:
		return nil, fmt.Errorf("tile: eje inválido %q", j.Axis)
	}
	if math.IsNaN(j.Ratio) || j.Ratio <= 0 || j.Ratio >= 1 {
		return nil, fmt.Errorf("tile: ratio fuera de rango %v", j.Ratio)
	}
	n.ratio = j.Ratio
	t.serial++
	n.uid = t.serial
	var err error
	if n.a, err = t.fromJSON(j.A, depth+1); err != nil {
		return nil, err
	}
	if n.b, err = t.fromJSON(j.B, depth+1); err != nil {
		return nil, err
	}
	n.a.parent, n.b.parent = n, n
	return n, nil
}
