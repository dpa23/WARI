package editor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- Open and Save ---------------------------------------------------------

func writeTemp(t *testing.T, content []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "wari-test-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func TestOpenLF(t *testing.T) {
	content := []byte("line1\nline2\nline3\n")
	path := writeTemp(t, content)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.LineEnding != "\n" {
		t.Fatalf("line ending: %q", fi.LineEnding)
	}
	if !fi.TrailingNL {
		t.Fatal("expected TrailingNL")
	}
	mustLines(t, b, "line1", "line2", "line3")
}

func TestOpenCRLF(t *testing.T) {
	content := []byte("line1\r\nline2\r\nline3\r\n")
	path := writeTemp(t, content)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.LineEnding != "\r\n" {
		t.Fatalf("line ending: %q", fi.LineEnding)
	}
	if !fi.TrailingNL {
		t.Fatal("expected TrailingNL")
	}
	mustLines(t, b, "line1", "line2", "line3")
}

func TestOpenNoTrailingNL(t *testing.T) {
	content := []byte("hello\nworld")
	path := writeTemp(t, content)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.TrailingNL {
		t.Fatal("expected no TrailingNL")
	}
	mustLines(t, b, "hello", "world")
}

func TestOpenEmpty(t *testing.T) {
	path := writeTemp(t, []byte{})
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.TrailingNL {
		t.Fatal("empty file should not have TrailingNL")
	}
	mustLines(t, b, "")
	_ = b
}

func TestSavePreservesLF(t *testing.T) {
	original := []byte("hello\nworld\n")
	path := writeTemp(t, original)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("bytes differ\ngot:  %q\nwant: %q", got, original)
	}
}

func TestSavePreservesCRLF(t *testing.T) {
	original := []byte("hello\r\nworld\r\n")
	path := writeTemp(t, original)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("bytes differ\ngot:  %q\nwant: %q", got, original)
	}
}

func TestSaveNoTrailingNL(t *testing.T) {
	original := []byte("hello\nworld")
	path := writeTemp(t, original)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("bytes differ\ngot:  %q\nwant: %q", got, original)
	}
}

// TestSaveNoCorruption: open, edit, undo all, save, compare with original.
func TestSaveNoCorruption(t *testing.T) {
	original := []byte("hello\nworld\nfoo\n")
	path := writeTemp(t, original)

	clk, advance := newFakeClock(0)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b.clock = clk

	// Make several edits.
	b.MoveDocEnd(false)
	advance(200)
	b.InsertText("bar\n")
	advance(200)
	b.InsertRune('!')
	advance(2_000_000_000) // big pause → new group
	b.MoveDocStart(false)
	b.InsertText("PREFIX\n")

	// Undo all changes.
	for b.Undo() {
	}

	// Save and compare with original.
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("bytes differ after undo+save\ngot:  %q\nwant: %q", got, original)
	}
}

// TestSaveAtomic: verify temp file is in same directory and rename succeeds.
func TestSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("initial\n"), 0644); err != nil {
		t.Fatal(err)
	}
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b.MoveDocEnd(false)
	b.InsertText("appended")
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "appended") {
		t.Fatalf("saved file missing edit: %q", got)
	}
	// No temp files should remain.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".wari-save-") {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

// TestSavePermissions: permissions of the original file are restored.
func TestSavePermissions(t *testing.T) {
	path := writeTemp(t, []byte("data\n"))
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b.InsertText("x")
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("perm: %o", info.Mode().Perm())
	}
}

// TestSaveCRLFNoCorruption: CRLF file survives edit+undo+save.
func TestSaveCRLFNoCorruption(t *testing.T) {
	original := []byte("alpha\r\nbeta\r\ngamma\r\n")
	path := writeTemp(t, original)
	b, fi, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b.MoveDocEnd(false)
	b.InsertText("delta")
	b.Undo()
	if err := b.Save(fi); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Fatalf("CRLF bytes differ\ngot:  %q\nwant: %q", got, original)
	}
}
