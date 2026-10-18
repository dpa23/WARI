package dap

import "context"

// Node es una variable (o un ámbito) en el árbol navegable. Los hijos se
// piden al expandir, no antes.
type Node struct {
	Variable
	// Scope es true en los nodos raíz que representan un ámbito.
	Scope    bool
	Children []*Node
	Expanded bool
	loaded   bool
}

// Expandable indica si el nodo tiene hijos que pedir.
func (n *Node) Expandable() bool { return n.VariablesReference > 0 }

// Row es una fila del árbol aplanado, lista para dibujar.
type Row struct {
	Node  *Node
	Depth int
}

// Tree es un árbol de variables con expansión perezosa. No es seguro para uso
// concurrente: lo maneja la vista que lo posee. Vale solo mientras el
// depurado siga en la misma parada; al reanudar hay que descartarlo.
type Tree struct {
	Roots []*Node
	load  func(ctx context.Context, ref int) ([]Variable, error)
}

// NewTree crea un árbol vacío que pide los hijos con load (por ejemplo
// Session.Variables).
func NewTree(load func(ctx context.Context, ref int) ([]Variable, error)) *Tree {
	return &Tree{load: load}
}

// Expand carga los hijos de n si hace falta y lo marca expandido. Si la carga
// falla, el nodo queda sin expandir y se puede reintentar.
func (t *Tree) Expand(ctx context.Context, n *Node) error {
	if !n.Expandable() {
		return nil
	}
	if !n.loaded {
		vars, err := t.load(ctx, n.VariablesReference)
		if err != nil {
			return err
		}
		n.Children = make([]*Node, len(vars))
		for i, v := range vars {
			n.Children[i] = &Node{Variable: v}
		}
		n.loaded = true
	}
	n.Expanded = true
	return nil
}

// Collapse pliega n; sus hijos cargados se conservan.
func (t *Tree) Collapse(n *Node) { n.Expanded = false }

// Toggle expande o pliega n.
func (t *Tree) Toggle(ctx context.Context, n *Node) error {
	if n.Expanded {
		t.Collapse(n)
		return nil
	}
	return t.Expand(ctx, n)
}

// Rows aplana el árbol en orden de dibujo: cada nodo seguido de sus hijos si
// está expandido.
func (t *Tree) Rows() []Row {
	var rows []Row
	var walk func(ns []*Node, depth int)
	walk = func(ns []*Node, depth int) {
		for _, n := range ns {
			rows = append(rows, Row{Node: n, Depth: depth})
			if n.Expanded {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(t.Roots, 0)
	return rows
}
