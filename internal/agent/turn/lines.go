package turn

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dpa23/wari/internal/proc"
)

// listTimeout acota los comandos de consulta (`agy models`...).
const listTimeout = 15 * time.Second

// maxListLines acota lo que se lee de una consulta.
const maxListLines = 500

// Lines ejecuta `<bin> <args...>` como proceso de su propio grupo (internal/proc)
// y devuelve las líneas no vacías de su salida. Es para consultas cortas de
// solo lectura, nunca para turnos de agente. bin se busca en el PATH.
func Lines(ctx context.Context, bin string, args ...string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	p, err := proc.Start(ctx, proc.Spec{Path: bin, Args: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bin, err)
	}
	defer p.Close()
	var lines []string
	sc := bufio.NewScanner(p.Stdout())
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() && len(lines) < maxListLines {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if err := p.Wait(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(p.Stderr()))
	}
	return lines, sc.Err()
}
