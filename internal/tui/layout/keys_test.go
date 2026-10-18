package layout

import (
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/internal/config"
)

// keyRecorder es un modelo que anota las teclas que le llegan.
type keyRecorder struct {
	mu   *sync.Mutex
	keys *[]string
}

func (keyRecorder) Init() tea.Cmd { return nil }

func (r keyRecorder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		r.mu.Lock()
		*r.keys = append(*r.keys, k.String())
		r.mu.Unlock()
	}
	return r, nil
}

func (keyRecorder) View() tea.View { return tea.NewView("") }

// TestDebugKeysDecodeInBubbleTea comprueba, con las secuencias que mandan las
// terminales xterm (CSI n ~ y CSI n ; 2 ~), que bubbletea v2 entrega las
// teclas F con los nombres que usa config.Default: f5, f8, f9, f10, f11,
// shift+f5 y shift+f11. Las alternativas alt+letra llegan como ESC letra.
func TestDebugKeysDecodeInBubbleTea(t *testing.T) {
	seqs := []struct{ in, want string }{
		{"\x1b[15~", "f5"},
		{"\x1b[19~", "f8"},
		{"\x1b[20~", "f9"},
		{"\x1b[21~", "f10"},
		{"\x1b[23~", "f11"},
		{"\x1b[15;2~", "shift+f5"},
		{"\x1b[23;2~", "shift+f11"},
		{"\x1bb", "alt+b"},
		{"\x1bq", "alt+q"},
	}
	pr, pw := io.Pipe()
	var mu sync.Mutex
	var got []string
	p := tea.NewProgram(keyRecorder{mu: &mu, keys: &got},
		tea.WithInput(pr), tea.WithOutput(io.Discard), tea.WithoutSignals(), tea.WithoutRenderer())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()

	for _, s := range seqs {
		if _, err := pw.Write([]byte(s.in)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond) // una secuencia por lectura
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= len(seqs) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.Quit()
	pw.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	for i, s := range seqs {
		if i >= len(got) || got[i] != s.want {
			t.Fatalf("secuencia %q: llegó %v, quería %q (todas: %v)", s.in, got, s.want, got)
		}
	}
}

// TestDefaultKeysReachDebugActions comprueba que cada tecla por omisión de las
// acciones de ejecución y depuración activa su acción y ninguna otra.
func TestDefaultKeysReachDebugActions(t *testing.T) {
	m := newLayout(t, Config{}, 100, 30)
	km := config.Default()
	actions := []string{"run_project", "run_stop", "promote_artifact", "toggle_breakpoint",
		"debug_start", "debug_continue", "debug_next", "debug_step_in", "debug_step_out", "debug_stop"}
	for _, a := range actions {
		for _, ks := range km[a] {
			k := parseKey(t, ks)
			var hit []string
			for _, other := range actions {
				if m.is(k, other) {
					hit = append(hit, other)
				}
			}
			if !slices.Equal(hit, []string{a}) {
				t.Errorf("tecla %q (%s) activa %v", ks, a, hit)
			}
		}
	}
	if c := km.Conflicts(); len(c) != 0 {
		t.Fatalf("conflictos: %v", c)
	}
}

// parseKey arma el KeyPressMsg de una tecla del mapa ("f9", "alt+b", "shift+f11").
func parseKey(t *testing.T, s string) tea.KeyPressMsg {
	t.Helper()
	var mod tea.KeyMod
	for {
		switch {
		case len(s) > 4 && s[:4] == "alt+":
			mod |= tea.ModAlt
			s = s[4:]
			continue
		case len(s) > 6 && s[:6] == "shift+":
			mod |= tea.ModShift
			s = s[6:]
			continue
		}
		break
	}
	fkeys := map[string]rune{"f5": tea.KeyF5, "f8": tea.KeyF8, "f9": tea.KeyF9, "f10": tea.KeyF10, "f11": tea.KeyF11}
	if code, ok := fkeys[s]; ok {
		return tea.KeyPressMsg{Code: code, Mod: mod}
	}
	if len([]rune(s)) == 1 {
		return tea.KeyPressMsg{Code: []rune(s)[0], Mod: mod}
	}
	t.Fatalf("tecla no soportada por el test: %q", s)
	return tea.KeyPressMsg{}
}
