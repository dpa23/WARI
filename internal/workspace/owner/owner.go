// Package owner lleva la propiedad de archivos entre agentes: un agente
// reclama un archivo (o una carpeta) y, mientras no lo libere, otro que quiera
// editarlo es un choque. El paquete solo conoce números de agente; decidir qué
// hacer ante un choque (pausar, avisar) es cosa del supervisor.
//
// Las rutas son relativas a la raíz del proyecto, con "/" y sin "./". Una
// ruta que termina en "/" reclama una carpeta entera.
package owner

import (
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Conflict describe un choque: Agent quiere editar Path, pero Holder ya
// reclamó Claimed (el mismo archivo o una carpeta que lo contiene).
type Conflict struct {
	Agent   int
	Holder  int
	Path    string
	Claimed string
}

// Table es la tabla de propiedad. Es segura para uso concurrente.
type Table struct {
	mu     sync.Mutex
	claims map[string]int // ruta normalizada (carpetas con "/" final) -> agente
}

// New crea una tabla vacía.
func New() *Table { return &Table{claims: make(map[string]int)} }

// Normalize deja rel en la forma canónica de la tabla: con "/", limpia, sin
// "./" ni "/" inicial; conserva el "/" final de las carpetas. Devuelve "" si
// la ruta es vacía o sale de la raíz ("..").
func Normalize(rel string) string {
	if rel == "" {
		return ""
	}
	dir := strings.HasSuffix(rel, "/") || strings.HasSuffix(rel, string(filepath.Separator))
	rel = path.Clean(filepath.ToSlash(rel))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." || rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	if dir {
		rel += "/"
	}
	return rel
}

// Rel convierte p (absoluta o relativa a root) en una ruta relativa a root.
// inside es false si p queda fuera de root; entonces devuelve p limpia y con
// "/".
func Rel(root, p string) (rel string, inside bool) {
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		if n := Normalize(p); n != "" {
			return n, true
		}
		return filepath.ToSlash(filepath.Clean(p)), false
	}
	for _, r := range roots(root) {
		if r == "" {
			continue
		}
		for _, q := range paths(p) {
			if x, err := filepath.Rel(r, q); err == nil && x != "." && x != ".." &&
				!strings.HasPrefix(x, ".."+string(filepath.Separator)) {
				return filepath.ToSlash(x), true
			}
		}
	}
	return filepath.ToSlash(filepath.Clean(p)), false
}

// paths devuelve p y, si difiere, p con los enlaces simbólicos de su carpeta
// resueltos: el archivo puede no existir todavía, pero su carpeta sí. Sin
// esto, en macOS (donde /var es un enlace a /private/var) una ruta y una
// raíz que apuntan al mismo sitio no coincidirían.
func paths(p string) []string {
	out := []string{filepath.Clean(p)}
	if d, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		if q := filepath.Join(d, filepath.Base(p)); q != out[0] {
			out = append(out, q)
		}
	}
	return out
}

// roots devuelve root y, si difiere, su ruta con los enlaces simbólicos
// resueltos (por ejemplo /tmp y /private/tmp).
func roots(root string) []string {
	out := []string{filepath.Clean(root)}
	if r, err := filepath.EvalSymlinks(root); err == nil && r != out[0] {
		out = append(out, r)
	}
	return out
}

// Claim reclama p para agent. Si otro agente ya reclamó el archivo, una
// carpeta que lo contiene o (al reclamar una carpeta) algo dentro de ella, no
// cambia nada y devuelve el choque. Reclamar lo que ya es suyo no hace nada.
func (t *Table) Claim(agent int, p string) *Conflict {
	p = Normalize(p)
	if p == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if c := t.conflictLocked(agent, p); c != nil {
		return c
	}
	t.claims[p] = agent
	return nil
}

// Check devuelve el choque que tendría agent al editar p, sin reclamar nada.
func (t *Table) Check(agent int, p string) *Conflict {
	p = Normalize(p)
	if p == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conflictLocked(agent, p)
}

func (t *Table) conflictLocked(agent int, p string) *Conflict {
	// Lo reclamado exactamente o por una carpeta que lo contiene.
	for _, c := range candidates(p) {
		if h, ok := t.claims[c]; ok && h != agent {
			return &Conflict{Agent: agent, Holder: h, Path: p, Claimed: c}
		}
	}
	// Al reclamar una carpeta, lo ya reclamado dentro de ella (en orden
	// estable para que el resultado sea determinista).
	if strings.HasSuffix(p, "/") {
		var inside []string
		for c, h := range t.claims {
			if h != agent && strings.HasPrefix(c, p) {
				inside = append(inside, c)
			}
		}
		if len(inside) > 0 {
			sort.Strings(inside)
			return &Conflict{Agent: agent, Holder: t.claims[inside[0]], Path: p, Claimed: inside[0]}
		}
	}
	return nil
}

// candidates son las claves que cubren p: la propia y cada carpeta
// ancestra (con "/" final).
func candidates(p string) []string {
	out := []string{p}
	trimmed := strings.TrimSuffix(p, "/")
	for {
		i := strings.LastIndex(trimmed, "/")
		if i < 0 {
			break
		}
		trimmed = trimmed[:i]
		out = append(out, trimmed+"/")
	}
	return out
}

// Holder devuelve quién reclamó p (directamente o por una carpeta) y la clave
// de la reclamación; ok es false si nadie.
func (t *Table) Holder(p string) (agent int, claimed string, ok bool) {
	p = Normalize(p)
	if p == "" {
		return 0, "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, c := range candidates(p) {
		if h, found := t.claims[c]; found {
			return h, c, true
		}
	}
	return 0, "", false
}

// Takeover pasa a agent la reclamación de p (o de la carpeta que la cubre),
// aunque sea de otro: es la decisión explícita del usuario. Devuelve el
// anterior dueño (0 si nadie) y la clave que se movió.
func (t *Table) Takeover(agent int, p string) (prev int, claimed string) {
	p = Normalize(p)
	if p == "" {
		return 0, ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	claimed = p
	for _, c := range candidates(p) {
		if h, ok := t.claims[c]; ok {
			prev, claimed = h, c
			break
		}
	}
	t.claims[claimed] = agent
	return prev, claimed
}

// Release libera todo lo que reclamó agent y devuelve las rutas liberadas,
// ordenadas.
func (t *Table) Release(agent int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for c, h := range t.claims {
		if h == agent {
			out = append(out, c)
			delete(t.claims, c)
		}
	}
	sort.Strings(out)
	return out
}

// ReleasePath libera p si lo reclamó agent. Devuelve si liberó algo.
func (t *Table) ReleasePath(agent int, p string) bool {
	p = Normalize(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	if h, ok := t.claims[p]; ok && h == agent {
		delete(t.claims, p)
		return true
	}
	return false
}

// Claims devuelve lo que reclamó agent, ordenado.
func (t *Table) Claims(agent int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for c, h := range t.claims {
		if h == agent {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}
