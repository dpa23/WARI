package theme

import "testing"

func TestFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		env         map[string]string
		dark, known bool
		describe    string
	}{
		{map[string]string{"WARI_THEME": "light"}, false, true, "WARI_THEME light"},
		{map[string]string{"WARI_THEME": "DARK"}, true, true, "WARI_THEME dark"},
		{map[string]string{"COLORFGBG": "15;0"}, true, true, "COLORFGBG fondo negro"},
		{map[string]string{"COLORFGBG": "0;15"}, false, true, "COLORFGBG fondo blanco"},
		{map[string]string{"COLORFGBG": "0;default;15"}, false, true, "COLORFGBG de tres campos"},
		{map[string]string{}, true, false, "sin pistas: oscuro y desconocido"},
	}
	for _, c := range cases {
		if d, k := fromEnv(env(c.env)); d != c.dark || k != c.known {
			t.Errorf("%s: dark=%v known=%v", c.describe, d, k)
		}
	}
}

func TestSetDarkMarcaComoResuelto(t *testing.T) {
	SetDark(false)
	if Dark() || NeedsQuery() {
		t.Error("tras SetDark no hay consulta pendiente y el fondo es claro")
	}
	SetDark(true)
	if !Dark() {
		t.Error("SetDark(true)")
	}
}
