// Package snippets carga snippets en formato JSON estilo VS Code, los busca
// por prefijo y los expande a texto con tabstops. No depende del editor: el
// llamador inserta Expansion.Text y mueve el cursor con una Session.
package snippets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Snippet es una entrada del JSON de un lenguaje.
type Snippet struct {
	Name        string
	Prefixes    []string
	Body        []string // una cadena por línea, sin saltos de línea finales
	Description string
}

// Set agrupa los snippets de un lenguaje, ordenados por nombre.
type Set struct {
	Snippets []Snippet
}

type stringList []string

func (l *stringList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*l = stringList{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return errors.New("se esperaba un texto o una lista de textos")
	}
	*l = arr
	return nil
}

type rawSnippet struct {
	Prefix      stringList `json:"prefix"`
	Body        stringList `json:"body"`
	Description string     `json:"description"`
}

// Load lee un JSON {"nombre": {"prefix", "body", "description"}} y devuelve el
// Set ordenado por nombre. prefix y body admiten texto o lista de textos. Los
// campos desconocidos (por ejemplo "scope") se ignoran.
func Load(r io.Reader) (Set, error) {
	var raw map[string]rawSnippet
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Set{}, fmt.Errorf("snippets: JSON inválido: %w", err)
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	set := Set{Snippets: make([]Snippet, 0, len(names))}
	for _, name := range names {
		rs := raw[name]
		if len(rs.Prefix) == 0 {
			return Set{}, fmt.Errorf("snippets: %q no tiene prefix", name)
		}
		for _, p := range rs.Prefix {
			if p == "" {
				return Set{}, fmt.Errorf("snippets: %q tiene un prefix vacío", name)
			}
		}
		if len(rs.Body) == 0 {
			return Set{}, fmt.Errorf("snippets: %q no tiene body", name)
		}
		set.Snippets = append(set.Snippets, Snippet{
			Name:        name,
			Prefixes:    []string(rs.Prefix),
			Body:        []string(rs.Body),
			Description: rs.Description,
		})
	}
	return set, nil
}

// LoadDir carga cada archivo *.json de dir (sin recursión). La clave del mapa
// es el nombre del archivo sin extensión (el lenguaje: "java", "kotlin").
func LoadDir(fsys fs.FS, dir string) (map[string]Set, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("snippets: %w", err)
	}
	sets := make(map[string]Set)
	for _, e := range entries {
		if !e.Type().IsRegular() || path.Ext(e.Name()) != ".json" {
			continue
		}
		f, err := fsys.Open(path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("snippets: %w", err)
		}
		lr := io.LimitReader(f, 1<<20+1)
		data, err := io.ReadAll(lr)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if len(data) > 1<<20 {
			return nil, fmt.Errorf("%s: archivo supera el límite de 1 MiB", e.Name())
		}
		set, err := Load(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		sets[strings.TrimSuffix(e.Name(), ".json")] = set
	}
	return sets, nil
}

// Match devuelve los snippets con algún prefijo que empiece por prefix
// (sensible a mayúsculas). Primero los de coincidencia exacta y luego el
// resto, cada grupo ordenado por nombre. Un prefix vacío no coincide con nada.
func (s Set) Match(prefix string) []Snippet {
	if prefix == "" {
		return nil
	}
	var exact, partial []Snippet
	for _, sn := range s.Snippets {
		kind := 0
		for _, p := range sn.Prefixes {
			if p == prefix {
				kind = 2
				break
			}
			if strings.HasPrefix(p, prefix) {
				kind = 1
			}
		}
		switch kind {
		case 2:
			exact = append(exact, sn)
		case 1:
			partial = append(partial, sn)
		}
	}
	return append(exact, partial...)
}
