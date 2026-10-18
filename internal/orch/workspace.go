package orch

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/workspace/owner"
	"github.com/dpa23/wari/internal/workspace/watch"
)

// Seguridad de cambios (fase 6). Los agentes editan el árbol real; para no
// perder trabajo, el supervisor:
//
//   - guarda un checkpoint del archivo cuando llega FileEditProposed, antes
//     de reenviar el evento;
//   - lleva la propiedad de archivos (owner): quien propone editar algo ya
//     reclamado por otro agente choca y queda pausado, con un aviso;
//   - observa el árbol (watch) y atribuye cada cambio a un agente.
//
// Límite conocido: FileEditProposed llega cuando la CLI emite la llamada a la
// herramienta, y la CLI puede ejecutarla antes de que el checkpoint termine
// de copiar el archivo. Con PermissionAsk (el valor por defecto) la edición
// se deniega, así que el riesgo existe solo con AcceptEdits.

const (
	// ownWriteTTL es cuánto tiempo se ignora el cambio que el watcher vea
	// tras una escritura propia (revertir, guardar desde el editor).
	ownWriteTTL = 2 * time.Second
)

// ErrConflict indica un choque de archivos.
var ErrConflict = errors.New("orch: archivo reclamado por otro agente")

// ErrNoCheckpoints indica que no hay almacén de checkpoints.
var ErrNoCheckpoints = errors.New("orch: sin almacén de checkpoints")

// Checkpointer es lo que el supervisor necesita del almacén de checkpoints
// (lo implementa *checkpoint.Store). Las rutas son relativas a la raíz.
type Checkpointer interface {
	Save(agent string, turn int, rel string) error
	RevertFile(agent string, turn int, rel string) error
	RevertTurn(agent string, turn int) error
	RevertAgent(agent string) error
	// Diff devuelve el contenido del checkpoint y el actual; nil si el
	// archivo no existía (o no existe ya).
	Diff(agent string, turn int, rel string) (old, current []byte, err error)
}

// Workspace configura el espacio de trabajo del supervisor.
type Workspace struct {
	// Root es la raíz del proyecto. Con ella se relativizan las rutas de los
	// agentes y se observa el árbol.
	Root string
	// Store guarda los checkpoints; nil los desactiva.
	Store Checkpointer
	// RunID distingue esta ejecución de WARI en las claves de checkpoint
	// (los IDs de agente vuelven a empezar en 1 en cada ejecución).
	RunID string
	// Watch activa el watcher del árbol; Ignore (puede ser nil) decide qué
	// ignora y Debounce (0: el predeterminado) cuánto agrupa los cambios.
	Watch    bool
	Ignore   func(rel string, isDir bool) bool
	Debounce time.Duration
	// OnWatchError recibe los errores no fatales del watcher; puede ser nil.
	OnWatchError func(error)
}

// Edit es un archivo que un agente propuso editar en un turno.
type Edit struct {
	// Turn es el turno (el valor de Snapshot.Turns cuando llegó).
	Turn int
	// Path es relativa a la raíz, con "/" (absoluta si quedó fuera).
	Path string
	// Saved indica que hay checkpoint y se puede revertir y ver el diff.
	Saved bool
	// Detected indica que el cambio lo detectó el watcher (p. ej. una
	// edición hecha por un comando de shell): no tiene checkpoint.
	Detected bool
}

// NoticeKind distingue los avisos del espacio de trabajo.
type NoticeKind int

const (
	// NoticeConflict: Agent quiso editar Path, reclamado por Holder. Agent
	// queda pausado (Late: no se pudo evitar, el cambio ya ocurrió).
	NoticeConflict NoticeKind = iota + 1
	// NoticeTakeover: Agent tomó Path, que era de Holder.
	NoticeTakeover
	// NoticeChanged: el watcher vio un cambio en Path atribuido a Agent (0:
	// desconocido).
	NoticeChanged
	// NoticeCheckpointFailed: no se pudo guardar el checkpoint de Path.
	NoticeCheckpointFailed
)

// Attribution dice cómo se atribuyó un cambio.
type Attribution int

const (
	// AttrUnknown: no se sabe quién fue.
	AttrUnknown Attribution = iota
	// AttrProposal: el agente propuso editar ese archivo en su turno en curso.
	AttrProposal
	// AttrSoleActive: no hubo propuesta, pero era el único agente corriendo.
	AttrSoleActive
	// AttrUser: lo escribió WARI a petición del usuario (guardar, revertir).
	AttrUser
)

// Notice es un aviso del espacio de trabajo.
type Notice struct {
	Kind   NoticeKind
	Agent  ID
	Holder ID
	// Path es relativa a la raíz, con "/".
	Path string
	Op   string
	Attr Attribution
	Late bool
	// Text es el aviso listo para mostrar.
	Text string
}

type proposal struct {
	turnSeq int
	seq     uint64
}

// AttachWorkspace configura checkpoints, propiedad de archivos y watcher.
// Hay que llamarlo antes de Submit. Devuelve el error del watcher; los
// checkpoints y la propiedad quedan configurados de todos modos.
func (s *Supervisor) AttachWorkspace(w Workspace) error {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrClosed
	}
	s.ws = w
	if !w.Watch || w.Root == "" || s.watcher != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	wt, err := watch.New(watch.Options{
		Root: w.Root, Ignore: w.Ignore, Debounce: w.Debounce, OnError: w.OnWatchError,
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closing || s.watcher != nil {
		s.mu.Unlock()
		_ = wt.Close()
		return nil
	}
	s.watcher = wt
	s.wg.Add(1)
	s.mu.Unlock()
	go s.watchLoop(wt)
	return nil
}

// agentKey es la clave del agente en el almacén de checkpoints.
func (s *Supervisor) agentKey(id ID) string { return fmt.Sprintf("%sa%d", s.ws.RunID, id) }

// onEditProposed atiende FileEditProposed: checkpoint, propiedad y reenvío.
// Corre en la goroutine lectora del agente, así que el orden de sus eventos
// se conserva; el checkpoint (E/S de disco) se hace con el mutex suelto.
func (s *Supervisor) onEditProposed(e *entry, ev agent.FileEditProposed) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	store, root, key, turn := s.ws.Store, s.ws.Root, s.agentKey(e.id), e.turns
	s.mu.Unlock()

	rel, inside := owner.Rel(root, ev.Path)
	var saveErr error
	saved := false
	if store != nil && inside && root != "" {
		if saveErr = store.Save(key, turn, rel); saveErr == nil {
			saved = true
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	if saveErr != nil {
		s.pushNoticeLocked(e, Notice{
			Kind: NoticeCheckpointFailed, Agent: e.id, Path: rel,
			Text: fmt.Sprintf("sin checkpoint de %s: %v", rel, saveErr),
		})
	}
	s.recordEditLocked(e, Edit{Turn: turn, Path: rel, Saved: saved})
	if e.proposals == nil {
		e.proposals = make(map[string]proposal)
	}
	s.propSeq++
	e.proposals[rel] = proposal{turnSeq: e.turnSeq, seq: s.propSeq}

	if c := s.owners.Claim(int(e.id), rel); c != nil {
		holder := s.nameOfLocked(ID(c.Holder))
		e.conflicts = append(e.conflicts, c.Claimed)
		s.pushNoticeLocked(e, Notice{
			Kind: NoticeConflict, Agent: e.id, Holder: ID(c.Holder), Path: rel,
			Text: fmt.Sprintf("choque: %s quiere editar %s, reclamado por %s", e.name, rel, holder),
		})
		if e.state == Running {
			s.goLocked(func() { s.pauseForConflict(e) })
		}
	}
	s.emitLocked(e, ev)
}

// pauseForConflict pausa al agente que chocó. Si no se puede (ya no corre,
// por ejemplo) no hace nada; si el agente falla al pausar, avisa.
func (s *Supervisor) pauseForConflict(e *entry) {
	err := s.Pause(e.id)
	if err == nil || errors.Is(err, ErrState) || errors.Is(err, ErrClosed) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushNoticeLocked(e, Notice{
		Kind: NoticeConflict, Agent: e.id, Late: true,
		Text: fmt.Sprintf("no se pudo pausar a %s tras el choque: %v", e.name, err),
	})
}

// recordEditLocked agrega la edición sin repetir (turno, archivo).
func (s *Supervisor) recordEditLocked(e *entry, ed Edit) {
	for i, x := range e.edits {
		if x.Turn == ed.Turn && x.Path == ed.Path {
			e.edits[i].Saved = e.edits[i].Saved || ed.Saved
			return
		}
	}
	e.edits = append(e.edits, ed)
}

func (s *Supervisor) nameOfLocked(id ID) string {
	if id >= 1 && int(id) <= len(s.entries) {
		return s.entries[id-1].name
	}
	return fmt.Sprintf("agente %d", id)
}

// pushNoticeLocked publica un aviso del agente e (nil: sin agente).
func (s *Supervisor) pushNoticeLocked(e *entry, n Notice) {
	u := Update{Notice: &n}
	if e != nil {
		u.ID, u.Snapshot = e.id, s.snapshotLocked(e)
	}
	s.pushLocked(u)
}

// releaseLocked libera lo que el agente reclamó y descarta sus choques
// pendientes.
func (s *Supervisor) releaseLocked(e *entry) {
	e.conflicts = nil
	s.owners.Release(int(e.id))
}

// takeoverLocked pasa al agente lo que un choque le negó: es lo que significa
// reanudarlo (la decisión del usuario).
func (s *Supervisor) takeoverLocked(e *entry) {
	pending := e.conflicts
	e.conflicts = nil
	for _, p := range pending {
		prev, claimed := s.owners.Takeover(int(e.id), p)
		if prev == 0 || prev == int(e.id) {
			continue
		}
		s.pushNoticeLocked(e, Notice{
			Kind: NoticeTakeover, Agent: e.id, Holder: ID(prev), Path: claimed,
			Text: fmt.Sprintf("%s toma %s, que era de %s", e.name, claimed, s.nameOfLocked(ID(prev))),
		})
	}
}

// Claim reclama path (archivo, o carpeta si termina en "/") para el agente.
// Devuelve ErrConflict (envuelto) si otro agente ya lo reclamó.
func (s *Supervisor) Claim(id ID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.findLocked(id); err != nil {
		return err
	}
	rel, _ := owner.Rel(s.ws.Root, path)
	if c := s.owners.Claim(int(id), rel); c != nil {
		return fmt.Errorf("%w: %s reclamado por %s", ErrConflict, c.Claimed, s.nameOfLocked(ID(c.Holder)))
	}
	return nil
}

// Release libera todo lo que el agente reclamó (la decisión del usuario de
// soltarlo sin detener al agente).
func (s *Supervisor) Release(id ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.findLocked(id)
	if err != nil {
		return err
	}
	s.releaseLocked(e)
	return nil
}

// Claims devuelve lo que el agente tiene reclamado.
func (s *Supervisor) Claims(id ID) []string { return s.owners.Claims(int(id)) }

// Holder devuelve quién reclamó path (0 si nadie).
func (s *Supervisor) Holder(path string) ID {
	s.mu.Lock()
	root := s.ws.Root
	s.mu.Unlock()
	rel, _ := owner.Rel(root, path)
	if h, _, ok := s.owners.Holder(rel); ok {
		return ID(h)
	}
	return 0
}

// NoteUserWrite avisa de que WARI mismo escribió path (guardar desde el
// editor): el watcher no lo atribuirá a ningún agente.
func (s *Supervisor) NoteUserWrite(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noteOwnWriteLocked(path)
}

func (s *Supervisor) noteOwnWriteLocked(path string) {
	rel, inside := owner.Rel(s.ws.Root, path)
	if !inside {
		return
	}
	now := s.now()
	s.ownWrites[rel] = now.Add(ownWriteTTL)
	for k, until := range s.ownWrites { // poda
		if until.Before(now) {
			delete(s.ownWrites, k)
		}
	}
}

// Attribute dice a qué agente se atribuye un cambio en path: el último con un
// turno en curso que propuso editarlo; si no hay, el único agente corriendo;
// si hay varios corriendo sin propuesta, ninguno (0, AttrUnknown).
func (s *Supervisor) Attribute(path string) (ID, Attribution) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, _ := owner.Rel(s.ws.Root, path)
	return s.attributeLocked(rel)
}

func (s *Supervisor) attributeLocked(rel string) (ID, Attribution) {
	if until, ok := s.ownWrites[rel]; ok && !until.Before(s.now()) {
		return 0, AttrUser
	}
	var best *entry
	var bestSeq uint64
	running, sole := 0, (*entry)(nil)
	for _, e := range s.entries {
		if e.state != Running && e.state != Paused {
			continue
		}
		if p, ok := e.proposals[rel]; ok && p.turnSeq == e.turnSeq && p.seq > bestSeq {
			best, bestSeq = e, p.seq
		}
		if e.state == Running {
			running++
			sole = e
		}
	}
	switch {
	case best != nil:
		return best.id, AttrProposal
	case running == 1:
		return sole.id, AttrSoleActive
	}
	return 0, AttrUnknown
}

// watchLoop convierte los cambios del watcher en avisos.
func (s *Supervisor) watchLoop(w *watch.Watcher) {
	defer s.wg.Done()
	for c := range w.Changes() {
		s.onChange(c)
	}
}

func (s *Supervisor) onChange(c watch.Change) {
	// Los temporales de WARI (guardado atómico del editor y de los
	// checkpoints) no son cambios de nadie.
	if base := path.Base(c.Path); strings.HasPrefix(base, ".wari-") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	id, how := s.attributeLocked(c.Path)
	if how == AttrUser {
		return
	}
	var e *entry
	if id != 0 {
		e = s.entries[id-1]
		if how == AttrSoleActive && c.Op != watch.Remove {
			s.recordEditLocked(e, Edit{Turn: e.turns, Path: c.Path, Detected: true})
		}
	}
	text := fmt.Sprintf("✎ %s (%s", c.Path, opText(c.Op))
	switch how {
	case AttrSoleActive:
		text += ", único agente activo"
	case AttrUnknown:
		text += ", origen desconocido"
	}
	text += ")"
	s.pushNoticeLocked(e, Notice{Kind: NoticeChanged, Agent: id, Path: c.Path, Op: c.Op.String(), Attr: how, Text: text})

	// Sin propuesta previa no se pudo avisar antes: si lo atribuido a un
	// agente es de otro, se avisa tarde (no se pausa: la atribución es una
	// suposición y el cambio pudo venir de fuera de WARI).
	if how == AttrSoleActive {
		if h, claimed, ok := s.owners.Holder(c.Path); ok && h != int(id) {
			s.pushNoticeLocked(e, Notice{
				Kind: NoticeConflict, Agent: id, Holder: ID(h), Path: c.Path, Attr: how, Late: true,
				Text: fmt.Sprintf("choque: %s cambió %s, reclamado por %s (ya ocurrió)",
					e.name, claimed, s.nameOfLocked(ID(h))),
			})
		}
	}
}

func opText(o watch.Op) string {
	switch o {
	case watch.Create:
		return "creado"
	case watch.Remove:
		return "borrado"
	}
	return "modificado"
}

// --- Checkpoints: edits, diff y revertir ---

// Edits devuelve las ediciones del agente, en orden de llegada.
func (s *Supervisor) Edits(id ID) []Edit {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id < 1 || int(id) > len(s.entries) {
		return nil
	}
	return append([]Edit(nil), s.entries[id-1].edits...)
}

// storeFor devuelve el almacén y la clave del agente, o un error si no se
// puede usar ahora. Con needIdle, rechaza al agente que está corriendo: sus
// archivos cambian bajo los pies del revert.
func (s *Supervisor) storeFor(id ID, needIdle bool) (Checkpointer, string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.findLocked(id)
	if err != nil {
		return nil, "", "", err
	}
	if s.ws.Store == nil {
		return nil, "", "", ErrNoCheckpoints
	}
	if needIdle && e.state == Running {
		return nil, "", "", fmt.Errorf("%w: %s está corriendo; páusalo o detenlo antes de revertir", ErrState, e.name)
	}
	return s.ws.Store, s.agentKey(id), s.ws.Root, nil
}

// Diff devuelve el contenido del checkpoint del archivo en el turno y el
// actual (nil si no existía o ya no existe). path puede ser relativa o
// absoluta.
func (s *Supervisor) Diff(id ID, turn int, path string) (old, current []byte, err error) {
	store, key, root, err := s.storeFor(id, false)
	if err != nil {
		return nil, nil, err
	}
	rel, _ := owner.Rel(root, path)
	return store.Diff(key, turn, rel)
}

// RevertFile deja el archivo como estaba en el checkpoint del turno.
func (s *Supervisor) RevertFile(id ID, turn int, path string) error {
	store, key, root, err := s.storeFor(id, true)
	if err != nil {
		return err
	}
	rel, _ := owner.Rel(root, path)
	s.mu.Lock()
	s.noteOwnWriteLocked(filepath.Join(root, filepath.FromSlash(rel)))
	s.mu.Unlock()
	return store.RevertFile(key, turn, rel)
}

// RevertTurn revierte todos los archivos que el agente tocó en el turno.
func (s *Supervisor) RevertTurn(id ID, turn int) error {
	store, key, root, err := s.storeFor(id, true)
	if err != nil {
		return err
	}
	s.noteOwnWrites(id, root, func(ed Edit) bool { return ed.Turn == turn })
	return store.RevertTurn(key, turn)
}

// RevertAgent revierte todo lo que el agente editó, en todos sus turnos.
func (s *Supervisor) RevertAgent(id ID) error {
	store, key, root, err := s.storeFor(id, true)
	if err != nil {
		return err
	}
	s.noteOwnWrites(id, root, func(Edit) bool { return true })
	return store.RevertAgent(key)
}

// noteOwnWrites marca como escritura propia los archivos de las ediciones del
// agente que cumplan pred, para que el watcher no atribuya el revert.
func (s *Supervisor) noteOwnWrites(id ID, root string, pred func(Edit) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ed := range s.entries[id-1].edits {
		if pred(ed) && ed.Saved {
			s.noteOwnWriteLocked(filepath.Join(root, filepath.FromSlash(ed.Path)))
		}
	}
}
