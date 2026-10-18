package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dpa23/wari/assets"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/imports"
	"github.com/dpa23/wari/internal/snippets"
	"github.com/dpa23/wari/internal/templates"
)

// loadSnippets devuelve los snippets por lenguaje: los embebidos más los de
// <userDir>/snippets/<lang>.json y <projectDir>/snippets/<lang>.json. Si dos
// definen el mismo nombre, gana el del proyecto y luego el del usuario. Los
// archivos ilegibles o inválidos se saltan y se describen en warns.
func loadSnippets(userDir, projectDir string) (sets map[string]snippets.Set, warns []string) {
	sets, err := snippets.LoadDir(assets.FS, "snippets")
	if err != nil {
		return map[string]snippets.Set{}, []string{"snippets embebidos: " + err.Error()}
	}
	for _, dir := range []string{userDir, projectDir} {
		if dir == "" {
			continue
		}
		dir = filepath.Join(dir, "snippets")
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				warns = append(warns, err.Error())
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			over, err := readSnippets(path)
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			lang := strings.TrimSuffix(e.Name(), ".json")
			sets[lang] = mergeSnippets(sets[lang], over)
		}
	}
	return sets, warns
}

func readSnippets(path string) (snippets.Set, error) {
	f, err := os.Open(path)
	if err != nil {
		return snippets.Set{}, err
	}
	defer f.Close()
	return snippets.Load(f)
}

// mergeSnippets suma over a base; ante el mismo nombre gana over. El
// resultado queda ordenado por nombre.
func mergeSnippets(base, over snippets.Set) snippets.Set {
	byName := make(map[string]snippets.Snippet, len(base.Snippets)+len(over.Snippets))
	for _, sn := range base.Snippets {
		byName[sn.Name] = sn
	}
	for _, sn := range over.Snippets {
		byName[sn.Name] = sn
	}
	out := snippets.Set{Snippets: make([]snippets.Snippet, 0, len(byName))}
	for _, sn := range byName {
		out.Snippets = append(out.Snippets, sn)
	}
	slices.SortFunc(out.Snippets, func(a, b snippets.Snippet) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// loadImports devuelve los mapas tipo→import por lenguaje: los embebidos más
// los de <userDir>/imports/<lang>.json y <projectDir>/imports/<lang>.json. Si
// dos definen el mismo tipo, gana el del proyecto y luego el del usuario (sus
// candidatos sustituyen a los anteriores). Los archivos inválidos se saltan y
// se describen en warns.
func loadImports(userDir, projectDir string) (maps map[string]imports.Map, warns []string) {
	maps, err := imports.LoadDir(assets.FS, "imports")
	if err != nil {
		return map[string]imports.Map{}, []string{"imports embebidos: " + err.Error()}
	}
	for _, dir := range []string{userDir, projectDir} {
		if dir == "" {
			continue
		}
		dir = filepath.Join(dir, "imports")
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				warns = append(warns, err.Error())
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			path := filepath.Join(dir, e.Name())
			over, err := readImports(path)
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			lang := strings.TrimSuffix(e.Name(), ".json")
			maps[lang] = maps[lang].Merge(over)
		}
	}
	return maps, warns
}

func readImports(path string) (imports.Map, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return imports.Load(f)
}

// loadTemplates devuelve las plantillas embebidas de nuevo archivo.
func loadTemplates() ([]templates.Template, error) {
	return templates.Load(assets.FS, "templates")
}

// loadAssets reúne snippets, imports y plantillas para el layout; los
// problemas se informan por stderr sin abortar.
func loadAssets(root string) (map[string]snippets.Set, map[string]imports.Map, []templates.Template) {
	userDir, err := config.UserDir()
	if err != nil {
		userDir = ""
	}
	projectDir := filepath.Join(root, ".wari")
	sets, warns := loadSnippets(userDir, projectDir)
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "wari: aviso: snippets:", w)
	}
	maps, warns := loadImports(userDir, projectDir)
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "wari: aviso: imports:", w)
	}
	tpls, err := loadTemplates()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wari: aviso: plantillas:", err)
	}
	return sets, maps, tpls
}
