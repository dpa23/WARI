package editor

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Aceptación de la fase 3: abrir un .java de 5000 líneas sin lag, editar,
// deshacer 100 pasos y guardar sin corromper (bytes idénticos al original).
func TestAcceptanceLargeJavaFile(t *testing.T) {
	orig, err := os.ReadFile("../../testdata/java/Grande.java")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Grande.java")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	t.Logf("abrir: %v", d)
	if d > 200*time.Millisecond {
		t.Errorf("abrir 5000 líneas tardó %v", d)
	}
	if n := b.LineCount(); n < 5000 {
		t.Fatalf("LineCount = %d", n)
	}

	// Reloj falso: cada edición queda separada por más que la pausa de
	// agrupación, así cada una es un paso de undo.
	var now int64
	b.clock = func() int64 { now += int64(2 * time.Second); return now }

	start = time.Now()
	for i := range 100 {
		b.SetCursorPos(Pos{Line: i * 40, Col: 0})
		if i%2 == 0 {
			b.InsertText("// editado\n")
		} else {
			b.DeleteForward()
		}
	}
	d = time.Since(start)
	t.Logf("100 ediciones: %v", d)
	if d > 500*time.Millisecond {
		t.Errorf("100 ediciones tardaron %v", d)
	}
	for i := range 100 {
		if !b.Undo() {
			t.Fatalf("Undo %d no hizo nada", i+1)
		}
	}
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Errorf("el archivo guardado difiere del original (%d vs %d bytes)", len(got), len(orig))
	}
}
