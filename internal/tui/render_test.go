package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2/compat"
)

func TestAdaptiveColorsRender(t *testing.T) {
	st := defaultStyles()

	// Guardar estado original
	origDark := compat.HasDarkBackground
	defer func() { compat.HasDarkBackground = origDark }()

	// Probar en modo oscuro
	compat.HasDarkBackground = true
	editDark := st.edit.Render("edición")
	deniedDark := st.denied.Render("denegado")
	warnDark := st.warn.Render("aviso")

	if editDark == "" || deniedDark == "" || warnDark == "" {
		t.Fatal("los estilos en modo oscuro no deben renderizar vacío")
	}

	// Probar en modo claro
	compat.HasDarkBackground = false
	editLight := st.edit.Render("edición")
	deniedLight := st.denied.Render("denegado")
	warnLight := st.warn.Render("aviso")

	if editLight == "" || deniedLight == "" || warnLight == "" {
		t.Fatal("los estilos en modo claro no deben renderizar vacío")
	}

	// Las secuencias generadas deben ser válidas y contener el texto
	for _, text := range []string{"edición", "denegado", "aviso"} {
		if !strings.Contains(editDark, "edición") || !strings.Contains(editLight, "edición") {
			t.Errorf("render de edición no contiene el texto original: %q / %q", editDark, editLight)
		}
		if !strings.Contains(deniedDark, "denegado") || !strings.Contains(deniedLight, "denegado") {
			t.Errorf("render de denegado no contiene el texto original: %q / %q", deniedDark, deniedLight)
		}
		if !strings.Contains(warnDark, "aviso") || !strings.Contains(warnLight, "aviso") {
			t.Errorf("render de aviso no contiene el texto original: %q / %q", warnDark, warnLight)
		}
		_ = text
	}

	// Verificar forKind
	for _, k := range []entryKind{kindEdit, kindDenied, kindWarn} {
		style := st.forKind(k)
		r := style.Render("test")
		if !strings.Contains(r, "test") {
			t.Errorf("forKind(%v) falló al renderizar: %q", k, r)
		}
	}
}

func TestFormatUsageShowsTokensFirst(t *testing.T) {
	if got := formatUsage(950, 12_345, 0); got != "↑950 ↓12.3k tok" {
		t.Errorf("sin costo: %q", got)
	}
	if got := formatUsage(1_500_000, 2, 0.01); got != "↑1.5M ↓2 tok · US$0.0100" {
		t.Errorf("con costo: %q", got)
	}
}
