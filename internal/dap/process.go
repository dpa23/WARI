package dap

import (
	"context"
	"io"

	"github.com/dpa23/wari/internal/proc"
)

// Process es el proceso de un adaptador ya lanzado. Lo satisface
// *proc.Process de internal/proc (con Spec.Stdin).
type Process interface {
	// Stdin es el extremo de escritura hacia el adaptador; cerrarlo señala EOF.
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// PGID es el grupo de procesos del adaptador (en Unix, el PID del líder).
	PGID() int
	// Kill termina el grupo completo (ordenado y luego forzado); es idempotente.
	Kill() error
	// Close cierra los extremos de lectura de stdout y stderr.
	Close() error
	Done() <-chan struct{}
	Stderr() string
}

// Launcher lanza un adaptador en el directorio dir. El proceso vive hasta que
// ctx se cancele o se cierre el cliente: debe ser un contexto de larga vida,
// no el de una petición. Es inyectable: la integración con el sandbox
// (fase 13) puede sustituir DefaultLauncher sin tocar este paquete.
type Launcher func(ctx context.Context, path string, args []string, dir string) (Process, error)

// DefaultLauncher lanza el adaptador con internal/proc, como líder de su
// propio grupo y con un pipe en stdin.
func DefaultLauncher(ctx context.Context, path string, args []string, dir string) (Process, error) {
	p, err := proc.Start(ctx, proc.Spec{Path: path, Args: args, Dir: dir, Stdin: true})
	if err != nil {
		return nil, err
	}
	return p, nil
}
