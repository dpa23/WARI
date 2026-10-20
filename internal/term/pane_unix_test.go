//go:build unix

package term

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/proc"
)

// waitScreen espera a que la pantalla contenga want.
func waitScreen(t *testing.T, p *Pane, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var screen string
	for time.Now().Before(deadline) {
		screen = strings.Join(p.Lines(), "\n")
		if strings.Contains(screen, want) {
			return screen
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("la pantalla no mostró %q:\n%s", want, screen)
	return ""
}

func TestPaneRunsInteractiveCLI(t *testing.T) {
	notified := make(chan struct{}, 1)
	p, err := Start(context.Background(), proc.Spec{
		Path: "sh",
		Args: []string{"-c", `printf '\033]0;trabajando\007hola\n'; read x; printf 'eco:%s\n' "$x"; stty size`},
	}, 80, 24, func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	waitScreen(t, p, "hola")
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Error("el cambio de pantalla debe avisarse")
	}
	if got := p.Title(); got != "trabajando" {
		t.Errorf("Title = %q", got)
	}

	p.Resize(100, 30)
	if c, r := p.Size(); c != 100 || r != 30 {
		t.Errorf("Size = %dx%d", c, r)
	}
	for _, r := range "mundo" {
		p.SendKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	p.SendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	screen := waitScreen(t, p, "30 100")
	if !strings.Contains(screen, "eco:mundo") {
		t.Errorf("la CLI debe recibir lo tecleado y el tamaño nuevo:\n%s", screen)
	}
	if lines := p.Lines(); len(lines) != 30 {
		t.Errorf("Lines devuelve %d filas, quería 30", len(lines))
	}
	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Error("Done debe cerrarse cuando la CLI termina")
	}
}

func TestPaneCloseKillsGroup(t *testing.T) {
	p, err := Start(context.Background(), proc.Spec{Path: "sh", Args: []string{"-c", "sleep 60 & wait"}, KillGrace: 200 * time.Millisecond}, 80, 24, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Process().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("Close debe terminar el proceso y su grupo")
	}
}

func TestCleanTitle(t *testing.T) {
	if got := cleanTitle("✳ Claude\x00 Code\xff "); got != "✳ Claude Code" {
		t.Errorf("cleanTitle = %q", got)
	}
}
