package lsp

import (
	"context"
	"io"
)

// Process es el proceso de un servidor ya lanzado. Lo satisface *proc.Process
// de internal/proc (con Spec.Stdin); el
// paquete lsp no importa proc para que se pueda probar con servidores falsos.
type Process interface {
	// Stdin es el extremo de escritura hacia el servidor; cerrarlo señala EOF.
	Stdin() io.WriteCloser
	Stdout() io.Reader
	// PGID es el grupo de procesos del servidor (en Unix, el PID del líder).
	PGID() int
	// Kill termina el grupo completo (ordenado y luego forzado); es idempotente.
	Kill() error
	// Close cierra los extremos de lectura de stdout y stderr.
	Close() error
	Done() <-chan struct{}
	Stderr() string
}

// Launcher lanza un servidor en el directorio dir. El proceso vive hasta que
// ctx se cancele o se llame a Kill: debe ser un contexto de larga vida, no el
// de una petición.
type Launcher func(ctx context.Context, path string, args []string, dir string) (Process, error)
