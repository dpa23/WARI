package index

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dpa23/wari/internal/proc"
)

// universal-ctags como fuente alternativa de los archivos del proyecto.
//
// Se invoca por lotes de archivos con
//
//	ctags --options=NONE --output-format=json --fields=+nKsS --sort=no -o - ./a ./b ...
//
// y cada línea de la salida es un objeto JSON, por ejemplo
//
//	{"_type": "tag", "name": "Svc", "path": "./A.cs", "pattern": "/^  public class Svc {$/",
//	 "line": 2, "kind": "class", "scope": "Foo.Bar", "scopeKind": "namespace"}
//
// (formato verificado con Universal Ctags 6.2.1). Limitaciones respecto al
// analizador propio: no trae columna (se busca el nombre en el patrón de la
// línea), ni anotaciones ni importaciones, y para Java los records salen como
// métodos. --options=NONE evita que un .ctags.d del proyecto cambie la
// ejecución. El proceso se lanza con internal/proc, así que un límite de
// tiempo lo termina con todos sus descendientes.

const (
	ctagsBatch   = 120
	ctagsTimeout = 60 * time.Second
	ctagsVerTime = 5 * time.Second
	// ctagsMaxOut acota lo que se lee de la salida de un lote.
	ctagsMaxOut = 64 << 20
)

// ErrNoCtags indica que no hay un universal-ctags utilizable.
var ErrNoCtags = errors.New("index: universal-ctags no está instalado")

// CtagsVersion busca ctags en el PATH y comprueba con --version que sea
// Universal Ctags (el ctags clásico no sirve: no tiene salida JSON). Devuelve
// su ruta y su versión ("6.2.1").
func CtagsVersion(ctx context.Context) (binPath, version string, err error) {
	bin, err := exec.LookPath("ctags")
	if err != nil {
		return "", "", ErrNoCtags
	}
	ctx, cancel := context.WithTimeout(ctx, ctagsVerTime)
	defer cancel()
	p, err := proc.Start(ctx, proc.Spec{Path: bin, Args: []string{"--version"}})
	if err != nil {
		return "", "", err
	}
	defer p.Close()
	out, _ := io.ReadAll(io.LimitReader(p.Stdout(), 4096))
	_ = p.Wait()
	line, _, _ := strings.Cut(string(out), "\n")
	rest, ok := strings.CutPrefix(line, "Universal Ctags ")
	if !ok {
		return "", "", fmt.Errorf("%w (%s no es Universal Ctags)", ErrNoCtags, bin)
	}
	version, _, _ = strings.Cut(rest, ",")
	return bin, strings.TrimSpace(version), nil
}

// ctags detecta ctags una sola vez por índice.
func (ix *Index) ctags() (binPath, version string, ok bool) {
	ix.ctagsOnce.Do(func() {
		if bin, ver, err := CtagsVersion(context.Background()); err == nil {
			ix.ctagsPath, ix.ctagsVer = bin, ver
		}
	})
	return ix.ctagsPath, ix.ctagsVer, ix.ctagsPath != ""
}

// parseCtags analiza todo con ctags por lotes y deja el resultado en out. Los
// archivos de un lote que falla no se agregan: quien llama los analiza con el
// analizador propio (y se cuentan en st.Fallback).
func (ix *Index) parseCtags(ctx context.Context, todo []candidate, out map[string]*File, st *Stats) {
	bin, _, _ := ix.ctags()
	for len(todo) > 0 && ctx.Err() == nil {
		n := min(ctagsBatch, len(todo))
		batch := todo[:n]
		todo = todo[n:]
		rels := make([]string, len(batch))
		for i, c := range batch {
			rels[i] = c.rel
		}
		tags, err := runCtags(ctx, bin, ix.opts.Root, rels)
		if err != nil {
			st.Fallback += len(batch)
			continue
		}
		for _, c := range batch {
			out[c.rel] = fileFromTags(c.rel, tags[c.rel])
		}
	}
}

type ctagsTag struct {
	Type      string `json:"_type"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Pattern   string `json:"pattern"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	ScopeKind string `json:"scopeKind"`
	Signature string `json:"signature"`
}

// runCtags ejecuta ctags sobre las rutas relativas rels (bajo root) y agrupa
// sus etiquetas por ruta relativa.
func runCtags(ctx context.Context, bin, root string, rels []string) (map[string][]ctagsTag, error) {
	ctx, cancel := context.WithTimeout(ctx, ctagsTimeout)
	defer cancel()
	args := []string{"--options=NONE", "--output-format=json", "--fields=+nKsS", "--sort=no", "-o", "-"}
	for _, r := range rels {
		args = append(args, "./"+r)
	}
	p, err := proc.Start(ctx, proc.Spec{Path: bin, Args: args, Dir: root})
	if err != nil {
		return nil, err
	}
	defer p.Close()
	defer p.Kill()
	out, err := io.ReadAll(io.LimitReader(p.Stdout(), ctagsMaxOut))
	if err != nil {
		return nil, err
	}
	if err := p.Wait(); err != nil {
		return nil, fmt.Errorf("ctags: %w: %s", err, strings.TrimSpace(p.Stderr()))
	}
	return parseTags(out)
}

// parseTags lee la salida JSON (un objeto por línea) y agrupa las etiquetas
// reales por ruta.
func parseTags(out []byte) (map[string][]ctagsTag, error) {
	res := map[string][]ctagsTag{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var t ctagsTag
		if err := json.Unmarshal(line, &t); err != nil {
			return nil, fmt.Errorf("ctags: salida inválida: %w", err)
		}
		if t.Type != "tag" || t.Name == "" || t.Line <= 0 {
			continue
		}
		p := path.Clean(t.Path)
		res[p] = append(res[p], t)
	}
	return res, sc.Err()
}

// ctagsKind traduce el tipo de ctags; "" si no se indexa.
func ctagsKind(lang string, t ctagsTag) Kind {
	switch t.Kind {
	case "package":
		return KindPackage
	case "namespace":
		return KindNamespace
	case "class":
		return KindClass
	case "interface":
		return KindInterface
	case "enum":
		return KindEnum
	case "struct":
		return KindStruct
	case "object":
		return KindObject
	case "record":
		return KindRecord
	case "annotation":
		return KindAnnotation
	case "typealias":
		return KindTypeAlias
	case "delegate":
		return KindDelegate
	case "event":
		return KindEvent
	case "property":
		return KindProperty
	case "enumConstant", "enumerator":
		return KindConstant
	case "field":
		return KindField
	case "constant", "variable":
		if lang == LangKotlin {
			return KindProperty
		}
		return KindField
	case "method":
		if t.ScopeKind != "" && (t.Scope == t.Name || strings.HasSuffix(t.Scope, "."+t.Name)) {
			return KindConstructor
		}
		return KindMethod
	}
	return ""
}

// fileFromTags arma el File de rel con sus etiquetas. El package o namespace
// sale de las etiquetas package/namespace del archivo; el contenedor, del
// scope de cada etiqueta sin ese prefijo.
func fileFromTags(rel string, tags []ctagsTag) *File {
	lang := LangOf(rel)
	f := &File{Path: rel, Lang: lang}
	var pkgs []string
	for _, t := range tags {
		if k := ctagsKind(lang, t); k == KindPackage || k == KindNamespace {
			pkgs = append(pkgs, t.Name)
		}
	}
	if len(pkgs) > 0 {
		f.Package = pkgs[0]
	}
	for _, t := range tags {
		kind := ctagsKind(lang, t)
		if kind == "" {
			continue
		}
		s := Symbol{Name: t.Name, Kind: kind, Path: rel, Line: t.Line - 1, Signature: t.Signature}
		s.Col = tagColumn(t)
		switch kind {
		case KindPackage, KindNamespace:
			s.Package = t.Name
		default:
			pkg, container := splitScope(f.Package, pkgs, t.Scope)
			s.Package, s.Container = pkg, container
		}
		f.Symbols = append(f.Symbols, s)
	}
	return f
}

// splitScope separa el scope de ctags en el package o namespace (el más
// largo de pkgs que lo prefija; def si ninguno) y el resto.
func splitScope(def string, pkgs []string, scope string) (pkg, container string) {
	best := ""
	for _, p := range pkgs {
		if (scope == p || strings.HasPrefix(scope, p+".")) && len(p) > len(best) {
			best = p
		}
	}
	if best == "" {
		return def, scope
	}
	return best, strings.TrimPrefix(strings.TrimPrefix(scope, best), ".")
}

// tagColumn busca el nombre en el patrón de la línea ("/^texto$/") y
// devuelve su columna en runas (0 si no aparece).
func tagColumn(t ctagsTag) int {
	pat := strings.TrimSuffix(strings.TrimPrefix(t.Pattern, "/^"), "/")
	pat = strings.TrimSuffix(pat, "$")
	pat = strings.NewReplacer(`\/`, "/", `\\`, `\`).Replace(pat)
	i := indexWord(pat, t.Name)
	if i < 0 {
		return 0
	}
	return utf8.RuneCountInString(pat[:i])
}

// indexWord busca name como identificador completo en s.
func indexWord(s, name string) int {
	for from := 0; from <= len(s)-len(name); {
		i := strings.Index(s[from:], name)
		if i < 0 {
			return -1
		}
		i += from
		before := i == 0 || !isIdentPart(s[i-1])
		after := i+len(name) >= len(s) || !isIdentPart(s[i+len(name)])
		if before && after {
			return i
		}
		from = i + 1
	}
	return -1
}
