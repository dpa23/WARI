package lsp

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrServerNotFound indica que el servidor no está en el PATH.
var ErrServerNotFound = errors.New("lsp: servidor no encontrado en el PATH")

// ServerDef describe un servidor de lenguaje conocido.
type ServerDef struct {
	// Name es el nombre visible.
	Name string
	// Command es el ejecutable que se busca en el PATH.
	Command string
	// LanguageID es el languageId de LSP.
	LanguageID string
	// Exts son las extensiones (con punto) que atiende.
	Exts []string
	// Args devuelve los argumentos para un proyecto en root; nil si no hay.
	Args func(root string) []string
}

// Known son los servidores soportados.
var Known = []ServerDef{
	{
		Name: "jdtls", Command: "jdtls", LanguageID: "java", Exts: []string{".java"},
		// jdtls necesita un directorio de datos propio por proyecto.
		Args: func(root string) []string { return []string{"-data", jdtlsData(root)} },
	},
	{
		Name: "kotlin-language-server", Command: "kotlin-language-server",
		LanguageID: "kotlin", Exts: []string{".kt", ".kts"},
	},
	{
		Name: "csharp-ls", Command: "csharp-ls", LanguageID: "csharp", Exts: []string{".cs"},
	},
}

// jdtlsData es el directorio de datos de jdtls para root, dentro de la caché
// del usuario (nunca dentro del proyecto).
func jdtlsData(root string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	sum := sha1.Sum([]byte(root))
	return filepath.Join(base, "wari", "jdtls", hex.EncodeToString(sum[:8]))
}

// Detect busca el ejecutable en el PATH. No lanza nada.
func (d ServerDef) Detect() (string, error) {
	p, err := exec.LookPath(d.Command)
	if err != nil {
		return "", errors.Join(ErrServerNotFound, err)
	}
	return p, nil
}

// CommandArgs devuelve los argumentos de arranque para el proyecto root.
func (d ServerDef) CommandArgs(root string) []string {
	if d.Args == nil {
		return nil
	}
	return d.Args(root)
}

// ServerForPath devuelve el servidor conocido que atiende la extensión de
// path, esté instalado o no.
func ServerForPath(path string) (ServerDef, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	for _, d := range Known {
		for _, e := range d.Exts {
			if e == ext {
				return d, true
			}
		}
	}
	return ServerDef{}, false
}

// Available devuelve los servidores conocidos presentes en el PATH.
func Available() []ServerDef {
	var out []ServerDef
	for _, d := range Known {
		if _, err := d.Detect(); err == nil {
			out = append(out, d)
		}
	}
	return out
}
