package editor

import "testing"

func TestReplaceRangeSingleLine(t *testing.T) {
	b := bufFromLines("hello world")
	b.ReplaceRange(Pos{0, 6}, Pos{0, 11}, "there")
	mustLines(t, b, "hello there")
	mustCursor(t, b, 0, 11)
	if !selIsEmpty(b) {
		t.Error("no debe quedar selección")
	}
}

func TestReplaceRangeMultiLineBothWays(t *testing.T) {
	b := bufFromLines("ab", "cd", "ef")
	// Los extremos pueden venir en cualquier orden.
	b.ReplaceRange(Pos{2, 1}, Pos{0, 1}, "X\nY")
	mustLines(t, b, "aX", "Yf")
	mustCursor(t, b, 1, 1)
}

func TestReplaceRangeInsertAndDeleteOnly(t *testing.T) {
	b := bufFromLines("abc")
	b.ReplaceRange(Pos{0, 1}, Pos{0, 1}, "\n  ")
	mustLines(t, b, "a", "  bc") // literal: sin sangría automática añadida
	b.ReplaceRange(Pos{0, 1}, Pos{1, 2}, "")
	mustLines(t, b, "abc")
}

func TestReplaceRangeIsOneUndoStepAndRedo(t *testing.T) {
	b := bufFromLines("one two three")
	b.InsertText("zzz") // una edición previa no se mezcla con la sustitución
	b.ReplaceRange(Pos{0, 7}, Pos{0, 10}, "2")
	mustLines(t, b, "zzzone 2 three")
	if !b.Undo() {
		t.Fatal("Undo")
	}
	mustLines(t, b, "zzzone two three")
	if !b.Redo() {
		t.Fatal("Redo")
	}
	mustLines(t, b, "zzzone 2 three")
	b.Undo()
	b.Undo()
	mustLines(t, b, "one two three")
}

func TestReplaceRangeClamps(t *testing.T) {
	b := bufFromLines("abc")
	b.ReplaceRange(Pos{0, 1}, Pos{9, 9}, "Z")
	mustLines(t, b, "aZ")
}

func TestGroupJoinsEditsInOneUndoStep(t *testing.T) {
	b := bufFromLines("a b c")
	b.BeginGroup()
	b.SetCursorPos(Pos{0, 1})
	b.InsertText("1")
	b.ReplaceRange(Pos{0, 5}, Pos{0, 6}, "C")
	b.SetCursorPos(Pos{0, 0})
	b.DeleteForward()
	b.EndGroup()
	mustLines(t, b, "1 b C")
	b.Undo()
	mustLines(t, b, "a b c")
	if b.Undo() {
		t.Error("el grupo debía ser un único paso")
	}
	b.Redo()
	mustLines(t, b, "1 b C")
}

func TestGroupNestsAndUnbalancedEndIsHarmless(t *testing.T) {
	b := bufFromLines("x")
	b.EndGroup()
	b.BeginGroup()
	b.BeginGroup()
	b.ReplaceRange(Pos{0, 0}, Pos{0, 1}, "y")
	b.EndGroup()
	b.ReplaceRange(Pos{0, 0}, Pos{0, 1}, "z")
	b.EndGroup()
	mustLines(t, b, "z")
	b.Undo()
	mustLines(t, b, "x")
}

func selIsEmpty(b *Buffer) bool {
	s := b.Selection()
	return s.Anchor == s.Active
}
