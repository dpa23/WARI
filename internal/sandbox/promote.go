package sandbox

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrPromote indica que un artefacto no se puede promover.
var ErrPromote = errors.New("sandbox: promoción rechazada")

// Promote copia un artefacto de las salidas de la ejecución (run.OutDir/rel)
// al proyecto real (run.Root/rel). Es la única operación que escribe en el
// proyecto y solo ocurre a petición explícita.
//
// rel debe ser relativa y quedarse dentro de las raíces (sin "..", sin ruta
// absoluta). Las dos raíces se abren con os.Root, que impide además escapar
// por enlaces simbólicos. Aparte de eso se rechaza cualquier enlace simbólico:
// el artefacto (y todo lo que contenga), los directorios intermedios de
// origen y destino y el destino existente. Solo se copian archivos regulares
// y directorios. Todo se valida antes de escribir, y cada archivo se escribe
// en un temporal que se renombra.
func Promote(run *Run, rel string) error {
	if run == nil {
		return fmt.Errorf("%w: ejecución nula", ErrPromote)
	}
	if rel == "" || !filepath.IsLocal(rel) || strings.ContainsRune(rel, 0) {
		return fmt.Errorf("%w: ruta fuera de la raíz: %q", ErrPromote, rel)
	}
	rel = filepath.ToSlash(filepath.Clean(rel))

	src, err := os.OpenRoot(run.OutDir)
	if err != nil {
		return fmt.Errorf("sandbox: abrir salidas: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenRoot(run.Root)
	if err != nil {
		return fmt.Errorf("sandbox: abrir proyecto: %w", err)
	}
	defer dst.Close()

	if err := noSymlinkInPath(src, rel, true); err != nil {
		return err
	}
	if err := noSymlinkInPath(dst, rel, false); err != nil {
		return err
	}

	fi, err := src.Lstat(rel)
	if err != nil {
		return fmt.Errorf("sandbox: artefacto %q: %w", rel, err)
	}

	// Primera pasada: nada de enlaces ni archivos especiales.
	type item struct {
		rel string
		dir bool
	}
	var items []item
	switch {
	case fi.Mode().IsRegular():
		items = append(items, item{rel, false})
	case fi.IsDir():
		sub, err := fs.Sub(src.FS(), rel)
		if err != nil {
			return err
		}
		err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			t := d.Type()
			switch {
			case t.IsDir():
				items = append(items, item{path.Join(rel, p), true})
			case t.IsRegular():
				items = append(items, item{path.Join(rel, p), false})
			default:
				return fmt.Errorf("%w: %q no es un archivo regular ni un directorio", ErrPromote, path.Join(rel, p))
			}
			return nil
		})
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: %q no es un archivo regular ni un directorio", ErrPromote, rel)
	}
	for _, it := range items {
		if err := noSymlinkInPath(dst, it.rel, false); err != nil {
			return err
		}
	}

	// Segunda pasada: copiar.
	if parent := path.Dir(rel); parent != "." {
		if err := dst.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("sandbox: crear %q: %w", parent, err)
		}
	}
	for _, it := range items {
		if it.dir {
			if err := dst.MkdirAll(it.rel, 0o755); err != nil {
				return fmt.Errorf("sandbox: crear %q: %w", it.rel, err)
			}
			continue
		}
		if err := copyFile(src, dst, it.rel); err != nil {
			return err
		}
	}
	return nil
}

// noSymlinkInPath comprueba que ningún componente existente de rel dentro de
// root es un enlace simbólico. Si mustExist es false, el primer componente
// ausente termina la comprobación sin error.
func noSymlinkInPath(root *os.Root, rel string, mustExist bool) error {
	parts := strings.Split(rel, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		fi, err := root.Lstat(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && !mustExist {
				return nil
			}
			return fmt.Errorf("sandbox: %q: %w", p, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %q es un enlace simbólico", ErrPromote, p)
		}
	}
	return nil
}

func copyFile(src, dst *os.Root, rel string) (err error) {
	in, err := src.Open(rel)
	if err != nil {
		return fmt.Errorf("sandbox: abrir %q: %w", rel, err)
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %q cambió durante la copia", ErrPromote, rel)
	}
	if di, err := dst.Lstat(rel); err == nil && !di.Mode().IsRegular() {
		return fmt.Errorf("%w: el destino %q existe y no es un archivo regular", ErrPromote, rel)
	}

	tmp := rel + ".wari-promote"
	out, err := dst.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm()&0o777)
	if err != nil {
		return fmt.Errorf("sandbox: crear %q: %w", tmp, err)
	}
	defer func() {
		if err != nil {
			out.Close()
			dst.Remove(tmp)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("sandbox: copiar %q: %w", rel, err)
	}
	if err = out.Close(); err != nil {
		return fmt.Errorf("sandbox: cerrar %q: %w", tmp, err)
	}
	if err = dst.Rename(tmp, rel); err != nil {
		return fmt.Errorf("sandbox: renombrar %q: %w", rel, err)
	}
	return nil
}
