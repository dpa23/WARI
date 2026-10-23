package icons

import (
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func tables() map[string]map[Glyph]set {
	return map[string]map[Glyph]set{"state": states, "cli": clis, "ui": ui}
}

func TestAnchosPorModo(t *testing.T) {
	defer Use(Current())
	for kind, tab := range tables() {
		for g, s := range tab {
			for _, c := range []struct {
				m      Mode
				glyph  string
				maxW   int
				minW   int
				nombre string
			}{{Nerd, s.nerd, 1, 1, "nerd"}, {ASCII, s.ascii, 1, 1, "ascii"}, {Emoji, s.emoji, 2, 1, "emoji"}} {
				if w := lipgloss.Width(c.glyph); w < c.minW || w > c.maxW {
					t.Errorf("%s/%s %s: ancho %d fuera de [%d,%d]", kind, g, c.nombre, w, c.minW, c.maxW)
				}
			}
		}
	}
}

func TestNoVaciosSalvoOff(t *testing.T) {
	defer Use(Current())
	for _, m := range []Mode{Nerd, Emoji, ASCII, Off} {
		Use(m)
		for kind, tab := range tables() {
			for g := range tab {
				var got string
				switch kind {
				case "state":
					got = State(string(g))
				case "cli":
					got = CLI(string(g))
				default:
					got = UI(string(g))
				}
				if (m == Off) != (got == "") {
					t.Errorf("modo %d %s/%s: %q", m, kind, g, got)
				}
			}
		}
	}
	Use(Nerd)
	if State("nope") != "" {
		t.Error("un estado desconocido debe dar vacío")
	}
}

func TestDistintosDentroDeUnModo(t *testing.T) {
	for kind, tab := range tables() {
		for _, m := range []Mode{Nerd, Emoji, ASCII} {
			seen := map[string]Glyph{}
			for g, s := range tab {
				v := s.in(m)
				if o, dup := seen[v]; dup {
					t.Errorf("%s modo %d: %s y %s comparten %q", kind, m, g, o, v)
				}
				seen[v] = g
			}
		}
	}
}

func TestPorOmisionEmoji(t *testing.T) {
	if Current() != Emoji {
		t.Fatalf("modo por omisión %d, quiero Emoji", Current())
	}
}

func TestParseMode(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	casos := []struct {
		v    string
		env  map[string]string
		want Mode
		err  bool
	}{
		{"nerd", nil, Nerd, false},
		{"EMOJI", nil, Emoji, false},
		{"ascii", nil, ASCII, false},
		{"off", nil, Off, false},
		{"auto", nil, Emoji, false},
		{"", map[string]string{"TERM": "xterm-kitty"}, Nerd, false},
		{"auto", map[string]string{"TERM_PROGRAM": "WezTerm"}, Nerd, false},
		{"auto", map[string]string{"TERM_PROGRAM": "ghostty"}, Nerd, false},
		{"auto", map[string]string{"WARI_NERD_FONT": "1"}, Nerd, false},
		{"raro", nil, Off, true},
	}
	for _, c := range casos {
		got, err := ParseMode(c.v, env(c.env))
		if (err != nil) != c.err || got != c.want {
			t.Errorf("ParseMode(%q,%v) = %d,%v", c.v, c.env, got, err)
		}
	}
	if m, err := ParseMode("auto", nil); err != nil || m != Emoji {
		t.Errorf("env nil: %d %v", m, err)
	}
}
