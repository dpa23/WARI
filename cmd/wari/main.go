// Command wari es un editor de código TUI con agentes múltiples.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/agent"
	"github.com/dpa23/wari/internal/agent/claude"
	"github.com/dpa23/wari/internal/config"
	"github.com/dpa23/wari/internal/tui"
	"github.com/dpa23/wari/internal/tui/layout"
)

// version se fija en el build con -ldflags "-X main.version=...".
var version = "dev"

func main() {
	prompt := flag.String("prompt", "", "sin TUI: lanza un turno del agente claude con este prompt y vuelca su salida")
	model := flag.String("model", "", "modelo del agente (vacío: el predeterminado de la CLI)")
	killAfter := flag.Duration("kill-after", 0, "con -prompt: termina el turno y sus subprocesos después de este tiempo")
	showVersion := flag.Bool("version", false, "muestra la versión y sale")
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Printf("wari %s\n", version)
	case *prompt != "":
		os.Exit(runTurn(agent.Task{Prompt: *prompt, Model: *model}, *killAfter))
	case *killAfter > 0:
		fmt.Fprintln(os.Stderr, "wari: -kill-after solo se usa junto con -prompt")
		os.Exit(2)
	case flag.NArg() > 1:
		fmt.Fprintln(os.Stderr, "wari: se espera como máximo un argumento (directorio o archivo)")
		os.Exit(2)
	default:
		root, file, err := resolveTarget(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, "wari:", err)
			os.Exit(2)
		}
		os.Exit(runTUI(agent.Task{Model: *model}, root, file))
	}
}

// resolveTarget interpreta el argumento posicional: vacío es el directorio
// actual; un directorio es la raíz; un archivo abre su carpeta como raíz y el
// archivo en el editor.
func resolveTarget(arg string) (root, file string, err error) {
	if arg == "" {
		arg = "."
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if st.IsDir() {
		return abs, "", nil
	}
	return filepath.Dir(abs), abs, nil
}

// loadKeymap carga los atajos (predeterminados, usuario y proyecto). Los
// errores y conflictos se informan por stderr sin abortar.
func loadKeymap(root string) config.Keymap {
	userDir, err := config.UserDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wari: aviso: no se encontró el directorio de configuración:", err)
		userDir = ""
	}
	km, err := config.Load(userDir, filepath.Join(root, ".wari"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wari: aviso: atajos:", err, "(se usan los predeterminados)")
		km = config.Default()
	}
	for _, c := range km.Conflicts() {
		fmt.Fprintln(os.Stderr, "wari: aviso:", c)
	}
	return km
}

// runTUI abre el layout (árbol, editor y chat) en root, con file abierto si
// no está vacío. Al salir, por la vía que sea (atajo, señal o pánico en el
// bucle de eventos), no debe quedar ningún agente vivo: el defer cierra al
// agente, que mata el turno en curso con todos sus descendientes.
func runTUI(task agent.Task, root, file string) int {
	ag := claude.New("")
	defer ag.Close()

	p := tea.NewProgram(layout.New(layout.Config{
		Root:   root,
		File:   file,
		Keymap: loadKeymap(root),
		Chat:   tui.Config{Agent: ag, Task: task},
	}))

	// Bubble Tea atrapa SIGINT y SIGTERM, pero no SIGHUP: al cerrarse la
	// terminal, wari moriría sin cerrar al agente, que vive en su propio
	// grupo de procesos y no recibe el SIGHUP de la terminal.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		<-hup
		p.Kill()
	}()

	if _, err := p.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		fmt.Fprintln(os.Stderr, "wari:", err)
		return 1
	}
	return 0
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
		if err := c.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "[wari] cerrar:", err)
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
			// El proceso ya salió (contrato de Done): Close solo cierra el agente.
			c.Close()
			if e.IsError {
				return 1
			}
			return 0
		}
	}
	// El canal se cerró sin Done: el turno fue terminado.
	return 2
}
