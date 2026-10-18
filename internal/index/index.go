package index

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Presupuesto por omisión. Un Symbol ocupa del orden de 200 bytes con sus
// cadenas, así que 200 000 símbolos son unas decenas de MiB como tope.
const (
	DefaultMaxFiles    = 20000
	DefaultMaxFileSize = 1 << 20
	DefaultMaxSymbols  = 200000
)

// Options configura un Index.
type Options struct {
	// Root es la raíz del proyecto.
	Root string
	// Ignore indica si una ruta relativa (con "/") y su tipo se omiten; es el
	// criterio de ignorados del árbol (filetree.IgnoreFunc). nil no omite
	// nada.
	Ignore func(rel string, isDir bool) bool
	// MaxFiles, MaxFileSize y MaxSymbols acotan la memoria; cero usa los
	// valores por omisión. Lo que se pasa del presupuesto no se indexa y
	// Stats.Truncated lo avisa.
	MaxFiles    int
	MaxFileSize int64
	MaxSymbols  int
	// Ctags pide usar universal-ctags como fuente de los archivos del
	// proyecto si está en el PATH (ver CtagsVersion). Sin él, o si no es
	// Universal Ctags, se usa el analizador propio.
	Ctags bool
}

// Stats es el resultado de una pasada de Refresh.
type Stats struct {
	// Files y Symbols son lo indexado tras la pasada.
	Files, Symbols int
	// Parsed son los archivos leídos de nuevo (nuevos o cambiados), Removed
	// los que ya no existen y Skipped los omitidos por tamaño, por lectura
	// fallida o por presupuesto.
	Parsed, Removed, Skipped int
	// Truncated indica que se alcanzó MaxFiles o MaxSymbols.
	Truncated bool
	// Fallback son los archivos que ctags no pudo procesar y se analizaron
	// con el analizador propio.
	Fallback int
	// Busy indica que no se hizo nada porque otra pasada estaba en curso.
	Busy bool
	Took time.Duration
	// Source es "go" o, con ctags, "ctags <versión>".
	Source string
}

type fileEntry struct {
	f    *File
	mod  time.Time
	size int64
}

type nameEntry struct{ lower, name string }

// snapshot es una vista inmutable del índice; Refresh publica una nueva.
type snapshot struct {
	files   map[string]*fileEntry
	byName  map[string][]*Symbol
	names   []nameEntry // sin package ni namespace; ordenados por lower
	symbols int
}

// Index es el índice incremental de símbolos de un proyecto. Es seguro
// usarlo desde varias goroutines: las consultas leen una vista inmutable y
// Refresh publica una nueva al terminar, así que nunca bloquean la UI.
type Index struct {
	opts      Options
	refreshMu sync.Mutex
	snap      atomic.Pointer[snapshot]
	stats     atomic.Pointer[Stats]
	ready     atomic.Bool

	ctagsOnce sync.Once
	ctagsPath string
	ctagsVer  string
}

// New crea un índice vacío; llama a Refresh para llenarlo.
func New(opts Options) *Index {
	if abs, err := filepath.Abs(opts.Root); err == nil {
		opts.Root = abs
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = DefaultMaxFiles
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = DefaultMaxFileSize
	}
	if opts.MaxSymbols <= 0 {
		opts.MaxSymbols = DefaultMaxSymbols
	}
	return &Index{opts: opts}
}

// Root devuelve la raíz absoluta del índice.
func (ix *Index) Root() string { return ix.opts.Root }

// Ready indica que ya terminó al menos una pasada de Refresh.
func (ix *Index) Ready() bool { return ix != nil && ix.ready.Load() }

// Stats devuelve el resultado de la última pasada.
func (ix *Index) Stats() Stats {
	if ix == nil {
		return Stats{}
	}
	if s := ix.stats.Load(); s != nil {
		return *s
	}
	return Stats{}
}

// Rel devuelve abs relativa a la raíz y con "/", y si está dentro de ella.
func (ix *Index) Rel(abs string) (string, bool) {
	rel, err := filepath.Rel(ix.opts.Root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// Abs devuelve la ruta absoluta de la ruta relativa rel de un símbolo.
func (ix *Index) Abs(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(ix.opts.Root, filepath.FromSlash(rel))
}

type candidate struct {
	rel  string
	abs  string
	mod  time.Time
	size int64
}

// Refresh recorre el proyecto y vuelve a analizar solo los archivos nuevos o
// cuyo mtime o tamaño cambió; quita los que desaparecieron. Es bloqueante:
// llámalo desde una goroutine. Si ya hay una pasada en curso devuelve
// enseguida Stats{Busy: true}.
func (ix *Index) Refresh(ctx context.Context) Stats {
	if !ix.refreshMu.TryLock() {
		return Stats{Busy: true}
	}
	defer ix.refreshMu.Unlock()
	start := time.Now()

	old := ix.snap.Load()
	if old == nil {
		old = &snapshot{files: map[string]*fileEntry{}}
	}
	st := Stats{Source: "go"}
	useCtags := false
	if ix.opts.Ctags {
		if _, ver, ok := ix.ctags(); ok {
			useCtags = true
			st.Source = "ctags " + ver
		}
	}

	cands, truncated := ix.walk(ctx)
	st.Truncated = truncated

	// Qué hay que analizar de nuevo.
	var todo []candidate
	for _, c := range cands {
		if e := old.files[c.rel]; e != nil && e.mod.Equal(c.mod) && e.size == c.size {
			continue
		}
		if c.size > ix.opts.MaxFileSize {
			st.Skipped++
			continue
		}
		todo = append(todo, c)
	}
	parsed := map[string]*File{}
	if useCtags {
		ix.parseCtags(ctx, todo, parsed, &st)
	}
	for _, c := range todo {
		if ctx.Err() != nil {
			break
		}
		if _, ok := parsed[c.rel]; ok {
			continue
		}
		if f := ix.parseNative(c); f != nil {
			parsed[c.rel] = f
		} else {
			st.Skipped++
		}
	}
	if ctx.Err() != nil {
		// Pasada cancelada: se conserva la vista anterior.
		st.Took = time.Since(start)
		return st
	}

	// Vista nueva, en orden de ruta, dentro del presupuesto de símbolos.
	files := make(map[string]*fileEntry, len(cands))
	total := 0
	for _, c := range cands {
		var e *fileEntry
		if f, ok := parsed[c.rel]; ok {
			e = &fileEntry{f: f, mod: c.mod, size: c.size}
			st.Parsed++
		} else if o := old.files[c.rel]; o != nil && o.mod.Equal(c.mod) && o.size == c.size {
			e = o
		} else {
			continue
		}
		if total+len(e.f.Symbols) > ix.opts.MaxSymbols {
			st.Truncated = true
			st.Skipped++
			continue
		}
		total += len(e.f.Symbols)
		files[c.rel] = e
	}
	for rel := range old.files {
		if _, ok := files[rel]; !ok {
			st.Removed++
		}
	}
	st.Files, st.Symbols = len(files), total
	if st.Parsed > 0 || st.Removed > 0 || ix.snap.Load() == nil {
		ix.snap.Store(buildSnapshot(files, total))
	}
	st.Took = time.Since(start)
	ix.stats.Store(&st)
	ix.ready.Store(true)
	return st
}

// walk lista los archivos fuente del proyecto, en orden de ruta, sin lo que
// ignora el criterio del árbol; solo archivos regulares (no sigue enlaces).
func (ix *Index) walk(ctx context.Context) (out []candidate, truncated bool) {
	root := ix.opts.Root
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
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
		if ix.opts.Ignore != nil && ix.opts.Ignore(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() || LangOf(rel) == "" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if len(out) >= ix.opts.MaxFiles {
			truncated = true
			return filepath.SkipAll
		}
		out = append(out, candidate{rel: rel, abs: p, mod: info.ModTime(), size: info.Size()})
		return nil
	})
	return out, truncated
}

// parseNative lee y analiza un archivo con el analizador propio (nil si no
// se pudo leer o supera el tamaño máximo).
func (ix *Index) parseNative(c candidate) *File {
	f, err := os.Open(c.abs)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ix.opts.MaxFileSize+1))
	if err != nil || int64(len(data)) > ix.opts.MaxFileSize {
		return nil
	}
	return Parse(LangOf(c.rel), c.rel, data)
}

func buildSnapshot(files map[string]*fileEntry, total int) *snapshot {
	s := &snapshot{files: files, byName: make(map[string][]*Symbol, total/2), symbols: total}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := files[p].f
		for i := range f.Symbols {
			sym := &f.Symbols[i]
			s.byName[sym.Name] = append(s.byName[sym.Name], sym)
		}
	}
	s.names = make([]nameEntry, 0, len(s.byName))
	for name, syms := range s.byName {
		for _, sym := range syms {
			if sym.Kind != KindPackage && sym.Kind != KindNamespace {
				s.names = append(s.names, nameEntry{strings.ToLower(name), name})
				break
			}
		}
	}
	sort.Slice(s.names, func(a, b int) bool {
		if s.names[a].lower != s.names[b].lower {
			return s.names[a].lower < s.names[b].lower
		}
		return s.names[a].name < s.names[b].name
	})
	return s
}

// view devuelve la vista actual (vacía si todavía no hay).
func (ix *Index) view() *snapshot {
	if ix == nil {
		return &snapshot{}
	}
	if s := ix.snap.Load(); s != nil {
		return s
	}
	return &snapshot{}
}
