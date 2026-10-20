package turn

import (
	"context"
	"os/exec"
	"reflect"
	"testing"
)

func TestLines(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh no está en el PATH")
	}
	got, err := Lines(context.Background(), sh, "-c", "printf 'uno\\n\\n  dos  \\ntres\\n'")
	if err != nil || !reflect.DeepEqual(got, []string{"uno", "dos", "tres"}) {
		t.Fatalf("Lines = %q, %v", got, err)
	}
	if _, err := Lines(context.Background(), sh, "-c", "echo roto >&2; exit 3"); err == nil {
		t.Error("un código de salida distinto de cero debe ser un error")
	}
	if _, err := Lines(context.Background(), "no-existe-wari-xyz"); err == nil {
		t.Error("un binario inexistente debe ser un error")
	}
}
