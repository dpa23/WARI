package lsp

import (
	"runtime"
	"testing"
)

func TestUTF16Conversions(t *testing.T) {
	line := "a€😀b" // a=1 byte/1 u16, €=3/1, 😀=4/2, b=1/1
	tests := []struct{ byteOff, u16 int }{
		{0, 0}, {1, 1}, {4, 2}, {8, 4}, {9, 5},
	}
	for _, tt := range tests {
		if got := ByteToUTF16(line, tt.byteOff); got != tt.u16 {
			t.Errorf("ByteToUTF16(%d)=%d, quería %d", tt.byteOff, got, tt.u16)
		}
		if got := UTF16ToByte(line, tt.u16); got != tt.byteOff {
			t.Errorf("UTF16ToByte(%d)=%d, quería %d", tt.u16, got, tt.byteOff)
		}
	}
	if got := ByteToUTF16(line, 99); got != 5 {
		t.Errorf("fuera de rango=%d", got)
	}
	if got := ByteToUTF16(line, -3); got != 0 {
		t.Errorf("negativo=%d", got)
	}
	if got := UTF16ToByte(line, 99); got != len(line) {
		t.Errorf("columna fuera de rango=%d", got)
	}
}

func TestFileURIRoundTrip(t *testing.T) {
	dir := t.TempDir() + "/con espacio/ñ"
	uri := FileURI(dir)
	if len(uri) < 8 || uri[:8] != "file:///" {
		t.Fatalf("uri=%q", uri)
	}
	back, ok := URIToPath(uri)
	if !ok || back != dir {
		t.Fatalf("back=%q ok=%v, quería %q", back, ok, dir)
	}
	if _, ok := URIToPath("http://x/y"); ok {
		t.Error("http no es un URI de archivo")
	}
	if runtime.GOOS == "windows" {
		return
	}
	if got := FileURI("/a/b c"); got != "file:///a/b%20c" {
		t.Errorf("FileURI=%q", got)
	}
}
