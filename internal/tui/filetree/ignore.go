package filetree

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// alwaysIgnored se ignora en cualquier nivel: .git y .wari (archivos o
// carpetas) y las carpetas de salida de compilación.
var (
	alwaysIgnored    = map[string]bool{".git": true, ".wari": true}
	alwaysIgnoredDir = map[string]bool{"bin": true, "target": true, "obj": true, "build": true}
)

// rule es una línea de .gitignore ya interpretada.
type rule struct {
	pattern  string
	dirOnly  bool // terminaba en "/"
	anchored bool // contenía "/": se compara contra la ruta relativa
}

// ignorer decide qué se oculta. Soporta un subconjunto simple de .gitignore:
// el .gitignore de la raíz con patrones de glob (*, ?, [..]) que se comparan
// con el nombre (sin "/") o con la ruta relativa (con "/"), "/" inicial,
// "/" final (solo carpetas) y "**/" inicial. No soporta negaciones ("!"), "**"
// intermedio ni .gitignore anidados: lo no soportado se omite, de modo que
// solo se oculta de más con patrones que sí se entendieron.
type ignorer struct {
	rules []rule
}

// newIgnorer lee <root>/.gitignore si existe.
func newIgnorer(root string) *ignorer {
	ig := &ignorer{}
	f, err := os.Open(filepath.Join(root, ".gitignore"))
	if err != nil {
		return ig
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(strings.TrimSpace(sc.Text()), "\r")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		r := rule{}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		line = strings.TrimPrefix(line, "**/")
		if strings.HasPrefix(line, "/") {
			r.anchored = true
			line = strings.TrimLeft(line, "/")
		} else if strings.Contains(line, "/") {
			r.anchored = true
		}
		if line == "" || strings.Contains(line, "**") {
			continue
		}
		r.pattern = line
		ig.rules = append(ig.rules, r)
	}
	return ig
}

// ignored indica si la entrada de nombre name y ruta relativa rel (con "/")
// debe ocultarse.
func (ig *ignorer) ignored(rel, name string, isDir bool) bool {
	if alwaysIgnored[name] || (isDir && alwaysIgnoredDir[name]) {
		return true
	}
	for _, r := range ig.rules {
		if r.dirOnly && !isDir {
			continue
		}
		target := name
		if r.anchored {
			target = rel
		}
		if ok, _ := path.Match(r.pattern, target); ok {
			return true
		}
	}
	return false
}

// ListFiles devuelve las rutas relativas (con "/", ordenadas) de los archivos
// bajo root que no se ignoran, hasta limit (0: sin límite). Sirve para la
// búsqueda de archivos por nombre.
func ListFiles(root string, limit int) []string {
	ig := newIgnorer(root)
	var out []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ig.ignored(rel, d.Name(), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		out = append(out, rel)
		if limit > 0 && len(out) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out
}
