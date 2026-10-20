package orch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dpa23/wari/internal/proc"
	"github.com/dpa23/wari/internal/workspace/owner"
)

// maxGitDiffBytes acota lo que se lee de git y del archivo para el diff.
const maxGitDiffBytes = 4 << 20

// ErrNoGit indica que no se pudo obtener la versión del archivo en git.
var ErrNoGit = errors.New("orch: sin versión en git")

// GitDiff devuelve el archivo en HEAD de git (nil si es nuevo) y el actual
// (nil si ya no existe), para las ediciones sin checkpoint (codex, opencode o
// un comando de shell): no se pueden revertir, pero sí verse. path puede ser
// relativa o absoluta; debe quedar dentro de la raíz del espacio de trabajo.
func (s *Supervisor) GitDiff(ctx context.Context, id ID, path string) (old, current []byte, err error) {
	s.mu.Lock()
	_, ferr := s.findLocked(id)
	root := s.ws.Root
	s.mu.Unlock()
	if ferr != nil {
		return nil, nil, ferr
	}
	if root == "" {
		return nil, nil, ErrNoGit
	}
	rel, inside := owner.Rel(root, path)
	if !inside {
		return nil, nil, fmt.Errorf("%w: %s está fuera del proyecto", ErrNoGit, path)
	}
	cur, err := readInside(root, rel)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	old, err = gitShowHead(ctx, root, rel)
	if err != nil {
		return nil, nil, err
	}
	return old, cur, nil
}

// readInside lee rel dentro de root sin seguir enlaces fuera de ella.
func readInside(root, rel string) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxGitDiffBytes))
}

// gitShowHead ejecuta `git show HEAD:<rel>` como proceso de su propio grupo.
// Un archivo que no está en HEAD (nuevo) da nil sin error.
func gitShowHead(ctx context.Context, root, rel string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := proc.Start(ctx, proc.Spec{
		Path: "git",
		Args: []string{"-C", root, "show", "HEAD:" + rel},
		// Los mensajes de git salen traducidos; se comparan en inglés.
		Env: append(os.Environ(), "LC_ALL=C"),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoGit, err)
	}
	defer p.Close()
	data, rerr := io.ReadAll(io.LimitReader(p.Stdout(), maxGitDiffBytes))
	werr := p.Wait()
	if rerr != nil {
		return nil, rerr
	}
	if werr != nil {
		// git sale con 128 si la ruta no existe en HEAD, el repo no tiene
		// commits o no hay repo; solo el primer caso es "archivo nuevo".
		if msg := p.Stderr(); strings.Contains(msg, "exists on disk, but not in") || strings.Contains(msg, "does not exist in") {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrNoGit, werr)
	}
	return data, nil
}
