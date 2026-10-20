package theme

import "testing"

func TestDetect(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	asked := false
	query := func() bool { asked = true; return true }
	cases := []struct {
		env      map[string]string
		want     bool
		wantAsk  bool
		describe string
	}{
		{map[string]string{"WARI_THEME": "light"}, false, false, "WARI_THEME light"},
		{map[string]string{"WARI_THEME": "DARK"}, true, false, "WARI_THEME dark"},
		{map[string]string{"COLORFGBG": "15;0"}, true, false, "COLORFGBG fondo negro"},
		{map[string]string{"COLORFGBG": "0;15"}, false, false, "COLORFGBG fondo blanco"},
		{map[string]string{"COLORFGBG": "0;default;15"}, false, false, "COLORFGBG de tres campos"},
		{map[string]string{}, true, true, "sin pistas consulta"},
	}
	for _, c := range cases {
		asked = false
		if got := detect(env(c.env), query); got != c.want || asked != c.wantAsk {
			t.Errorf("%s: dark=%v consultó=%v", c.describe, got, asked)
		}
	}
}
