package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dpa23/wari/internal/lsp"
	"github.com/dpa23/wari/internal/proc"
)

// lspLauncher lanza un servidor de lenguaje como líder de su propio grupo
// (internal/proc): al cerrar el gestor, o al cancelarse ctx, el grupo
// completo termina, JVM de jdtls incluida.
func lspLauncher(ctx context.Context, path string, args []string, dir string) (lsp.Process, error) {
	p, err := proc.Start(ctx, proc.Spec{Path: path, Args: args, Dir: dir, Stdin: true})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// newLSP crea el gestor de servidores de lenguaje. No lanza nada: los
// servidores arrancan bajo demanda, cuando el usuario enciende el LSP y abre
// un archivo que ellos atienden. ctx es el de larga vida de los servidores.
func newLSP(ctx context.Context) *lsp.Manager {
	return lsp.NewManager(ctx, lspLauncher, lsp.Options{ClientName: "wari", ClientVersion: version})
}

// closeLSP cierra todos los servidores y avisa por stderr si alguno no salió
// limpio (el grupo se mata igualmente).
func closeLSP(m *lsp.Manager) {
	if err := m.Shutdown(); err != nil {
		fmt.Fprintln(os.Stderr, "wari: aviso: LSP:", err)
	}
}
