// Package sandbox ejecuta el proyecto dentro de un contenedor Podman o Docker
// sin que la ejecución pueda tocar el proyecto real (PLAN fase 13, tier 3).
//
// Lo que se aísla es la ejecución, no la edición: el código fuente es el real,
// pero el proceso corre en un contenedor cuyos cambios mueren con él.
//
// # Estrategia de escritura efímera
//
// El proyecto nunca se monta con permiso de escritura. Según el runtime:
//
//   - Podman (StrategyOverlay): `-v <proyecto>:/workspace:O`. Es un montaje
//     overlay: el proyecto es la capa inferior de solo lectura y todo lo que el
//     contenedor escribe va a una capa superior efímera que Podman borra con el
//     contenedor. Funciona sin root (probado con Podman 5.8 rootless) y no copia
//     nada. La opción O no se puede combinar con ro, pero la capa inferior no
//     se modifica nunca.
//   - Docker (StrategyCopy): Docker no tiene overlays de usuario. El proyecto
//     se monta en /src con ro, se declara un volumen anónimo en /workspace y al
//     arrancar `cp -a /src/. /workspace/` lo puebla. `--rm` borra el volumen.
//
// Con StrategyAuto (por defecto) se elige overlay con Podman y copia con Docker.
// Ambas estrategias exigen `sh` y `cp` dentro de la imagen.
//
// # Salidas
//
// Todo lo que se escribe en /workspace (target/, build/, bin/, obj/) vive en la
// capa efímera. Lo que deba sobrevivir se escribe en /out, que es un directorio
// del anfitrión fuera del proyecto (~/.cache/wari/runs/<id>/). Promote copia un
// artefacto de ahí al proyecto real, solo a petición explícita.
//
// # Seguridad
//
// Los argumentos se pasan al runtime como argv, nunca por una cadena de shell;
// el único shell es el de dentro del contenedor y ejecuta un guion fijo al que
// el comando del usuario llega como argumentos posicionales ("$@"). La red es
// "none" salvo opción explícita, y no se aceptan las redes host ni container:.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Valores por defecto.
const (
	// NetworkNone es la red por defecto: sin acceso a nada, producción incluida.
	NetworkNone = "none"
	// NetworkBridge es la red NAT habitual de los contenedores; hay que pedirla.
	NetworkBridge = "bridge"

	DefaultCPU    = 2.0
	DefaultMemory = "2g"
	pidsLimit     = 1024

	// WorkspaceDir es donde el proceso ve la copia escribible del proyecto.
	WorkspaceDir = "/workspace"
	// OutDirInContainer es donde el proceso deja artefactos que sobreviven al contenedor.
	OutDirInContainer = "/out"
	srcDir            = "/src"
	stderrFile        = ".stderr.log"
)

// Strategy es el mecanismo de capa de escritura efímera.
type Strategy string

const (
	StrategyAuto    Strategy = ""
	StrategyOverlay Strategy = "overlay"
	StrategyCopy    Strategy = "copy"
)

// Errores de validación.
var (
	ErrNoRuntime = errors.New("sandbox: no hay podman ni docker en el PATH")
	ErrNoImage   = errors.New("sandbox: falta la imagen y no hay perfil que la dé")
	ErrNoCommand = errors.New("sandbox: falta el comando y no hay perfil que lo dé")
)

// Sandbox lanza ejecuciones aisladas del proyecto.
type Sandbox interface {
	// Run lanza la ejecución y vuelve en cuanto el contenedor fue lanzado.
	// Cancelar ctx detiene la ejecución como Run.Stop.
	Run(ctx context.Context, spec Spec) (*Run, error)
}

// Mount es un montaje extra; siempre de solo lectura.
type Mount struct {
	Host      string // ruta absoluta en el anfitrión
	Container string // ruta absoluta dentro del contenedor
}

// Port publica un puerto del contenedor en 127.0.0.1 del anfitrión. Exige una
// red distinta de none.
type Port struct {
	Host      int    // 0 usa el mismo que Container
	Container int    //
	Protocol  string // "tcp" (por defecto) o "udp"
}

// Spec describe una ejecución.
type Spec struct {
	// Root es la raíz del proyecto real. Nunca se escribe.
	Root string
	// Profile fuerza un perfil por nombre ("maven", "gradle", "dotnet"). Vacío
	// lo detecta en Root cuando falta Image o Command.
	Profile string
	// Image y Command salen del perfil si se dejan vacíos.
	Image   string
	Command []string
	// WorkDir es un subdirectorio relativo del proyecto donde ejecutar.
	WorkDir string
	// Env son variables para el proceso; nunca se interpolan en un shell.
	Env map[string]string
	// Ports publicados en 127.0.0.1.
	Ports []Port
	// CPU es el límite de CPUs (0 usa DefaultCPU); Memory, p. ej. "1g" (vacío
	// usa DefaultMemory).
	CPU    float64
	Memory string
	// Network es "none" si está vacío. Cualquier otro valor es la opción
	// explícita de permitir red.
	Network string
	// ExtraMounts se montan siempre en solo lectura.
	ExtraMounts []Mount
	// Strategy elige la capa de escritura; vacío según el runtime.
	Strategy Strategy
}

var (
	imageRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	memoryRe  = regexp.MustCompile(`^[1-9][0-9]*[bkmgBKMG]?$`)
	networkRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// resolved es un Spec validado y completo.
type resolved struct {
	Spec
	strategy Strategy
	cpu      string
}

// resolve completa y valida el Spec. rt es "podman" o "docker".
func (s Spec) resolve(rt string) (resolved, error) {
	r := resolved{Spec: s}

	if s.Root == "" || !filepath.IsAbs(s.Root) {
		return r, fmt.Errorf("sandbox: Root debe ser una ruta absoluta, no %q", s.Root)
	}
	if err := safeMountPath(s.Root); err != nil {
		return r, fmt.Errorf("sandbox: Root: %w", err)
	}

	if s.Image == "" || len(s.Command) == 0 {
		var p Profile
		var ok bool
		if s.Profile != "" {
			p, ok = ProfileByName(s.Profile)
			if !ok {
				return r, fmt.Errorf("sandbox: perfil desconocido %q", s.Profile)
			}
		} else if p, ok = DetectProfile(s.Root); !ok {
			if s.Image == "" {
				return r, ErrNoImage
			}
			return r, ErrNoCommand
		}
		if r.Image == "" {
			r.Image = p.Image
		}
		if len(r.Command) == 0 {
			r.Command = append([]string(nil), p.Command...)
		}
	}
	if !imageRe.MatchString(r.Image) {
		return r, fmt.Errorf("sandbox: imagen no válida %q", r.Image)
	}
	for _, a := range r.Command {
		if strings.ContainsRune(a, 0) {
			return r, errors.New("sandbox: el comando contiene NUL")
		}
	}

	if r.WorkDir != "" {
		if !filepath.IsLocal(r.WorkDir) || strings.ContainsAny(r.WorkDir, "\x00\n") {
			return r, fmt.Errorf("sandbox: WorkDir debe ser relativo y dentro del proyecto: %q", r.WorkDir)
		}
		r.WorkDir = filepath.ToSlash(filepath.Clean(r.WorkDir))
	}

	for k, v := range r.Env {
		if !envKeyRe.MatchString(k) {
			return r, fmt.Errorf("sandbox: nombre de variable no válido %q", k)
		}
		if strings.ContainsRune(v, 0) {
			return r, fmt.Errorf("sandbox: la variable %s contiene NUL", k)
		}
	}

	switch {
	case r.CPU == 0:
		r.CPU = DefaultCPU
	case r.CPU < 0 || math.IsNaN(r.CPU) || math.IsInf(r.CPU, 0):
		return r, fmt.Errorf("sandbox: CPU no válida %v", r.CPU)
	}
	r.cpu = strconv.FormatFloat(r.CPU, 'f', -1, 64)

	if r.Memory == "" {
		r.Memory = DefaultMemory
	}
	if !memoryRe.MatchString(r.Memory) {
		return r, fmt.Errorf("sandbox: memoria no válida %q", r.Memory)
	}

	if r.Network == "" {
		r.Network = NetworkNone
	}
	if !networkRe.MatchString(r.Network) || r.Network == "host" {
		return r, fmt.Errorf("sandbox: red no permitida %q", r.Network)
	}

	for _, p := range r.Ports {
		if p.Container < 1 || p.Container > 65535 || p.Host < 0 || p.Host > 65535 {
			return r, fmt.Errorf("sandbox: puerto no válido %+v", p)
		}
		if p.Protocol != "" && p.Protocol != "tcp" && p.Protocol != "udp" {
			return r, fmt.Errorf("sandbox: protocolo no válido %q", p.Protocol)
		}
	}
	if len(r.Ports) > 0 && r.Network == NetworkNone {
		return r, errors.New("sandbox: publicar puertos exige una red distinta de none")
	}

	for _, m := range r.ExtraMounts {
		if !filepath.IsAbs(m.Host) {
			return r, fmt.Errorf("sandbox: montaje con ruta de anfitrión relativa %q", m.Host)
		}
		if err := safeMountPath(m.Host); err != nil {
			return r, fmt.Errorf("sandbox: montaje %q: %w", m.Host, err)
		}
		c := path.Clean(m.Container)
		if !path.IsAbs(m.Container) || c == "/" || strings.ContainsAny(m.Container, ",:\x00\n") ||
			underDir(c, WorkspaceDir) || underDir(c, OutDirInContainer) || underDir(c, srcDir) {
			return r, fmt.Errorf("sandbox: ruta de contenedor no permitida %q", m.Container)
		}
	}

	switch s.Strategy {
	case StrategyAuto:
		if rt == "podman" {
			r.strategy = StrategyOverlay
		} else {
			r.strategy = StrategyCopy
		}
	case StrategyOverlay:
		if rt != "podman" {
			return r, errors.New("sandbox: el overlay de usuario solo existe en podman")
		}
		r.strategy = StrategyOverlay
	case StrategyCopy:
		r.strategy = StrategyCopy
	default:
		return r, fmt.Errorf("sandbox: estrategia desconocida %q", s.Strategy)
	}
	return r, nil
}

func underDir(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// safeMountPath rechaza rutas que alterarían la sintaxis de -v o --mount.
func safeMountPath(p string) error {
	if strings.ContainsAny(p, ",:\x00\n\r") {
		return errors.New("la ruta contiene , : o caracteres de control")
	}
	return nil
}

// Guiones fijos que se ejecutan dentro del contenedor. El comando del usuario
// llega como "$@": nunca forma parte del texto del guion. El error estándar
// del comando va a un archivo de /out para poder transmitirlo por separado.
const (
	overlayScript = `exec "$@" 2>>` + OutDirInContainer + `/` + stderrFile
	copyScript    = `cp -a ` + srcDir + `/. ` + WorkspaceDir + `/ && ` + overlayScript
)

// buildArgs construye los argumentos de `<runtime> run`. No ejecuta nada.
func buildArgs(r resolved, name, id, outDir string) []string {
	args := []string{
		"run", "--rm",
		"--name", name,
		"--label", "wari.run=" + id,
		"--network", r.Network,
		"--cpus", r.cpu,
		"--memory", r.Memory,
		"--memory-swap", r.Memory,
		"--pids-limit", strconv.Itoa(pidsLimit),
		"--security-opt", "no-new-privileges",
		// Sin etiquetas SELinux: así no se reetiqueta el proyecto real (:z) y
		// los montajes se pueden leer en Fedora.
		"--security-opt", "label=disable",
		"--mount", "type=bind,src=" + outDir + ",dst=" + OutDirInContainer,
	}

	script := overlayScript
	switch r.strategy {
	case StrategyOverlay:
		args = append(args, "-v", r.Root+":"+WorkspaceDir+":O")
	default:
		script = copyScript
		args = append(args,
			"--mount", "type=bind,src="+r.Root+",dst="+srcDir+",ro",
			"--mount", "type=volume,dst="+WorkspaceDir,
		)
	}

	for _, m := range r.ExtraMounts {
		args = append(args, "--mount", "type=bind,src="+m.Host+",dst="+path.Clean(m.Container)+",ro")
	}

	env := map[string]string{"WARI_OUT": OutDirInContainer}
	for k, v := range r.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+env[k])
	}

	for _, p := range r.Ports {
		host := p.Host
		if host == 0 {
			host = p.Container
		}
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d/%s", host, p.Container, proto))
	}

	wd := WorkspaceDir
	if r.WorkDir != "" && r.WorkDir != "." {
		wd += "/" + r.WorkDir
	}
	args = append(args, "--workdir", wd, "--entrypoint", "sh", r.Image,
		"-c", script, "wari-sandbox")
	return append(args, r.Command...)
}
