package sandbox

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/dpa23/wari/internal/proc"
)

// Runtime es el CLI de contenedores detectado.
type Runtime struct {
	Name    string // "podman" o "docker"
	Path    string
	Version string // salida de --version
}

// DetectRuntime busca podman y luego docker en el PATH y comprueba que
// responden a --version. prefer ("podman" o "docker") se prueba primero. No
// comprueba que el demonio de docker esté en marcha.
func DetectRuntime(ctx context.Context, prefer string) (Runtime, error) {
	order := []string{"podman", "docker"}
	if prefer == "docker" {
		order = []string{"docker", "podman"}
	}
	for _, name := range order {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		out, err := quick(ctx, 5*time.Second, p, "--version")
		if err != nil {
			continue
		}
		return Runtime{Name: name, Path: p, Version: strings.TrimSpace(out)}, nil
	}
	return Runtime{}, ErrNoRuntime
}

// quick ejecuta un comando corto bajo internal/proc y devuelve su salida
// estándar. Un error incluye el final de stderr.
func quick(ctx context.Context, timeout time.Duration, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	p, err := proc.Start(ctx, proc.Spec{Path: path, Args: args, KillGrace: 500 * time.Millisecond})
	if err != nil {
		return "", err
	}
	defer p.Close()
	out, _ := io.ReadAll(io.LimitReader(p.Stdout(), 1<<20))
	if err := p.Wait(); err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", path, strings.Join(args, " "), err,
			strings.TrimSpace(p.Stderr()))
	}
	return string(out), nil
}
