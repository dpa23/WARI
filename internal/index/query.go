package index

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dpa23/wari/internal/imports"
)

// Niveles de cercanía de un símbolo respecto al archivo desde el que se
// consulta; menor es mejor.
const (
	tierSameFile = iota
	tierSamePackage
	tierImported
	tierGlobal
)

// tierOf calcula la cercanía de s al archivo cur.
func tierOf(cur *File, s *Symbol) int {
	if cur == nil {
		return tierGlobal
	}
	if s.Path != "" && s.Path == cur.Path {
		return tierSameFile
	}
	if s.Package != "" && s.Package == cur.Package {
		return tierSamePackage
	}
	if imported(cur, s) {
		return tierImported
	}
	return tierGlobal
}

// imported indica que alguno de los imports de cur trae a s: el tipo mismo,
// su package completo ("a.b.*") o, para un miembro, su clase contenedora. En
// C# cada using es un namespace.
func imported(cur *File, s *Symbol) bool {
	if s.Package == "" {
		return false
	}
	for _, imp := range cur.Imports {
		if cur.Lang == LangCSharp {
			if imp == s.Package {
				return true
			}
			continue
		}
		if strings.HasSuffix(imp, ".*") {
			if imp[:len(imp)-2] == s.Package {
				return true
			}
			continue
		}
		if imp == s.Qualified() {
			return true
		}
		if s.Container != "" && imp == s.Package+"."+s.Container {
			return true
		}
	}
	return false
}

func isDecl(k Kind) bool { return k != KindPackage && k != KindNamespace }

// Resolve busca las definiciones aproximadas del identificador word desde el
// archivo cur (analizado de su texto actual, con File.Path = su ruta relativa).
// Prefiere el mismo archivo, luego el mismo package o namespace, luego lo que
// cur importa y por último el resto del proyecto; devuelve solo los símbolos
// del mejor nivel que haya, y si alguno es un tipo, solo los tipos (así un
// constructor no tapa a su clase). Varios resultados (sobrecargas, homónimos)
// los resuelve quien llama con un selector.
//
// Si el cursor (line, col; base 0, runas) está sobre la declaración de un
// símbolo de cur, ese símbolo se excluye. Con line < 0 no se excluye nada. El
// índice puede ser nil: entonces solo se mira cur.
func (ix *Index) Resolve(word string, cur *File, line, col int) []Symbol {
	if word == "" {
		return nil
	}
	type scored struct {
		sym  Symbol
		tier int
	}
	var all []scored
	if cur != nil {
		for i := range cur.Symbols {
			s := cur.Symbols[i]
			if s.Name != word || !isDecl(s.Kind) {
				continue
			}
			if line >= 0 && s.Line == line && col >= s.Col && col <= s.Col+utf8.RuneCountInString(s.Name) {
				continue
			}
			all = append(all, scored{s, tierSameFile})
		}
	}
	for _, sp := range ix.view().byName[word] {
		if !isDecl(sp.Kind) || (cur != nil && sp.Path == cur.Path) {
			continue
		}
		all = append(all, scored{*sp, tierOf(cur, sp)})
	}
	if len(all) == 0 {
		return nil
	}
	best := tierGlobal
	hasType := false
	for _, s := range all {
		best = min(best, s.tier)
	}
	for _, s := range all {
		if s.tier == best && s.sym.Kind.IsType() {
			hasType = true
		}
	}
	var out []Symbol
	for _, s := range all {
		if s.tier == best && (!hasType || s.sym.Kind.IsType()) {
			out = append(out, s.sym)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Path != out[b].Path {
			return out[a].Path < out[b].Path
		}
		return out[a].Line < out[b].Line
	})
	return out
}

// Completion es un nombre sugerido por Complete.
type Completion struct {
	Name string
	// Kind, Container y Package describen la mejor declaración de ese nombre.
	Kind      Kind
	Container string
	Package   string
	// Count es cuántas declaraciones tiene el nombre (sobrecargas, homónimos).
	Count int
	tier  int
}

// Complete sugiere nombres de símbolos del proyecto (y de cur) que empiezan
// por prefix, sin distinguir mayúsculas, ordenados por cercanía a cur (mismo
// archivo, mismo package, importados, resto), con los tipos antes que los
// miembros y luego por nombre. Devuelve a lo sumo limit; no incluye el propio
// prefix si ya es un nombre completo. El índice puede ser nil.
func (ix *Index) Complete(prefix string, cur *File, limit int) []Completion {
	if prefix == "" || limit <= 0 {
		return nil
	}
	lp := strings.ToLower(prefix)
	best := map[string]*Completion{}
	add := func(s *Symbol, tier int) {
		if !isDecl(s.Kind) || s.Name == prefix {
			return
		}
		c := best[s.Name]
		if c == nil {
			best[s.Name] = &Completion{Name: s.Name, Kind: s.Kind, Container: s.Container, Package: s.Package, Count: 1, tier: tier}
			return
		}
		c.Count++
		if tier < c.tier || (tier == c.tier && s.Kind.IsType() && !c.Kind.IsType()) {
			c.Kind, c.Container, c.Package, c.tier = s.Kind, s.Container, s.Package, tier
		}
	}
	if cur != nil {
		for i := range cur.Symbols {
			s := &cur.Symbols[i]
			if strings.HasPrefix(strings.ToLower(s.Name), lp) {
				add(s, tierSameFile)
			}
		}
	}
	v := ix.view()
	from := sort.Search(len(v.names), func(i int) bool { return v.names[i].lower >= lp })
	const maxScan = 5000
	for i := from; i < len(v.names) && i < from+maxScan && strings.HasPrefix(v.names[i].lower, lp); i++ {
		for _, sp := range v.byName[v.names[i].name] {
			if cur != nil && sp.Path == cur.Path {
				continue
			}
			add(sp, tierOf(cur, sp))
		}
	}
	if len(best) == 0 {
		return nil
	}
	out := make([]Completion, 0, len(best))
	for _, c := range best {
		out = append(out, *c)
	}
	sort.Slice(out, func(a, b int) bool {
		x, y := out[a], out[b]
		if x.tier != y.tier {
			return x.tier < y.tier
		}
		if x.Kind.IsType() != y.Kind.IsType() {
			return x.Kind.IsType()
		}
		if len(x.Name) != len(y.Name) {
			return len(x.Name) < len(y.Name)
		}
		return x.Name < y.Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ImportMap devuelve los tipos de nivel superior del proyecto como mapas
// tipo→import por lenguaje ("java", "kotlin", "csharp"), listos para sumarse a
// los mapas de internal/imports. En Java y Kotlin el candidato es el nombre
// completo (package.Tipo) y Java y Kotlin comparten los tipos; en C# es el
// namespace. Los tipos sin package o namespace y los anidados no se incluyen.
func (ix *Index) ImportMap() map[string]imports.Map {
	jvm, cs := map[string][]string{}, map[string][]string{}
	seen := map[string]bool{}
	v := ix.view()
	for _, syms := range v.byName {
		for _, s := range syms {
			if !s.Kind.IsType() || s.Container != "" || s.Package == "" {
				continue
			}
			cand := s.Package + "." + s.Name
			if LangOf(s.Path) == LangCSharp {
				cand = s.Package
				if k := "cs|" + s.Name + "|" + cand; !seen[k] {
					seen[k] = true
					cs[s.Name] = append(cs[s.Name], cand)
				}
				continue
			}
			if k := "jvm|" + s.Name + "|" + cand; !seen[k] {
				seen[k] = true
				jvm[s.Name] = append(jvm[s.Name], cand)
			}
		}
	}
	for _, m := range []map[string][]string{jvm, cs} {
		for _, c := range m {
			sort.Strings(c)
		}
	}
	return map[string]imports.Map{
		LangJava: imports.Map(jvm), LangKotlin: imports.Map(jvm), LangCSharp: imports.Map(cs),
	}
}

// Symbols devuelve los símbolos indexados de la ruta relativa rel (nil si no
// está indexada).
func (ix *Index) Symbols(rel string) []Symbol {
	e := ix.view().files[rel]
	if e == nil {
		return nil
	}
	return append([]Symbol(nil), e.f.Symbols...)
}
