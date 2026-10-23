// Package worktree da a cada agente, de forma opcional, su propio `git
// worktree` bajo <raíz>/.wari/worktrees/<nombre> y una rama wari/<nombre>.
// Los agentes trabajan ahí sin tocar el árbol principal; al terminar, sus
// cambios se revisan (Changes, Diff, Patch) y se fusionan (Merge) o se
// descartan (Remove). Todo git se invoca con internal/proc, con argv en
// slice, sin shell.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dpa23/wari/internal/proc"
)

const (
	gitTimeout   = 2 * time.Minute
	maxFileBytes = 4 << 20  // lectura de un archivo o de su versión en git
	maxGitOut    = 32 << 20 // salida total de un comando git
	maxCopyBytes = 1 << 20  // tope por archivo copiado con include
	maxCopyFiles = 200
	maxPatterns  = 64
	maxIncludeSz = 64 << 10
	maxNameLen   = 40
	subdir       = ".wari/worktrees"
	includeFile  = ".wari/worktree-include"
	copiedFile   = "wari-copied"
)

// defaultInclude son los patrones por omisión. filepath.Glob no soporta `**`,
// así que cada patrón se aplica en la raíz y en src/main/resources/.
var defaultInclude = []string{".env", ".env.*", "application-*.yml", "application-*.yaml", "application-*.properties"}

// Info describe el worktree de un agente.
type Info struct {
	Name   string // nombre saneado, único
	Root   string // árbol principal
	Path   string // carpeta del worktree
	Branch string // wari/<Name>
	Base   string // SHA del HEAD de Root al crear
}

// Change es un archivo cambiado respecto a Base. Status es A, M, D, T o ?
// (sin seguimiento).
type Change struct {
	Status string
	Path   string // relativa al worktree, con '/'
}

// MergeResult es el resultado de fusionar la rama en el árbol principal.
type MergeResult struct {
	Merged    bool
	Conflicts []string
	Message   string
}

// Available comprueba que Root admite worktrees: repositorio git con algún
// commit, `git worktree` disponible y al menos minFree bytes libres.
func Available(ctx context.Context, root string, minFree uint64) error {
	if _, _, err := git(ctx, root, 1<<16, "rev-parse", "--is-inside-work-tree"); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("git no está instalado o no está en el PATH")
		}
		return fmt.Errorf("%s no está dentro de un repositorio git", root)
	}
	if top, _, err := git(ctx, root, 1<<16, "rev-parse", "--show-toplevel"); err == nil {
		got, want := filepath.Clean(strings.TrimSpace(string(top))), filepath.Clean(root)
		if g, err := filepath.EvalSymlinks(got); err == nil {
			got = g
		}
		if w, err := filepath.EvalSymlinks(want); err == nil {
			want = w
		}
		if !samePath(got, want) {
			return fmt.Errorf("el proyecto es una subcarpeta del repositorio %s: abre WARI en la raíz del repositorio para usar worktrees", got)
		}
	}
	if _, _, err := git(ctx, root, 1<<16, "rev-parse", "--verify", "-q", "HEAD^{commit}"); err != nil {
		return errors.New("el repositorio no tiene ningún commit; haz uno antes de usar worktrees")
	}
	if _, _, err := git(ctx, root, 1<<20, "worktree", "list"); err != nil {
		return errors.New("esta versión de git no soporta `git worktree`")
	}
	free, err := freeBytes(root)
	if err != nil {
		return fmt.Errorf("no se pudo medir el espacio libre de %s: %w", root, err)
	}
	if free < minFree {
		return fmt.Errorf("espacio libre insuficiente: %d MiB disponibles, se necesitan %d MiB", free>>20, minFree>>20)
	}
	return nil
}

// LoadInclude lee <root>/.wari/worktree-include: un patrón glob por línea,
// `#` para comentarios; máx. 64 patrones y 64 KiB. Sin archivo devuelve los
// predeterminados. Los patrones se aplican en la raíz y en src/main/resources/
// y no admiten `**`.
func LoadInclude(root string) []string {
	def := append([]string(nil), defaultInclude...)
	r, err := os.OpenRoot(root)
	if err != nil {
		return def
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(includeFile))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return def
		}
		return nil
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, maxIncludeSz))
	out := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(out) == maxPatterns {
			break
		}
		out = append(out, line)
	}
	return out
}

// Create crea el worktree de un agente a partir del HEAD de root y copia los
// archivos de include. Si algo falla, lo deshace.
func Create(ctx context.Context, root, name string, include []string) (Info, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Info{}, err
	}
	base, err := revParse(ctx, root, "HEAD")
	if err != nil {
		return Info{}, fmt.Errorf("worktree: leer HEAD: %w", err)
	}
	if err := ensureExcluded(ctx, root); err != nil {
		return Info{}, err
	}
	parent := filepath.Join(root, filepath.FromSlash(subdir))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Info{}, fmt.Errorf("worktree: crear %s: %w", parent, err)
	}

	clean := sanitizeName(name)
	info := Info{Root: root, Base: base}
	for n := 1; ; n++ {
		cand := clean
		if n > 1 {
			cand = clean + "-" + strconv.Itoa(n)
		}
		path := filepath.Join(parent, cand)
		exists, err := branchExists(ctx, root, "wari/"+cand)
		if err != nil {
			return Info{}, err
		}
		if exists {
			continue
		}
		// Mkdir es atómico: dos WARI sobre el mismo proyecto no eligen el
		// mismo nombre. `git worktree add` acepta un directorio vacío.
		if err := os.Mkdir(path, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return Info{}, err
		}
		info.Name, info.Path, info.Branch = cand, path, "wari/"+cand
		break
	}
	if err := info.valid(); err != nil {
		return Info{}, err
	}

	if _, _, err := git(ctx, root, maxGitOut, "worktree", "add", "-b", info.Branch, info.Path, "HEAD"); err != nil {
		info.rollbackAdd()
		return Info{}, fmt.Errorf("worktree: crear: %w", err)
	}
	if err := info.copyIncluded(ctx, include); err != nil {
		info.rollback()
		return Info{}, err
	}
	info.writeOwner(ctx) // mejor esfuerzo: sin dueño, Prune nunca lo toca
	return info, nil
}

// ownerFile es el archivo, en el gitdir privado del worktree, con el PID del
// WARI que lo creó.
const ownerFile = "wari-owner"

func (i Info) ownerPath(ctx context.Context) (string, error) {
	out, _, err := git(ctx, i.Path, 1<<16, "rev-parse", "--git-path", ownerFile)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(i.Path, p)
	}
	return p, nil
}

func (i Info) writeOwner(ctx context.Context) {
	if p, err := i.ownerPath(ctx); err == nil {
		_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
}

// Prune borra los worktrees que un WARI anterior dejó sin cerrar (el terminal
// se cerró de golpe): los que tienen dueño, el dueño ya no existe y no hay nada
// que perder (árbol limpio y rama sin commits propios). Devuelve cuántos borró.
func Prune(ctx context.Context, root string) int {
	root, err := filepath.Abs(root)
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(subdir)))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		i := Info{Name: e.Name(), Root: root, Path: filepath.Join(root, filepath.FromSlash(subdir), e.Name()), Branch: "wari/" + e.Name()}
		if i.valid() != nil {
			continue
		}
		p, err := i.ownerPath(ctx)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue // sin dueño conocido: no es nuestro
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 || alive(pid) {
			continue
		}
		if st, _, err := git(ctx, i.Path, 1<<16, "status", "--porcelain"); err != nil || strings.TrimSpace(string(st)) != "" {
			continue
		}
		// Una rama desacoplada (git switch --detach) puede tener commits que
		// ninguna rama referencia: no es seguro borrarla.
		if ref, _, err := git(ctx, i.Path, 1<<16, "symbolic-ref", "-q", "HEAD"); err != nil || strings.TrimSpace(string(ref)) != "refs/heads/"+i.Branch {
			continue
		}
		if ahead, _, err := git(ctx, root, 1<<16, "rev-list", "--count", "HEAD.."+i.Branch); err != nil || strings.TrimSpace(string(ahead)) != "0" {
			continue
		}
		if i.Remove(ctx) == nil {
			n++
		}
	}
	return n
}

// sanitizeName deja solo [a-z0-9-], sin guiones repetidos ni en los bordes.
func sanitizeName(name string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxNameLen {
		s = strings.Trim(s[:maxNameLen], "-")
	}
	if s == "" {
		return "agente"
	}
	return s
}

// ensureExcluded añade .wari/worktrees/ a <gitdir>/info/exclude, sin tocar el
// .gitignore del autor.
func ensureExcluded(ctx context.Context, root string) error {
	out, _, err := git(ctx, root, 1<<16, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("worktree: localizar el gitdir: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	file := filepath.Join(dir, "info", "exclude")
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("worktree: leer %s: %w", file, err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == subdir+"/" {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("worktree: escribir %s: %w", file, err)
	}
	line := subdir + "/\n"
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		line = "\n" + line
	}
	_, werr := f.WriteString(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// copyIncluded copia al worktree los archivos locales (no versionados) que
// piden los patrones y anota cuáles son para que nunca entren en un commit.
func (i Info) copyIncluded(ctx context.Context, include []string) error {
	if len(include) == 0 {
		return nil
	}
	out, _, err := git(ctx, i.Root, maxGitOut, "ls-files", "-z")
	if err != nil {
		return fmt.Errorf("worktree: listar archivos versionados: %w", err)
	}
	tracked := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		tracked[p] = true
	}
	src, err := os.OpenRoot(i.Root)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenRoot(i.Path)
	if err != nil {
		return err
	}
	defer dst.Close()

	seen := map[string]bool{}
	var copied []string
loop:
	for _, pat := range include {
		pat = filepath.ToSlash(strings.TrimSpace(pat))
		if pat == "" || !filepath.IsLocal(filepath.FromSlash(pat)) {
			continue
		}
		for _, prefix := range []string{"", "src/main/resources/"} {
			matches, err := fs.Glob(src.FS(), prefix+pat)
			if err != nil {
				continue
			}
			for _, m := range matches {
				if seen[m] || tracked[m] {
					continue
				}
				seen[m] = true
				if first, _, _ := strings.Cut(m, "/"); first == ".git" || first == ".wari" {
					continue
				}
				ok, err := copyFile(src, dst, m)
				if err != nil {
					return fmt.Errorf("worktree: copiar %s: %w", m, err)
				}
				if ok {
					copied = append(copied, m)
					if len(copied) >= maxCopyFiles {
						break loop
					}
				}
			}
		}
	}
	if len(copied) == 0 {
		return nil
	}
	file, err := i.copiedPath(ctx)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(strings.Join(copied, "\x00")), 0o644)
}

// copyFile copia rel entre dos raíces si es un archivo regular pequeño. No
// sigue enlaces simbólicos: un enlace no es regular para Lstat.
func copyFile(src, dst *os.Root, rel string) (bool, error) {
	native := filepath.FromSlash(rel)
	fi, err := src.Lstat(native)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCopyBytes {
		return false, nil
	}
	in, err := src.Open(native)
	if err != nil {
		return false, nil
	}
	defer in.Close()
	if dir := filepath.Dir(native); dir != "." {
		if err := dst.MkdirAll(dir, 0o755); err != nil {
			return false, err
		}
	}
	out, err := dst.OpenFile(native, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm()|0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	_, cerr := io.Copy(out, io.LimitReader(in, maxCopyBytes))
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	return cerr == nil, cerr
}

// copiedPath es el archivo, en el gitdir privado del worktree, con las rutas
// copiadas por include (separadas por NUL).
func (i Info) copiedPath(ctx context.Context) (string, error) {
	out, _, err := git(ctx, i.Path, 1<<16, "rev-parse", "--git-path", copiedFile)
	if err != nil {
		return "", fmt.Errorf("worktree: localizar el gitdir del worktree: %w", err)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(i.Path, p)
	}
	return p, nil
}

func (i Info) copied(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	file, err := i.copiedPath(ctx)
	if err != nil {
		return set
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return set
	}
	for _, p := range strings.Split(string(data), "\x00") {
		if p != "" {
			set[p] = true
		}
	}
	return set
}

// Changes lista los archivos cambiados respecto a Base, incluidos los no
// confirmados y los sin seguimiento (menos los copiados con include). No
// detecta renombrados: aparecen como D más A.
func (i Info) Changes(ctx context.Context) ([]Change, error) {
	if err := i.valid(); err != nil {
		return nil, err
	}
	return i.changes(ctx)
}

// ProjectChanges son los cambios sin confirmar del propio proyecto respecto a
// HEAD (lo de todos los agentes que editan en vivo, y lo tuyo).
func ProjectChanges(ctx context.Context, root string) ([]Change, error) {
	return Info{Root: root, Path: root, Base: "HEAD"}.changes(ctx)
}

// ProjectPatch es el diff unificado de ProjectChanges, con los archivos nuevos.
func ProjectPatch(ctx context.Context, root string) (string, error) {
	return Info{Root: root, Path: root, Base: "HEAD"}.patch(ctx)
}

func (i Info) changes(ctx context.Context) ([]Change, error) {
	out, _, err := git(ctx, i.Path, maxGitOut, "diff", "--no-color", "--no-ext-diff", "--no-renames", "--name-status", "-z", i.Base, "--")
	if err != nil {
		return nil, err
	}
	var res []Change
	tok := strings.Split(string(out), "\x00")
	for n := 0; n+1 < len(tok); n += 2 {
		if tok[n] == "" {
			break
		}
		res = append(res, Change{Status: tok[n][:1], Path: tok[n+1]})
	}
	untracked, err := i.untracked(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range untracked {
		res = append(res, Change{Status: "?", Path: p})
	}
	sort.SliceStable(res, func(a, b int) bool { return res[a].Path < res[b].Path })
	return res, nil
}

func (i Info) untracked(ctx context.Context) ([]string, error) {
	out, _, err := git(ctx, i.Path, maxGitOut, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	skip := i.copied(ctx)
	var res []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && !skip[p] {
			res = append(res, p)
		}
	}
	sort.Strings(res)
	return res, nil
}

// Diff devuelve la versión de path en Base y la actual en el worktree; nil si
// no existe en ese lado. path es relativa al worktree.
func (i Info) Diff(ctx context.Context, path string) (old, cur []byte, err error) {
	if err := i.valid(); err != nil {
		return nil, nil, err
	}
	native := filepath.FromSlash(path)
	if !filepath.IsLocal(native) {
		return nil, nil, fmt.Errorf("worktree: %q queda fuera del worktree", path)
	}
	rel := filepath.ToSlash(filepath.Clean(native))

	r, err := os.OpenRoot(i.Path)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	if f, err := r.Open(filepath.Clean(native)); err == nil {
		cur, err = io.ReadAll(io.LimitReader(f, maxFileBytes))
		f.Close()
		if err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}

	old, stderr, err := git(ctx, i.Path, maxFileBytes, "show", i.Base+":"+rel)
	if err != nil {
		if strings.Contains(stderr, "exists on disk, but not in") || strings.Contains(stderr, "does not exist in") {
			return nil, cur, nil
		}
		return nil, nil, err
	}
	return old, cur, nil
}

// Patch devuelve el diff unificado completo contra Base, con los archivos
// nuevos incluidos.
func (i Info) Patch(ctx context.Context) (string, error) {
	if err := i.valid(); err != nil {
		return "", err
	}
	return i.patch(ctx)
}

func (i Info) patch(ctx context.Context) (string, error) {
	out, _, err := git(ctx, i.Path, maxGitOut, "diff", "--no-color", "--no-ext-diff", "--no-renames", i.Base, "--")
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.Write(out)
	untracked, err := i.untracked(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range untracked {
		// --no-index sale con 1 cuando hay diferencias, que aquí es lo normal.
		blk, _, err := git(ctx, i.Path, maxFileBytes*2, "diff", "--no-color", "--no-ext-diff", "--no-index", "--", "/dev/null", filepath.FromSlash(p))
		if err != nil && exitCode(err) != 1 {
			return "", err
		}
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteByte('\n')
		}
		sb.Write(blk)
	}
	return sb.String(), nil
}

// Merge confirma en el worktree lo pendiente y fusiona la rama en Root con
// `git merge --no-ff`. Ante conflictos o un árbol principal con cambios que
// chocan, no deja nada a medias: Merged es false y Conflicts lista los
// archivos. Nunca fuerza ni descarta cambios del árbol principal.
func (i Info) Merge(ctx context.Context) (MergeResult, error) {
	if err := i.valid(); err != nil {
		return MergeResult{}, err
	}
	// Una fusión del autor sin concluir no es nuestra: ni tocarla ni abortarla.
	busy, err := mergeInProgress(ctx, i.Root)
	if err != nil {
		return MergeResult{}, err
	}
	if busy {
		return MergeResult{Message: "el árbol principal tiene una fusión sin concluir; termínala o abórtala primero"}, nil
	}

	// Lo que el autor tiene en el índice acabaría dentro del commit de la
	// fusión o se mezclaría al abortarla: se rechaza antes de tocar nada.
	if _, _, err := git(ctx, i.Root, 1<<16, "diff", "--cached", "--quiet"); err != nil {
		if exitCode(err) == 1 {
			return MergeResult{Message: "tu proyecto tiene cambios en el índice (staged): confírmalos o sácalos con git restore --staged antes de fusionar"}, nil
		}
		return MergeResult{}, err
	}
	if err := i.commitPending(ctx); err != nil {
		return MergeResult{}, err
	}
	out, _, err := git(ctx, i.Root, 1<<16, "rev-list", "--count", "HEAD.."+i.Branch)
	if err != nil {
		return MergeResult{}, err
	}
	if strings.TrimSpace(string(out)) == "0" {
		return MergeResult{Merged: true, Message: "sin cambios"}, nil
	}

	args := append(identityArgs(ctx, i.Root), "merge", "--no-ff", "--no-edit", i.Branch)
	mout, stderr, merr := git(ctx, i.Root, maxGitOut, args...)
	msg := strings.TrimSpace(string(mout) + "\n" + stderr)
	if merr == nil {
		return MergeResult{Merged: true, Message: msg}, nil
	}
	if exitCode(merr) < 0 {
		return MergeResult{}, merr
	}

	res := MergeResult{Message: msg}
	started, err := mergeInProgress(ctx, i.Root)
	if err != nil {
		return MergeResult{}, err
	}
	if started {
		cout, _, err := git(ctx, i.Root, maxGitOut, "diff", "--name-only", "--diff-filter=U", "-z")
		if err == nil {
			for _, p := range strings.Split(string(cout), "\x00") {
				if p != "" {
					res.Conflicts = append(res.Conflicts, p)
				}
			}
		}
		if _, _, err := git(ctx, i.Root, 1<<16, "merge", "--abort"); err != nil {
			return res, fmt.Errorf("worktree: abortar la fusión: %w", err)
		}
		return res, nil
	}
	// Git la rechazó sin empezar (cambios locales que chocan): lista los
	// archivos que nombra, con sangría de tabulador.
	for _, l := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(l, "\t") {
			res.Conflicts = append(res.Conflicts, strings.TrimSpace(l))
		}
	}
	return res, nil
}

// commitPending confirma en la rama todo lo pendiente del worktree, salvo los
// archivos copiados con include.
func (i Info) commitPending(ctx context.Context) error {
	add := []string{"add", "-A"}
	if skip := i.copied(ctx); len(skip) > 0 {
		add = append(add, "--", ".")
		names := make([]string, 0, len(skip))
		for p := range skip {
			names = append(names, p)
		}
		sort.Strings(names)
		for _, p := range names {
			add = append(add, ":(exclude,literal)"+p)
		}
	}
	if _, _, err := git(ctx, i.Path, maxGitOut, add...); err != nil {
		return err
	}
	out, _, err := git(ctx, i.Path, maxGitOut, "diff", "--cached", "--name-only")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	args := append(identityArgs(ctx, i.Path), "-c", "commit.gpgsign=false",
		"commit", "--no-verify", "-m", "WARI: cambios de "+i.Name)
	_, _, err = git(ctx, i.Path, maxGitOut, args...)
	return err
}

// identityArgs devuelve los `-c` con una identidad de respaldo solo para lo
// que git no tenga configurado.
func identityArgs(ctx context.Context, dir string) []string {
	var args []string
	if _, _, err := git(ctx, dir, 1<<12, "config", "user.name"); err != nil {
		args = append(args, "-c", "user.name=WARI")
	}
	if _, _, err := git(ctx, dir, 1<<12, "config", "user.email"); err != nil {
		args = append(args, "-c", "user.email=wari@localhost")
	}
	return args
}

// Remove quita el worktree y borra su rama aunque tenga cambios (el llamante
// ya confirmó). Es idempotente.
func (i Info) Remove(ctx context.Context) error {
	if err := i.valid(); err != nil {
		return err
	}
	if _, err := os.Lstat(i.Path); err == nil {
		if _, _, err := git(ctx, i.Root, maxGitOut, "worktree", "remove", "--force", i.Path); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, _, err := git(ctx, i.Root, maxGitOut, "worktree", "prune"); err != nil {
		return err
	}
	exists, err := branchExists(ctx, i.Root, i.Branch)
	if err != nil {
		return err
	}
	if exists {
		if _, _, err := git(ctx, i.Root, maxGitOut, "branch", "-D", i.Branch); err != nil {
			return err
		}
	}
	return nil
}

// rollback deshace una creación a medias con un contexto propio, porque el del
// llamante puede haberse cancelado.
func (i Info) rollback() {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if i.Remove(ctx) != nil {
		i.rollbackAdd()
	}
}

// rollbackAdd limpia lo que dejó un `worktree add` fallido: la carpeta (que no
// existía antes de crearla), el registro y la rama.
func (i Info) rollbackAdd() {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if i.valid() != nil {
		return
	}
	os.RemoveAll(i.Path)
	git(ctx, i.Root, maxGitOut, "worktree", "prune")
	// Si `worktree add` creó la rama y falló después, no debe quedar huérfana.
	// Si la creó otra instancia de WARI y su worktree está en uso, git se
	// niega a borrarla ("checked out"), así que esto no le quita la suya.
	if ok, err := branchExists(ctx, i.Root, i.Branch); err == nil && ok {
		git(ctx, i.Root, maxGitOut, "branch", "-D", i.Branch)
	}
}

// valid comprueba que Path esté dentro de <Root>/.wari/worktrees/ y que la
// rama sea de WARI, para que una Info forjada no apunte a otra parte.
func (i Info) valid() error {
	if i.Root == "" || i.Path == "" || !strings.HasPrefix(i.Branch, "wari/") {
		return errors.New("worktree: información incompleta")
	}
	base := filepath.Join(i.Root, filepath.FromSlash(subdir))
	rel, err := filepath.Rel(base, i.Path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return fmt.Errorf("worktree: %s no está bajo %s", i.Path, base)
	}
	return nil
}

func revParse(ctx context.Context, dir, rev string) (string, error) {
	out, _, err := git(ctx, dir, 1<<12, "rev-parse", "--verify", rev)
	return strings.TrimSpace(string(out)), err
}

func branchExists(ctx context.Context, dir, branch string) (bool, error) {
	_, _, err := git(ctx, dir, 1<<12, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

func mergeInProgress(ctx context.Context, dir string) (bool, error) {
	_, _, err := git(ctx, dir, 1<<12, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// exitCode devuelve el código de salida de git, o -1 si no es un error de
// salida (no se pudo lanzar, plazo vencido...).
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// git ejecuta git en dir como proceso de su propio grupo. Devuelve stdout
// (acotado a max; si lo supera es un error) y el final de stderr.
func git(ctx context.Context, dir string, max int64, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	p, err := proc.Start(ctx, proc.Spec{
		Path: "git",
		Args: args,
		Dir:  dir,
		// Los mensajes de git salen traducidos; se comparan en inglés.
		Env: append(os.Environ(), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0"),
	})
	if err != nil {
		return nil, "", fmt.Errorf("git: %w", err)
	}
	defer p.Close()
	data, rerr := io.ReadAll(io.LimitReader(p.Stdout(), max+1))
	if rerr != nil || int64(len(data)) > max {
		p.Kill()
		p.Wait()
		if rerr == nil {
			rerr = fmt.Errorf("git %s: salida mayor de %d bytes", args[0], max)
		}
		return nil, "", rerr
	}
	werr := p.Wait()
	stderr := p.Stderr()
	if werr != nil {
		return data, stderr, fmt.Errorf("git %s: %w: %s", args[0], werr, strings.TrimSpace(stderr))
	}
	return data, stderr, nil
}

// Advance mueve Base a la punta de la rama: tras fusionar, los diffs y los
// cambios solo muestran lo que el agente haga desde entonces.
func (i Info) Advance(ctx context.Context) (Info, error) {
	if err := i.valid(); err != nil {
		return i, err
	}
	sha, err := revParse(ctx, i.Path, "HEAD")
	if err != nil {
		return i, err
	}
	i.Base = sha
	return i, nil
}

// samePath compara dos rutas; en Windows y macOS los sistemas de archivos no
// distinguen mayúsculas (git en Windows devuelve la letra de unidad en
// minúscula).
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
