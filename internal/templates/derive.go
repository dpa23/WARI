package templates

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// ErrNoSourceRoot indica que la ruta no está bajo src/{main,test}/{java,kotlin}.
var ErrNoSourceRoot = errors.New("la ruta no está bajo src/main/java ni src/main/kotlin")

// ErrNoProject indica que no hay ningún .csproj entre el archivo y la raíz.
var ErrNoProject = errors.New("no se encontró ningún .csproj")

// relDir devuelve los segmentos de la carpeta de target relativa a root.
func relDir(root, target string) ([]string, error) {
	if target == "" {
		return nil, errors.New("falta TargetPath")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	rel, err := filepath.Rel(root, filepath.Dir(target))
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, fmt.Errorf("%q queda fuera de la raíz %q", target, root)
	}
	if rel == "." {
		return nil, nil
	}
	return strings.Split(rel, "/"), nil
}

// DerivePackage deduce el package de Java/Kotlin del archivo path (absoluto o
// relativo a root) a partir de su carpeta bajo src/main/java, src/main/kotlin
// o sus equivalentes de test: com/empresa/api -> "com.empresa.api". El paquete
// por defecto (archivo directamente en la carpeta de fuentes) da "". Falla
// con ErrNoSourceRoot si no hay raíz de fuentes y si un segmento no es un
// identificador válido.
func DerivePackage(root, path string) (string, error) {
	segs, err := relDir(root, path)
	if err != nil {
		return "", err
	}
	for i := 0; i+2 < len(segs); i++ {
		if segs[i] != "src" || (segs[i+1] != "main" && segs[i+1] != "test") ||
			(segs[i+2] != "java" && segs[i+2] != "kotlin") {
			continue
		}
		pkg := segs[i+3:]
		for _, s := range pkg {
			if !isJavaIdent(s) {
				return "", fmt.Errorf("%q no es un identificador válido de package", s)
			}
		}
		return strings.Join(pkg, "."), nil
	}
	return "", ErrNoSourceRoot
}

func isJavaIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r == '$' || unicode.IsLetter(r) || (i > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}

type csproj struct {
	Groups []struct {
		RootNamespace string `xml:"RootNamespace"`
	} `xml:"PropertyGroup"`
}

// DeriveNamespace deduce el namespace de C# del archivo path (absoluto o
// relativo a root): sube desde su carpeta hasta el .csproj más cercano sin
// salir de root y une con puntos el RootNamespace del proyecto (o su nombre
// si no lo define) con las carpetas intermedias. Los caracteres no válidos
// se sustituyen por "_", como hace dotnet. Sin proyecto, falla con
// ErrNoProject.
func DeriveNamespace(root, path string) (string, error) {
	if _, err := relDir(root, path); err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	root = filepath.Clean(root)
	dir := filepath.Dir(filepath.Clean(path))
	for {
		file, err := findCsproj(dir)
		if err != nil {
			return "", err
		}
		if file != "" {
			ns, err := projectNamespace(file)
			if err != nil {
				return "", err
			}
			rel, err := filepath.Rel(dir, filepath.Dir(filepath.Clean(path)))
			if err != nil {
				return "", err
			}
			parts := []string{ns}
			if rel != "." {
				for _, s := range strings.Split(filepath.ToSlash(rel), "/") {
					parts = append(parts, nsSegment(s))
				}
			}
			return strings.Join(parts, "."), nil
		}
		if dir == root || filepath.Dir(dir) == dir {
			return "", ErrNoProject
		}
		dir = filepath.Dir(dir)
	}
}

func findCsproj(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil // la carpeta destino aún no existe
		}
		return "", err
	}
	var found []string
	for _, e := range entries {
		if !strings.EqualFold(filepath.Ext(e.Name()), ".csproj") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		found = append(found, e.Name())
	}
	if len(found) == 0 {
		return "", nil
	}
	sort.Strings(found)
	return filepath.Join(dir, found[0]), nil
}

const maxCsprojSize = 1 << 20 // 1 MiB

func projectNamespace(file string) (string, error) {
	fi, err := os.Lstat(file)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: no es un archivo regular", filepath.Base(file))
	}
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()

	lr := io.LimitReader(f, maxCsprojSize+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return "", err
	}
	if len(data) > maxCsprojSize {
		return "", fmt.Errorf("%s: archivo supera el límite de 1 MiB", filepath.Base(file))
	}

	var p csproj
	if err := xml.Unmarshal(data, &p); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	for _, g := range p.Groups {
		if ns := strings.TrimSpace(g.RootNamespace); ns != "" {
			return ns, nil
		}
	}
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	parts := strings.Split(name, ".")
	for i, s := range parts {
		parts[i] = nsSegment(s)
	}
	return strings.Join(parts, "."), nil
}

// nsSegment convierte un nombre de carpeta en un identificador de C#.
func nsSegment(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '_' || unicode.IsLetter(r):
			b.WriteRune(r)
		case unicode.IsDigit(r):
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}
