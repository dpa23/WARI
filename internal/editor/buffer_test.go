package editor

import (
	"regexp"
	"strings"
	"testing"
)

// ---- helpers ---------------------------------------------------------------

func bufFromLines(lines ...string) *Buffer {
	b := New()
	b.lines = make([][]rune, len(lines))
	for i, l := range lines {
		b.lines[i] = []rune(l)
	}
	b.cursor = Pos{}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	return b
}

func lines(b *Buffer) []string {
	out := make([]string, b.LineCount())
	for i := 0; i < b.LineCount(); i++ {
		out[i] = string(b.Line(i))
	}
	return out
}

func mustLines(t *testing.T, b *Buffer, want ...string) {
	t.Helper()
	got := lines(b)
	if len(got) != len(want) {
		t.Fatalf("line count: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func mustCursor(t *testing.T, b *Buffer, line, col int) {
	t.Helper()
	c := b.Cursor()
	if c.Line != line || c.Col != col {
		t.Fatalf("cursor: got {%d,%d}, want {%d,%d}", c.Line, c.Col, line, col)
	}
}

// ---- Buffer basics ---------------------------------------------------------

func TestNew(t *testing.T) {
	b := New()
	mustLines(t, b, "")
	mustCursor(t, b, 0, 0)
}

// ---- InsertRune ------------------------------------------------------------

func TestInsertRuneSingleLine(t *testing.T) {
	b := New()
	b.InsertRune('H')
	b.InsertRune('i')
	mustLines(t, b, "Hi")
	mustCursor(t, b, 0, 2)
}

func TestInsertRuneNewlineAutoIndent(t *testing.T) {
	b := bufFromLines("\t\thello")
	b.cursor = Pos{0, 7} // end of "\t\thello"
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.InsertRune('\n')
	mustLines(t, b, "\t\thello", "\t\t")
	mustCursor(t, b, 1, 2)
}

func TestInsertRuneNewlinePreservesSpaceIndent(t *testing.T) {
	b := bufFromLines("    world")
	b.cursor = Pos{0, 9}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.InsertRune('\n')
	mustLines(t, b, "    world", "    ")
	mustCursor(t, b, 1, 4)
}

func TestInsertRuneNewlineNoIndentOnEmptyLine(t *testing.T) {
	b := bufFromLines("foo")
	b.cursor = Pos{0, 3}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.InsertRune('\n')
	mustLines(t, b, "foo", "")
}

func TestInsertTextMultiline(t *testing.T) {
	b := New()
	b.InsertText("line1\nline2\nline3")
	mustLines(t, b, "line1", "line2", "line3")
	mustCursor(t, b, 2, 5)
}

func TestInsertTextCRLF(t *testing.T) {
	b := New()
	b.InsertText("a\r\nb\r\nc")
	mustLines(t, b, "a", "b", "c")
}

// ---- Deletion --------------------------------------------------------------

func TestDeleteBackward(t *testing.T) {
	b := bufFromLines("hello")
	b.cursor = Pos{0, 5}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.DeleteBackward()
	mustLines(t, b, "hell")
	mustCursor(t, b, 0, 4)
}

func TestDeleteBackwardMergesLines(t *testing.T) {
	b := bufFromLines("foo", "bar")
	b.cursor = Pos{1, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.DeleteBackward()
	mustLines(t, b, "foobar")
	mustCursor(t, b, 0, 3)
}

func TestDeleteForward(t *testing.T) {
	b := bufFromLines("hello")
	b.cursor = Pos{0, 1}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.DeleteForward()
	mustLines(t, b, "hllo")
	mustCursor(t, b, 0, 1)
}

func TestDeleteForwardMergesLines(t *testing.T) {
	b := bufFromLines("foo", "bar")
	b.cursor = Pos{0, 3}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.DeleteForward()
	mustLines(t, b, "foobar")
	mustCursor(t, b, 0, 3)
}

func TestDeleteSelection(t *testing.T) {
	b := bufFromLines("hello world")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: Pos{0, 6}, Active: Pos{0, 0}}
	b.DeleteSelection()
	mustLines(t, b, "world")
	mustCursor(t, b, 0, 0)
}

func TestDeleteSelectionMultiline(t *testing.T) {
	b := bufFromLines("aaa", "bbb", "ccc")
	b.sel = Selection{Anchor: Pos{0, 1}, Active: Pos{2, 2}}
	b.cursor = Pos{2, 2}
	b.DeleteSelection()
	mustLines(t, b, "ac")
	mustCursor(t, b, 0, 1)
}

// ---- Cursor movement -------------------------------------------------------

func TestMoveRightWraps(t *testing.T) {
	b := bufFromLines("ab", "cd")
	b.cursor = Pos{0, 2}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveRight(false)
	mustCursor(t, b, 1, 0)
}

func TestMoveLeftWraps(t *testing.T) {
	b := bufFromLines("ab", "cd")
	b.cursor = Pos{1, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveLeft(false)
	mustCursor(t, b, 0, 2)
}

func TestMoveUpDown(t *testing.T) {
	b := bufFromLines("abc", "de")
	b.cursor = Pos{0, 2}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveDown(false)
	mustCursor(t, b, 1, 2) // clamped to 2 (len("de"))
	b.MoveUp(false)
	mustCursor(t, b, 0, 2)
}

func TestMoveLineStartEnd(t *testing.T) {
	b := bufFromLines("hello world")
	b.cursor = Pos{0, 5}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveLineStart(false)
	mustCursor(t, b, 0, 0)
	b.MoveLineEnd(false)
	mustCursor(t, b, 0, 11)
}

func TestMoveDocStartEnd(t *testing.T) {
	b := bufFromLines("line1", "line2", "line3")
	b.cursor = Pos{1, 2}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveDocEnd(false)
	mustCursor(t, b, 2, 5)
	b.MoveDocStart(false)
	mustCursor(t, b, 0, 0)
}

func TestMoveWordRight(t *testing.T) {
	b := bufFromLines("hello world  end")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveWordRight(false)
	mustCursor(t, b, 0, 6) // "world"
	b.MoveWordRight(false)
	mustCursor(t, b, 0, 13) // "end"
}

func TestMoveWordLeft(t *testing.T) {
	b := bufFromLines("hello world")
	b.cursor = Pos{0, 11}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveWordLeft(false)
	mustCursor(t, b, 0, 6) // start of "world"
	b.MoveWordLeft(false)
	mustCursor(t, b, 0, 0) // start of "hello"
}

func TestMoveWithSelection(t *testing.T) {
	b := bufFromLines("hello")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.MoveRight(true)
	b.MoveRight(true)
	b.MoveRight(true)
	sel := b.Selection()
	if sel.Anchor != (Pos{0, 0}) {
		t.Fatalf("anchor moved: %v", sel.Anchor)
	}
	if sel.Active != (Pos{0, 3}) {
		t.Fatalf("active: got %v want {0,3}", sel.Active)
	}
	if b.SelectedText() != "hel" {
		t.Fatalf("selected: %q", b.SelectedText())
	}
}

// ---- Copy / Cut / Paste ----------------------------------------------------

func TestCopyPaste(t *testing.T) {
	b := bufFromLines("hello world")
	b.sel = Selection{Anchor: Pos{0, 0}, Active: Pos{0, 5}}
	b.cursor = Pos{0, 5}
	text := b.Copy()
	if text != "hello" {
		t.Fatalf("copy: %q", text)
	}
	// Paste at end.
	b.MoveLineEnd(false)
	b.Paste(text)
	mustLines(t, b, "hello worldhello")
}

func TestCut(t *testing.T) {
	b := bufFromLines("hello world")
	b.sel = Selection{Anchor: Pos{0, 6}, Active: Pos{0, 11}}
	b.cursor = Pos{0, 11}
	text := b.Cut()
	if text != "world" {
		t.Fatalf("cut: %q", text)
	}
	mustLines(t, b, "hello ")
}

func TestPasteReplacesSelection(t *testing.T) {
	b := bufFromLines("hello world")
	b.sel = Selection{Anchor: Pos{0, 0}, Active: Pos{0, 5}}
	b.cursor = Pos{0, 5}
	b.Paste("goodbye")
	mustLines(t, b, "goodbye world")
}

// ---- Undo / Redo -----------------------------------------------------------

func newFakeClock(start int64) (func() int64, func(delta int64)) {
	t := start
	return func() int64 { return t }, func(d int64) { t += d }
}

func TestUndoRedoInsert(t *testing.T) {
	clk, _ := newFakeClock(0)
	b := newWithClock(clk)
	b.InsertRune('a')
	b.InsertRune('b')
	b.InsertRune('c')
	// All in one group (no pause).
	b.Undo()
	mustLines(t, b, "")
	b.Redo()
	// After redo the letters should be back (may vary in grouping).
	if b.String() == "" {
		t.Fatal("redo produced empty buffer")
	}
}

func TestUndoGroupSeparatedByPause(t *testing.T) {
	clk, advance := newFakeClock(0)
	b := newWithClock(clk)
	b.SetUndoGroupPause(500_000_000) // 0.5 s
	b.InsertRune('a')
	b.InsertRune('b')
	advance(600_000_000) // > 0.5 s pause
	b.InsertRune('c')
	b.InsertRune('d')

	// First undo should remove "cd".
	b.Undo()
	mustLines(t, b, "ab")
	// Second undo removes "ab".
	b.Undo()
	mustLines(t, b, "")
}

func TestUndoDeleteBackward(t *testing.T) {
	clk, _ := newFakeClock(0)
	b := newWithClock(clk)
	b.InsertText("hello")
	b.flushPending()
	b.DeleteBackward()
	b.flushPending()
	b.Undo()
	mustLines(t, b, "hello")
}

func TestUndoDeleteForward(t *testing.T) {
	clk, _ := newFakeClock(0)
	b := newWithClock(clk)
	b.InsertText("hello")
	b.flushPending()
	b.cursor = Pos{0, 2}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.DeleteForward()
	b.flushPending()
	b.Undo()
	mustLines(t, b, "hello")
}

func TestUndoNewline(t *testing.T) {
	clk, _ := newFakeClock(0)
	b := newWithClock(clk)
	b.InsertText("hello")
	b.flushPending()
	b.InsertRune('\n')
	b.flushPending()
	b.Undo()
	mustLines(t, b, "hello")
}

func TestUndoDeleteLineMerge(t *testing.T) {
	clk, _ := newFakeClock(0)
	b := newWithClock(clk)
	b.InsertText("foo\nbar")
	b.flushPending()
	b.cursor = Pos{1, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.DeleteBackward() // merge lines
	b.flushPending()
	b.Undo()
	mustLines(t, b, "foo", "bar")
}

func TestMultipleUndoSteps(t *testing.T) {
	clk, advance := newFakeClock(0)
	b := newWithClock(clk)
	b.SetUndoGroupPause(100)

	b.InsertText("hello")
	advance(200)
	b.InsertRune('\n')
	advance(200)
	b.InsertText("world")

	// Undo each group separately.
	b.Undo()
	if !strings.HasSuffix(b.String(), "hello\n") && !strings.HasSuffix(b.String(), "hello") {
		t.Logf("after 1 undo: %q", b.String())
	}
	b.Undo()
	b.Undo()
	mustLines(t, b, "")
}

// TestInsertTextWithNewlineIsOneUndoGroup is the minimal reproduction of the
// bug where InsertText("text\n") produced TWO undo groups instead of one
// because insertRaw and splitLineRaw each called b.clock() separately. With a
// fake clock that advances 2 s per call (> undoGroupPause of 1 s), that caused
// recordOp to flush after the first op, splitting the operation in two.
func TestInsertTextWithNewlineIsOneUndoGroup(t *testing.T) {
	var tick int64
	clk := func() int64 { tick += int64(2e9); return tick } // 2 s per call
	b := newWithClock(clk)
	b.SetUndoGroupPause(int64(1e9)) // 1 s

	b.InsertText("hello\n") // must be one group: "hello" + newline
	// One Undo must restore the buffer to empty.
	if !b.Undo() {
		t.Fatal("Undo returned false")
	}
	mustLines(t, b, "")
	// A second Undo must return false (nothing left).
	if b.Undo() {
		t.Fatalf("second Undo returned true, buffer: %q", b.String())
	}
}

// ---- Search and Replace ----------------------------------------------------

func TestFindNext(t *testing.T) {
	b := bufFromLines("foo bar foo")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`foo`)
	start, end, ok := b.FindNext(re)
	if !ok {
		t.Fatal("expected match")
	}
	if start != (Pos{0, 0}) || end != (Pos{0, 3}) {
		t.Fatalf("first match: got %v-%v", start, end)
	}
	b.cursor = end
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	start2, end2, ok2 := b.FindNext(re)
	if !ok2 {
		t.Fatal("expected second match")
	}
	if start2 != (Pos{0, 8}) || end2 != (Pos{0, 11}) {
		t.Fatalf("second match: got %v-%v", start2, end2)
	}
}

func TestFindNextWraps(t *testing.T) {
	b := bufFromLines("hello foo world")
	b.cursor = Pos{0, 10} // past "foo"
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`foo`)
	start, _, ok := b.FindNext(re)
	if !ok {
		t.Fatal("expected wrap-around match")
	}
	if start.Col != 6 {
		t.Fatalf("wrapped match col: %d", start.Col)
	}
}

func TestFindNextNoMatch(t *testing.T) {
	b := bufFromLines("hello world")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`xyz`)
	_, _, ok := b.FindNext(re)
	if ok {
		t.Fatal("expected no match")
	}
}

func TestFindNextMultiline(t *testing.T) {
	b := bufFromLines("line one", "line two", "end")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`two`)
	start, end, ok := b.FindNext(re)
	if !ok {
		t.Fatal("expected match")
	}
	if start.Line != 1 || start.Col != 5 {
		t.Fatalf("match start: %v", start)
	}
	if end.Line != 1 || end.Col != 8 {
		t.Fatalf("match end: %v", end)
	}
}

func TestReplaceNext(t *testing.T) {
	b := bufFromLines("foo bar foo")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`foo`)
	ok := b.ReplaceNext(re, "baz")
	if !ok {
		t.Fatal("replace should have found a match")
	}
	mustLines(t, b, "baz bar foo")
}

func TestReplaceNextUndoable(t *testing.T) {
	b := bufFromLines("foo bar")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`foo`)
	b.ReplaceNext(re, "baz")
	b.Undo()
	mustLines(t, b, "foo bar")
}

func TestReplaceAll(t *testing.T) {
	b := bufFromLines("foo bar foo baz foo")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`foo`)
	n := b.ReplaceAll(re, "qux")
	if n != 3 {
		t.Fatalf("count: got %d want 3", n)
	}
	mustLines(t, b, "qux bar qux baz qux")
}

func TestReplaceAllUndoableAsSingleStep(t *testing.T) {
	b := bufFromLines("foo bar foo baz foo")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`foo`)
	b.ReplaceAll(re, "qux")
	b.Undo()
	mustLines(t, b, "foo bar foo baz foo")
}

func TestReplaceWithCaptureGroup(t *testing.T) {
	b := bufFromLines("hello world")
	b.cursor = Pos{0, 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	re := regexp.MustCompile(`(\w+)`)
	b.ReplaceNext(re, "[$1]")
	mustLines(t, b, "[hello] world")
}

// ---- SelectAll -------------------------------------------------------------

func TestSelectAll(t *testing.T) {
	b := bufFromLines("abc", "def")
	b.SelectAll()
	if b.SelectedText() != "abc\ndef" {
		t.Fatalf("SelectAll: %q", b.SelectedText())
	}
}

// ---- String ----------------------------------------------------------------

func TestString(t *testing.T) {
	b := bufFromLines("a", "b", "c")
	if b.String() != "a\nb\nc" {
		t.Fatalf("String: %q", b.String())
	}
}

// ---- leadingWhitespace -----------------------------------------------------

func TestLeadingWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"    hello", "    "},
		{"\t\tworld", "\t\t"},
		{"noindent", ""},
		{"", ""},
		{"   ", "   "},
	}
	for _, c := range cases {
		got := string(leadingWhitespace([]rune(c.in)))
		if got != c.want {
			t.Errorf("leadingWhitespace(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDeleteForwardAtLineEndUndo(t *testing.T) {
	b := New()
	b.InsertText("abc\ndef")
	b.SetCursorPos(Pos{Line: 0, Col: 3})
	b.DeleteForward()
	if got := b.String(); got != "abcdef" {
		t.Fatalf("tras DeleteForward = %q, quiero \"abcdef\"", got)
	}
	b.Undo()
	if got := b.String(); got != "abc\ndef" {
		t.Errorf("tras Undo = %q, quiero \"abc\\ndef\"", got)
	}
}
