// Package templates carga plantillas de archivo (texto con cabecera YAML
// simple) y las renderiza derivando package, nombre de clase y namespace de
// la ruta del archivo nuevo. No depende del editor ni de los agentes.
//
// Formato de un archivo *.tmpl:
//
//	---
//	name: Controller
//	lang: java
//	ext: java
//	vars: [basePath]        # o una lista "- basePath" en líneas siguientes
//	---
//	package {{package}};
//	...
package templates

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Extension es la extensión de los archivos de plantilla que lee Load.
const Extension = ".tmpl"

// Template es una plantilla ya parseada.
type Template struct {
	Name   string
	Lang   string
	Ext    string   // extensión del archivo generado, sin punto
	Vars   []string // variables que el usuario debe aportar
	Body   string
	Source string // ruta dentro del fs.FS (vacía si viene de Parse)
}

// Context son los datos para renderizar.
type Context struct {
	// Root es la raíz del proyecto (ruta del sistema de archivos).
	Root string
	// TargetPath es el archivo que se va a crear, absoluto o relativo a Root.
	TargetPath string
	// Vars son los valores de las variables declaradas en la plantilla. Una
	// clave "package", "ClassName" o "namespace" sustituye al valor derivado.
	Vars map[string]string
}

// Load lee recursivamente los *.tmpl de dir y los devuelve ordenados por
// ruta. Una plantilla inválida devuelve un error que nombra su archivo.
func Load(fsys fs.FS, dir string) ([]Template, error) {
	var out []Template
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != Extension {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		t, err := Parse(data)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		t.Source = p
		out = append(out, t)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("templates: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// Parse interpreta el contenido de un archivo de plantilla.
func Parse(data []byte) (Template, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r\n") != "---" {
		return Template{}, errors.New("falta la cabecera '---'")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\n") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Template{}, errors.New("la cabecera no se cierra con '---'")
	}
	t := Template{Body: strings.Join(lines[end+1:], "")}
	if err := parseHeader(&t, lines[1:end]); err != nil {
		return Template{}, err
	}
	if t.Name == "" {
		return Template{}, errors.New("la cabecera no define name")
	}
	return t, nil
}

func parseHeader(t *Template, lines []string) error {
	inVars := false
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if inVars && strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			if !inVars {
				return fmt.Errorf("elemento de lista fuera de vars: %q", trimmed)
			}
			t.Vars = append(t.Vars, unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))))
			continue
		}
		inVars = false
		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			return fmt.Errorf("línea de cabecera inválida: %q", trimmed)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "name":
			t.Name = unquote(val)
		case "lang":
			t.Lang = unquote(val)
		case "ext":
			t.Ext = strings.TrimPrefix(unquote(val), ".")
		case "vars":
			switch {
			case val == "":
				inVars = true
			case strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]"):
				for _, item := range strings.Split(val[1:len(val)-1], ",") {
					if item = unquote(strings.TrimSpace(item)); item != "" {
						t.Vars = append(t.Vars, item)
					}
				}
			default:
				return fmt.Errorf("vars debe ser una lista: %q", val)
			}
		default:
			return fmt.Errorf("clave de cabecera desconocida: %q", key)
		}
	}
	for _, v := range t.Vars {
		if !varName.MatchString(v) {
			return fmt.Errorf("nombre de variable inválido: %q", v)
		}
	}
	return nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

var (
	varName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
)

// Render sustituye las variables {{x}} de la plantilla. Disponibles:
// {{package}} (DerivePackage), {{ClassName}} (nombre del archivo sin
// extensión), {{namespace}} (DeriveNamespace) y las declaradas en vars. Falla
// si falta el valor de una variable declarada o si el cuerpo usa una variable
// desconocida. Los valores derivados solo se calculan si el cuerpo los usa.
func Render(t Template, ctx Context) (string, error) {
	var missing []string
	for _, v := range t.Vars {
		if _, ok := ctx.Vars[v]; !ok {
			missing = append(missing, v)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("templates: faltan variables: %s", strings.Join(missing, ", "))
	}

	var rerr error
	out := placeholder.ReplaceAllStringFunc(t.Body, func(m string) string {
		if rerr != nil {
			return m
		}
		name := placeholder.FindStringSubmatch(m)[1]
		if v, ok := ctx.Vars[name]; ok {
			return v
		}
		var v string
		var err error
		switch name {
		case "package":
			v, err = DerivePackage(ctx.Root, ctx.TargetPath)
		case "ClassName":
			v, err = ClassName(ctx.TargetPath)
		case "namespace":
			v, err = DeriveNamespace(ctx.Root, ctx.TargetPath)
		default:
			err = errors.New("variable desconocida")
		}
		if err != nil {
			rerr = fmt.Errorf("templates: {{%s}}: %w", name, err)
			return m
		}
		return v
	})
	if rerr != nil {
		return "", rerr
	}
	return out, nil
}

// ClassName devuelve el nombre del archivo sin su última extensión.
func ClassName(targetPath string) (string, error) {
	if targetPath == "" {
		return "", errors.New("falta TargetPath")
	}
	base := path.Base(strings.ReplaceAll(targetPath, `\`, "/"))
	name := strings.TrimSuffix(base, path.Ext(base))
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("nombre de archivo inválido: %q", targetPath)
	}
	return name, nil
}
