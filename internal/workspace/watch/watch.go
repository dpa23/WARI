// Package watch observa el árbol del proyecto con fsnotify y emite los
// cambios de archivos (ruta relativa y operación) con un debounce corto: una
// ráfaga de eventos sobre el mismo archivo (guardar con temporal más rename,
// escrituras en trozos) llega como un solo cambio. Quién hizo el cambio no se
// decide aquí: lo atribuye el supervisor.
package watch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// DefaultDebounce es la espera por omisión antes de emitir un cambio.
const DefaultDebounce = 50 * time.Millisecond

// changeBuffer es la capacidad del canal de cambios.
const changeBuffer = 1024

// Op es la operación observada sobre un archivo.
type Op uint8

const (
	// Write: el archivo existente cambió de contenido.
	Write Op = iota + 1
	// Create: el archivo apareció (puede incluir escrituras posteriores).
	Create
	// Remove: el archivo desapareció (borrado o movido fuera).
	Remove
)

func (o Op) String() string {
	switch o {
	case Write:
		return "write"
	case Create:
		return "create"
	case Remove:
		return "remove"
	}
	return "unknown"
}

// Change es un cambio de un archivo.
type Change struct {
	// Path es relativa a la raíz, con "/".
	Path string
	Op   Op
}

// Options configura el watcher.
type Options struct {
	// Root es la raíz a observar (obligatoria).
	Root string
	// Ignore dice si una ruta relativa (con "/") se ignora; se llama con cada
	// carpeta antes de vigilarla y con cada archivo. nil usa IgnoreDefault.
	// Una carpeta ignorada no se recorre.
	Ignore func(rel string, isDir bool) bool
	// Debounce es la espera antes de emitir; <= 0 usa DefaultDebounce.
	Debounce time.Duration
	// OnError recibe los errores no fatales (límite de watches, desbordes de
	// la cola del sistema). Puede ser nil.
	OnError func(error)
}

// IgnoreDefault ignora .git y .wari en cualquier nivel.
func IgnoreDefault(rel string, _ bool) bool {
	name := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		name = rel[i+1:]
	}
	return name == ".git" || name == ".wari"
}

// Watcher observa un árbol de forma recursiva.
type Watcher struct {
	root     string
	ignore   func(string, bool) bool
	debounce time.Duration
	onError  func(error)

	fsw     *fsnotify.Watcher
	changes chan Change
	done    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// New empieza a observar root. Hay que cerrarlo con Close.
func New(o Options) (*Watcher, error) {
	if o.Root == "" {
		return nil, errors.New("watch: falta la raíz")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errors.New("watch: la raíz no es un directorio")
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root:     root,
		ignore:   o.Ignore,
		debounce: o.Debounce,
		onError:  o.OnError,
		fsw:      fsw,
		changes:  make(chan Change, changeBuffer),
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	if w.ignore == nil {
		w.ignore = IgnoreDefault
	}
	if w.debounce <= 0 {
		w.debounce = DefaultDebounce
	}
	w.addTree(root)
	go w.loop()
	return w, nil
}

// Changes entrega los cambios. Se cierra con Close.
func (w *Watcher) Changes() <-chan Change { return w.changes }

// Close detiene el watcher y cierra Changes. Es idempotente.
func (w *Watcher) Close() error {
	var err error
	w.once.Do(func() {
		close(w.done)
		err = w.fsw.Close()
		<-w.stopped
	})
	return err
}

func (w *Watcher) fail(err error) {
	if w.onError != nil {
		w.onError(err)
	}
}

// relOf devuelve la ruta relativa (con "/") de abs, o "" si es la raíz o está
// fuera de ella.
func (w *Watcher) relOf(abs string) string {
	rel, err := filepath.Rel(w.root, abs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// ignoredPath comprueba rel y todas sus carpetas ancestras.
func (w *Watcher) ignoredPath(rel string, isDir bool) bool {
	parts := strings.Split(rel, "/")
	for i := 1; i < len(parts); i++ {
		if w.ignore(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return w.ignore(rel, isDir)
}

// addTree vigila dir y todas sus carpetas no ignoradas. Devuelve los archivos
// que encontró (rutas relativas), para reportar lo que ya existía cuando la
// carpeta se creó entre el evento y el vigilado.
func (w *Watcher) addTree(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel := w.relOf(p)
		if d.IsDir() {
			if rel != "" && w.ignore(rel, true) {
				return filepath.SkipDir
			}
			if err := w.fsw.Add(p); err != nil {
				w.fail(err)
			}
			return nil
		}
		if rel != "" && !w.ignore(rel, false) {
			files = append(files, rel)
		}
		return nil
	})
	return files
}

type pending struct {
	op  Op
	due time.Time
}

// merge combina la operación acumulada con una nueva del mismo archivo.
func merge(old, op Op) Op {
	switch {
	case old == 0:
		return op
	case op == Remove:
		if old == Create { // apareció y desapareció dentro de la ventana
			return 0
		}
		return Remove
	case old == Remove: // borrado y vuelto a crear: el contenido cambió
		return Write
	case old == Create:
		return Create
	}
	return op
}

func (w *Watcher) loop() {
	defer close(w.stopped)
	defer close(w.changes)

	items := map[string]*pending{}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false

	note := func(rel string, op Op) {
		it := items[rel]
		if it == nil {
			it = &pending{due: time.Now().Add(w.debounce)}
			items[rel] = it
		}
		it.op = merge(it.op, op)
		if !armed {
			timer.Reset(w.debounce)
			armed = true
		}
	}

	flush := func() bool {
		now := time.Now()
		var next time.Time
		for rel, it := range items {
			if it.due.After(now) {
				if next.IsZero() || it.due.Before(next) {
					next = it.due
				}
				continue
			}
			delete(items, rel)
			if it.op == 0 {
				continue
			}
			select {
			case w.changes <- Change{Path: rel, Op: it.op}:
			case <-w.done:
				return false
			}
		}
		if next.IsZero() {
			armed = false
		} else {
			timer.Reset(time.Until(next))
		}
		return true
	}

	for {
		select {
		case <-w.done:
			return
		case <-timer.C:
			if !flush() {
				return
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.fail(err)
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev, note)
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event, note func(string, Op)) {
	rel := w.relOf(ev.Name)
	if rel == "" || ev.Op == fsnotify.Chmod {
		return
	}
	gone := ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)
	if gone {
		// No se puede saber si era carpeta: se informa como archivo. Si era
		// carpeta, fsnotify retira solo su watch.
		if !w.ignoredPath(rel, false) {
			note(rel, Remove)
		}
		return
	}
	st, err := os.Lstat(ev.Name)
	if err != nil {
		// Ya desapareció (típico de un temporal que se renombra enseguida):
		// se anota igual, y su Remove lo cancela si apareció y se fue dentro
		// de la ventana.
		if !w.ignoredPath(rel, false) {
			switch {
			case ev.Has(fsnotify.Create):
				note(rel, Create)
			case ev.Has(fsnotify.Write):
				note(rel, Write)
			}
		}
		return
	}
	if st.IsDir() {
		if ev.Has(fsnotify.Create) && !w.ignoredPath(rel, true) {
			// Lo que ya se escribió dentro antes de vigilarla no genera
			// eventos propios: se informa como creado.
			for _, f := range w.addTree(ev.Name) {
				note(f, Create)
			}
		}
		return
	}
	if w.ignoredPath(rel, false) {
		return
	}
	switch {
	case ev.Has(fsnotify.Create):
		note(rel, Create)
	case ev.Has(fsnotify.Write):
		note(rel, Write)
	}
}
