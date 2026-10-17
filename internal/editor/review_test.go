package editor

import (
	"os"
	"path/filepath"
	"testing"
)

// stepClock returns a clock that advances 2 s per call, which is always larger
// than the default undo group pause (1 s), so each edit is its own group.
func stepClock() func() int64 {
	var now int64
	return func() int64 { now += 2_000_000_000; return now }
}

func mustText(t *testing.T, b *Buffer, step, want string) {
	t.Helper()
	if got := b.String(); got != want {
		t.Fatalf("%s: text = %q, want %q", step, got, want)
	}
}

func mustSelInvariant(t *testing.T, b *Buffer, step string) {
	t.Helper()
	if b.sel.Active != b.cursor || b.sel.Anchor != b.cursor {
		t.Fatalf("%s: selection %+v out of sync with cursor %+v", step, b.sel, b.cursor)
	}
}

// Review finding 1: redo of typed text must not clear the rest of the redo stack.
func TestRedoThreeSeparateInsertsRestoresAll(t *testing.T) {
	b := newWithClock(stepClock())
	for _, r := range "abc" {
		b.InsertRune(r)
	}
	for i := 0; i < 3; i++ {
		if !b.Undo() {
			t.Fatalf("undo %d returned false", i)
		}
	}
	mustText(t, b, "after undo x3", "")
	for i := 0; i < 3; i++ {
		if !b.Redo() {
			t.Fatalf("redo %d returned false", i)
		}
	}
	mustText(t, b, "after redo x3", "abc")
	mustSelInvariant(t, b, "after redo x3")
	if b.Redo() {
		t.Fatalf("extra redo should return false")
	}
}

func TestRedoNewlinesKeepRedoStack(t *testing.T) {
	b := newWithClock(stepClock())
	b.InsertRune('a')
	b.InsertRune('\n')
	b.InsertRune('b')
	want := b.String()
	for i := 0; i < 3; i++ {
		b.Undo()
	}
	mustText(t, b, "undo", "")
	for i := 0; i < 3; i++ {
		if !b.Redo() {
			t.Fatalf("redo %d returned false", i)
		}
	}
	mustText(t, b, "redo", want)
}

// Review finding 2: Undo, Redo, Undo must not duplicate groups or panic.
func TestUndoRedoUndoNoDuplicateGroup(t *testing.T) {
	b := newWithClock(stepClock())
	b.InsertRune('a')
	b.Undo()
	b.Redo()
	b.Undo()
	mustText(t, b, "undo,redo,undo", "")
	if b.Undo() {
		t.Fatalf("second undo should have nothing to undo")
	}
	mustText(t, b, "extra undo", "")
	if len(b.pending) != 0 {
		t.Fatalf("pending not empty after redo/undo: %d", len(b.pending))
	}
	b.Redo()
	mustText(t, b, "redo", "a")
}

func TestUndoRedoAlternatingSequence(t *testing.T) {
	b := newWithClock(stepClock())
	states := []string{""}
	for _, r := range "wxyz" {
		b.InsertRune(r)
		states = append(states, b.String())
	}
	// Walk down and up the history several times, comparing at every step.
	pos := len(states) - 1
	moves := []int{-1, -1, +1, -1, -1, -1, +1, +1, +1, +1, -1, +1, -1, -1, -1, -1, +1}
	for i, m := range moves {
		step := "step " + string(rune('0'+i%10))
		if m < 0 {
			ok := b.Undo()
			if ok != (pos > 0) {
				t.Fatalf("%s: Undo ok=%v at pos %d", step, ok, pos)
			}
			if ok {
				pos--
			}
		} else {
			ok := b.Redo()
			if ok != (pos < len(states)-1) {
				t.Fatalf("%s: Redo ok=%v at pos %d", step, ok, pos)
			}
			if ok {
				pos++
			}
		}
		mustText(t, b, step, states[pos])
		mustSelInvariant(t, b, step)
	}
}

func TestUndoRedoMixedOpsSequence(t *testing.T) {
	b := newWithClock(stepClock())
	b.InsertText("hello world")
	s1 := b.String()
	b.SetCursorPos(Pos{0, 5})
	b.DeleteForward()
	s2 := b.String()
	b.DeleteBackward()
	s3 := b.String()
	b.InsertRune('\n')
	s4 := b.String()
	b.DeleteBackward() // merge up
	s5 := b.String()
	states := []string{"", s1, s2, s3, s4, s5}
	pos := len(states) - 1
	for round := 0; round < 3; round++ {
		for pos > 0 {
			if !b.Undo() {
				t.Fatalf("round %d: undo failed at %d", round, pos)
			}
			pos--
			mustText(t, b, "undo", states[pos])
			mustSelInvariant(t, b, "undo")
		}
		for pos < len(states)-1 {
			if !b.Redo() {
				t.Fatalf("round %d: redo failed at %d", round, pos)
			}
			pos++
			mustText(t, b, "redo", states[pos])
			mustSelInvariant(t, b, "redo")
		}
	}
}

// A new edit after undo must still discard the redo stack.
func TestEditAfterUndoClearsRedo(t *testing.T) {
	b := newWithClock(stepClock())
	b.InsertRune('a')
	b.InsertRune('b')
	b.Undo()
	b.InsertRune('c')
	if b.Redo() {
		t.Fatalf("redo should be empty after a new edit")
	}
	mustText(t, b, "text", "ac")
}

// Review finding 3: redo of Delete must keep selection in sync with cursor.
func TestRedoDeleteForwardSyncsSelection(t *testing.T) {
	b := bufFromLines("abcd")
	b.clock = stepClock()
	b.DeleteForward() // "bcd"
	b.MoveRight(false)
	b.DeleteForward() // "bd"
	b.Undo()
	b.Undo()
	mustText(t, b, "undo", "abcd")
	b.Redo()
	mustSelInvariant(t, b, "redo 1")
	b.Redo()
	mustText(t, b, "redo 2", "bd")
	mustCursor(t, b, 0, 1)
	mustSelInvariant(t, b, "redo 2")
}

// Review finding 4: empty LineEnding must default to "\n".
func TestSaveEmptyLineEndingDefaultsToLF(t *testing.T) {
	b := bufFromLines("one", "two", "three")
	if got := b.serialise("", true); got != "one\ntwo\nthree\n" {
		t.Fatalf("serialise = %q", got)
	}
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := b.Save(FileInfo{Path: path, TrailingNL: false}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one\ntwo\nthree" {
		t.Fatalf("saved %q", data)
	}
}

// Review finding 5: a new file (Perm == 0) must be created with 0644.
func TestSaveAsNewFilePermissions(t *testing.T) {
	b := bufFromLines("x")
	path := filepath.Join(t.TempDir(), "new.txt")
	if err := b.SaveAs(path, FileInfo{}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0644 {
		t.Fatalf("perm = %o, want 644", st.Mode().Perm())
	}
}
