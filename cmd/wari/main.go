// Command wari es un editor de código TUI con agentes múltiples.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/claude"
)

// version se fija en el build con -ldflags "-X main.version=...".
var version = "dev"

func main() {
	prompt := flag.String("prompt", "", "lanza un turno del agente claude con este prompt y vuelca su salida")
	model := flag.String("model", "", "modelo del agente (vacío: el predeterminado de la CLI)")
	killAfter := flag.Duration("kill-after", 0, "termina el turno y sus subprocesos después de este tiempo")
	flag.Parse()

	if *prompt == "" {
		fmt.Printf("wari %s — en desarrollo (fase 1)\n", version)
		flag.Usage()
		return
	}
	os.Exit(runTurn(agent.Task{Prompt: *prompt, Model: *model}, *killAfter))
}

// runTurn ejecuta un turno: la respuesta va a stdout y el resto de los
// eventos a stderr. Ctrl+C o -kill-after terminan el grupo completo.
func runTurn(task agent.Task, killAfter time.Duration) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	c := claude.New("")
	if err := c.Start(context.Background(), task); err != nil {
		fmt.Fprintln(os.Stderr, "[wari]", err)
		return 1
	}
	pgid := c.PGID()
	fmt.Fprintf(os.Stderr, "[wari] turno lanzado, grupo de procesos %d\n", pgid)

	kill := func(reason string) {
		fmt.Fprintf(os.Stderr, "\n[wari] %s: terminando el grupo %d\n", reason, pgid)
		if err := c.Kill(); err != nil {
			fmt.Fprintln(os.Stderr, "[wari] kill:", err)
		}
	}
	if killAfter > 0 {
		t := time.AfterFunc(killAfter, func() { kill("kill-after") })
		defer t.Stop()
	}
	go func() {
		<-sigs
		kill("señal recibida")
	}()

	for ev := range c.Events() {
		switch e := ev.(type) {
		case agent.Started:
			fmt.Fprintf(os.Stderr, "[sesión] %s (%s)\n", e.SessionID, e.Model)
		case agent.TextDelta:
			fmt.Print(e.Text)
		case agent.ToolCall:
			fmt.Fprintf(os.Stderr, "\n[herramienta] %s %s\n", e.Name, e.Input)
		case agent.FileEditProposed:
			fmt.Fprintf(os.Stderr, "[edición] %s (%s)\n", e.Path, e.Tool)
		case agent.NeedsApproval:
			fmt.Fprintf(os.Stderr, "[denegado] %s %s\n", e.Tool, e.Input)
		case agent.Usage:
			fmt.Fprintf(os.Stderr, "\n[uso] entrada %d, salida %d, caché %d/%d, US$%.4f\n",
				e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.CostUSD)
		case agent.Error:
			fmt.Fprintln(os.Stderr, "[error]", e.Err)
		case agent.Done:
			fmt.Fprintf(os.Stderr, "[fin] sesión %s, %d turnos internos\n", e.SessionID, e.Turns)
			// El proceso ya salió (contrato de Done): Kill solo cierra el agente.
			c.Kill()
			if e.IsError {
				return 1
			}
			return 0
		}
	}
	// El canal se cerró sin Done: el turno fue terminado.
	return 2
}
