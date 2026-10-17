// Package editor implements the core text editor without any TUI dependency.
// It provides a line-based buffer ([][]rune), cursor and selection management,
// undo/redo with grouped operations, UTF-8 file I/O preserving newline style,
// and regexp-based search and replace.
package editor

import (
	"regexp"
	"strings"
	"unicode"
)

// Pos is a (line, col) position in the buffer. Both are 0-based.
type Pos struct {
	Line, Col int
}

// less returns true if p is strictly before q.
func (p Pos) less(q Pos) bool {
	return p.Line < q.Line || (p.Line == q.Line && p.Col < q.Col)
}

// Selection holds the anchor and the active (moving) end of a selection.
// When anchor == cursor the selection is empty (no selection).
type Selection struct {
	Anchor Pos // fixed end
	Active Pos // moving end (equals cursor)
}

// sorted returns (start, end) such that start <= end.
func (s Selection) sorted() (Pos, Pos) {
	if s.Anchor.less(s.Active) {
		return s.Anchor, s.Active
	}
	return s.Active, s.Anchor
}

// empty returns true when there is no active selection.
func (s Selection) empty() bool {
	return s.Anchor == s.Active
}

// Buffer is the central data structure: a slice of rune slices, one per line.
// It is NOT safe for concurrent use.
type Buffer struct {
	lines [][]rune
	// cursor is always inside lines.
	cursor Pos
	// sel holds the selection; sel.Active == cursor always.
	sel Selection

	// undo/redo stacks
	undo [][]op // each element is a group of ops to replay in reverse
	redo [][]op

	// pending accumulates ops that haven't been committed to the undo stack yet.
	pending []op

	// undoGroupPause is the gap that triggers a new undo group (default 1 s in
	// nanoseconds). The clock is injected so tests can control it.
	undoGroupPause int64
	clock          func() int64 // returns now in nanoseconds
	lastEditNs     int64

	// file metadata (filled by Open, used by Save)
	fileLineEnding string // "\n" or "\r\n"
	fileTrailingNL bool
}

// New returns an empty Buffer with a single empty line.
func New() *Buffer {
	return newWithClock(monoNow)
}

func newWithClock(clock func() int64) *Buffer {
	b := &Buffer{
		lines:          [][]rune{{}},
		undoGroupPause: 1_000_000_000, // 1 s in ns
		clock:          clock,
		fileLineEnding: "\n",
	}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	return b
}

// --- Raw accessors -------------------------------------------------------

// LineCount returns the number of lines.
func (b *Buffer) LineCount() int { return len(b.lines) }

// Line returns the runes of line i (0-based). The returned slice must not be
// modified by the caller.
func (b *Buffer) Line(i int) []rune {
	if i < 0 || i >= len(b.lines) {
		return nil
	}
	return b.lines[i]
}

// Cursor returns the current cursor position.
func (b *Buffer) Cursor() Pos { return b.cursor }

// Selection returns the current selection. When empty (Anchor == Active) there
// is no active selection.
func (b *Buffer) Selection() Selection { return b.sel }

// SetUndoGroupPause sets the pause (in nanoseconds) that triggers a new undo
// group. Use 0 to make every insertion a separate group.
func (b *Buffer) SetUndoGroupPause(ns int64) { b.undoGroupPause = ns }

// --- Internal helpers ----------------------------------------------------

// clampPos clamps p to valid buffer coordinates.
func (b *Buffer) clampPos(p Pos) Pos {
	if p.Line < 0 {
		p.Line = 0
	}
	if p.Line >= len(b.lines) {
		p.Line = len(b.lines) - 1
	}
	if p.Col < 0 {
		p.Col = 0
	}
	if p.Col > len(b.lines[p.Line]) {
		p.Col = len(b.lines[p.Line])
	}
	return p
}

// setCursor moves the cursor and clears the selection.
func (b *Buffer) setCursor(p Pos) {
	b.cursor = b.clampPos(p)
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}

// extendSel moves the active end of the selection to p without clearing it.
func (b *Buffer) extendSel(p Pos) {
	b.cursor = b.clampPos(p)
	b.sel.Active = b.cursor
}

// --- Cursor movement -----------------------------------------------------

// MoveRight moves the cursor one rune to the right, wrapping to the next line.
func (b *Buffer) MoveRight(sel bool) {
	p := b.cursor
	if p.Col < len(b.lines[p.Line]) {
		p.Col++
	} else if p.Line < len(b.lines)-1 {
		p.Line++
		p.Col = 0
	}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveLeft moves the cursor one rune to the left, wrapping to the previous line.
func (b *Buffer) MoveLeft(sel bool) {
	p := b.cursor
	if p.Col > 0 {
		p.Col--
	} else if p.Line > 0 {
		p.Line--
		p.Col = len(b.lines[p.Line])
	}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveDown moves the cursor one line down, preserving the column goal.
func (b *Buffer) MoveDown(sel bool) {
	p := b.cursor
	if p.Line < len(b.lines)-1 {
		p.Line++
		if p.Col > len(b.lines[p.Line]) {
			p.Col = len(b.lines[p.Line])
		}
	}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveUp moves the cursor one line up.
func (b *Buffer) MoveUp(sel bool) {
	p := b.cursor
	if p.Line > 0 {
		p.Line--
		if p.Col > len(b.lines[p.Line]) {
			p.Col = len(b.lines[p.Line])
		}
	}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveLineStart moves to the start of the current line.
func (b *Buffer) MoveLineStart(sel bool) {
	p := Pos{Line: b.cursor.Line, Col: 0}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveLineEnd moves to the end of the current line.
func (b *Buffer) MoveLineEnd(sel bool) {
	p := Pos{Line: b.cursor.Line, Col: len(b.lines[b.cursor.Line])}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveDocStart moves to the very beginning of the buffer.
func (b *Buffer) MoveDocStart(sel bool) {
	p := Pos{}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveDocEnd moves to the very end of the buffer.
func (b *Buffer) MoveDocEnd(sel bool) {
	last := len(b.lines) - 1
	p := Pos{Line: last, Col: len(b.lines[last])}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveWordRight moves to the start of the next word.
func (b *Buffer) MoveWordRight(sel bool) {
	p := b.cursor
	line := b.lines[p.Line]

	// skip current word characters
	for p.Col < len(line) && !unicode.IsSpace(rune(line[p.Col])) {
		p.Col++
	}
	// skip whitespace
	for p.Col < len(line) && unicode.IsSpace(rune(line[p.Col])) {
		p.Col++
	}
	// if at end of line, wrap
	if p.Col >= len(line) && p.Line < len(b.lines)-1 {
		p.Line++
		p.Col = 0
	}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// MoveWordLeft moves to the start of the current/previous word.
func (b *Buffer) MoveWordLeft(sel bool) {
	p := b.cursor
	if p.Col == 0 {
		if p.Line > 0 {
			p.Line--
			p.Col = len(b.lines[p.Line])
		}
		if sel {
			b.extendSel(p)
		} else {
			b.setCursor(p)
		}
		return
	}
	line := b.lines[p.Line]
	// skip whitespace to the left
	for p.Col > 0 && unicode.IsSpace(rune(line[p.Col-1])) {
		p.Col--
	}
	// skip word chars to the left
	for p.Col > 0 && !unicode.IsSpace(rune(line[p.Col-1])) {
		p.Col--
	}
	if sel {
		b.extendSel(p)
	} else {
		b.setCursor(p)
	}
}

// SetCursorPos sets the cursor to an arbitrary position (clamped).
func (b *Buffer) SetCursorPos(p Pos) { b.setCursor(p) }

// --- Selection helpers ---------------------------------------------------

// SelectAll selects the entire buffer content.
func (b *Buffer) SelectAll() {
	b.sel.Anchor = Pos{}
	last := len(b.lines) - 1
	b.cursor = Pos{Line: last, Col: len(b.lines[last])}
	b.sel.Active = b.cursor
}

// ClearSelection collapses the selection to the cursor.
func (b *Buffer) ClearSelection() {
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}

// SelectedText returns the text covered by the current selection (empty string
// if no selection).
func (b *Buffer) SelectedText() string {
	if b.sel.empty() {
		return ""
	}
	return b.textBetween(b.sel.sorted())
}

// textBetween returns the text between two positions (start <= end).
func (b *Buffer) textBetween(start, end Pos) string {
	if start == end {
		return ""
	}
	var sb strings.Builder
	for l := start.Line; l <= end.Line; l++ {
		startCol := 0
		if l == start.Line {
			startCol = start.Col
		}
		endCol := len(b.lines[l])
		if l == end.Line {
			endCol = end.Col
		}
		sb.WriteString(string(b.lines[l][startCol:endCol]))
		if l < end.Line {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// --- Clipboard helpers (no system clipboard) -----------------------------

// Copy returns the selected text (does not modify the buffer).
func (b *Buffer) Copy() string {
	return b.SelectedText()
}

// Cut returns the selected text and deletes it from the buffer.
func (b *Buffer) Cut() string {
	text := b.SelectedText()
	if text != "" {
		b.DeleteSelection()
	}
	return text
}

// Paste inserts text at the cursor, replacing any active selection.
func (b *Buffer) Paste(text string) {
	if !b.sel.empty() {
		b.DeleteSelection()
	}
	now := b.clock()
	b.insertText(text, now)
}

// --- Insertion -----------------------------------------------------------

// InsertRune inserts a single rune at the current cursor position, handling
// '\n' as a line break with automatic indentation.
func (b *Buffer) InsertRune(r rune) {
	if !b.sel.empty() {
		b.DeleteSelection()
	}
	now := b.clock()
	if r == '\n' {
		b.insertNewline(now)
	} else {
		b.insertRaw([]rune{r}, now)
	}
}

// InsertText inserts a string at the current cursor position. Newlines in the
// string are treated as line breaks; only the first line gets auto-indent (the
// caller controls pasting behaviour).
func (b *Buffer) InsertText(text string) {
	if !b.sel.empty() {
		b.DeleteSelection()
	}
	now := b.clock()
	b.insertText(text, now)
}

// insertText is the internal version (no selection handling).
func (b *Buffer) insertText(text string, now int64) {
	if text == "" {
		return
	}
	// Split on newlines and insert line by line.
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		// Strip CR so we handle CRLF text uniformly.
		part = strings.TrimSuffix(part, "\r")
		b.insertRaw([]rune(part), now)
		if i < len(parts)-1 {
			// Insert line break without auto-indent (paste mode).
			b.splitLineRaw(now)
		}
	}
}

// insertNewline splits the current line at the cursor and copies the
// leading whitespace of the current line to the new line (auto-indent).
func (b *Buffer) insertNewline(now int64) {
	indent := leadingWhitespace(b.lines[b.cursor.Line])
	b.splitLineRaw(now)
	// Insert the indent at the beginning of the new line.
	if len(indent) > 0 {
		b.insertRaw(indent, now)
	}
}

// splitLineRaw splits the current line at the cursor without any indent logic.
// now must be the timestamp captured by the public operation.
func (b *Buffer) splitLineRaw(now int64) {
	b.recordOp(b.applySplit(), now)
}

// applySplit splits the current line at the cursor without recording anything
// and returns the op that describes the edit.
func (b *Buffer) applySplit() opInsertNewline {
	line := b.lines[b.cursor.Line]
	before := cloneRunes(line[:b.cursor.Col])
	after := cloneRunes(line[b.cursor.Col:])
	b.lines[b.cursor.Line] = before

	newLine := b.cursor.Line + 1
	b.lines = append(b.lines, nil)
	copy(b.lines[newLine+1:], b.lines[newLine:])
	b.lines[newLine] = after

	p := Pos{Line: newLine, Col: 0}
	inv := &opDeleteRange{start: b.cursor, end: p, text: ""}
	b.cursor = p
	b.sel = Selection{Anchor: p, Active: p}
	return opInsertNewline{pos: Pos{Line: newLine - 1, Col: len(before)}, inverse: inv}
}

// insertRaw inserts runes on the current line at the current column.
// now must be the timestamp captured by the public operation.
func (b *Buffer) insertRaw(rs []rune, now int64) {
	if len(rs) == 0 {
		return
	}
	b.recordOp(b.applyInsert(rs), now)
}

// applyInsert inserts runes at the cursor without recording anything and
// returns the op that describes the edit.
func (b *Buffer) applyInsert(rs []rune) opInsert {
	line := b.lines[b.cursor.Line]
	newLine := make([]rune, 0, len(line)+len(rs))
	newLine = append(newLine, line[:b.cursor.Col]...)
	newLine = append(newLine, rs...)
	newLine = append(newLine, line[b.cursor.Col:]...)
	b.lines[b.cursor.Line] = newLine
	start := b.cursor
	b.cursor.Col += len(rs)
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	return opInsert{start: start, runes: cloneRunes(rs)}
}

// --- Deletion ------------------------------------------------------------

// DeleteBackward deletes the character to the left of the cursor (backspace).
// If there is a selection, it deletes the selection instead.
func (b *Buffer) DeleteBackward() {
	if !b.sel.empty() {
		b.DeleteSelection()
		return
	}
	now := b.clock()
	p := b.cursor
	if p.Col > 0 {
		// Delete one rune to the left.
		line := b.lines[p.Line]
		deleted := cloneRunes(line[p.Col-1 : p.Col])
		b.lines[p.Line] = append(cloneRunes(line[:p.Col-1]), line[p.Col:]...)
		b.cursor.Col--
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		b.recordOp(opDeleteBack{at: b.cursor, runes: deleted}, now)
	} else if p.Line > 0 {
		// Merge with previous line.
		prev := p.Line - 1
		prevLen := len(b.lines[prev])
		joined := append(cloneRunes(b.lines[prev]), b.lines[p.Line]...)
		b.lines[prev] = joined
		b.lines = append(b.lines[:p.Line], b.lines[p.Line+1:]...)
		b.cursor = Pos{Line: prev, Col: prevLen}
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		b.recordOp(opMergeUp{line: p.Line, col: prevLen}, now)
	}
}

// DeleteForward deletes the character to the right of the cursor (delete key).
// If there is a selection, it deletes the selection instead.
func (b *Buffer) DeleteForward() {
	if !b.sel.empty() {
		b.DeleteSelection()
		return
	}
	now := b.clock()
	p := b.cursor
	line := b.lines[p.Line]
	if p.Col < len(line) {
		deleted := cloneRunes(line[p.Col : p.Col+1])
		b.lines[p.Line] = append(cloneRunes(line[:p.Col]), line[p.Col+1:]...)
		b.recordOp(opDeleteFwd{at: p, runes: deleted}, now)
	} else if p.Line < len(b.lines)-1 {
		// Merge next line into current.
		joined := append(cloneRunes(b.lines[p.Line]), b.lines[p.Line+1]...)
		b.lines[p.Line] = joined
		b.lines = append(b.lines[:p.Line+1], b.lines[p.Line+2:]...)
		b.recordOp(opMergeDown{line: p.Line, col: p.Col}, now)
	}
}

// DeleteSelection deletes the currently selected region.
func (b *Buffer) DeleteSelection() {
	if b.sel.empty() {
		return
	}
	now := b.clock()
	start, end := b.sel.sorted()
	text := b.textBetween(start, end)
	b.deleteRange(start, end)
	b.cursor = start
	b.sel = Selection{Anchor: start, Active: start}
	b.recordOp(opDeleteRange{start: start, end: end, text: text}, now)
}

// deleteRange removes characters from start to end without recording an op.
func (b *Buffer) deleteRange(start, end Pos) {
	if start == end {
		return
	}
	if start.Line == end.Line {
		line := b.lines[start.Line]
		b.lines[start.Line] = append(cloneRunes(line[:start.Col]), line[end.Col:]...)
		return
	}
	// Multi-line: keep prefix of start line and suffix of end line.
	before := cloneRunes(b.lines[start.Line][:start.Col])
	after := cloneRunes(b.lines[end.Line][end.Col:])
	b.lines[start.Line] = append(before, after...)
	// Remove intermediate + end lines.
	b.lines = append(b.lines[:start.Line+1], b.lines[end.Line+1:]...)
}

// --- Undo / redo ---------------------------------------------------------

// op is a single reversible edit. Each concrete type knows how to undo itself.
type op interface {
	undo(b *Buffer)
	// isInsert returns true for typing operations (used for grouping).
	isInsert() bool
}

// opInsert: typed characters on a single line.
type opInsert struct {
	start Pos
	runes []rune
}

func (o opInsert) undo(b *Buffer) {
	line := b.lines[o.start.Line]
	b.lines[o.start.Line] = append(cloneRunes(line[:o.start.Col]), line[o.start.Col+len(o.runes):]...)
	b.cursor = o.start
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opInsert) isInsert() bool { return true }

// opInsertNewline: splitting a line.
type opInsertNewline struct {
	pos     Pos // position where the Enter was pressed
	inverse *opDeleteRange
}

func (o opInsertNewline) undo(b *Buffer) {
	// Re-join the two lines.
	line := o.pos.Line
	joined := append(cloneRunes(b.lines[line]), b.lines[line+1]...)
	b.lines[line] = joined
	b.lines = append(b.lines[:line+1], b.lines[line+2:]...)
	b.cursor = o.pos
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opInsertNewline) isInsert() bool { return true }

// opDeleteBack: backspace of one rune (not newline).
type opDeleteBack struct {
	at    Pos
	runes []rune
}

func (o opDeleteBack) undo(b *Buffer) {
	line := b.lines[o.at.Line]
	newLine := make([]rune, 0, len(line)+len(o.runes))
	newLine = append(newLine, line[:o.at.Col]...)
	newLine = append(newLine, o.runes...)
	newLine = append(newLine, line[o.at.Col:]...)
	b.lines[o.at.Line] = newLine
	b.cursor = Pos{Line: o.at.Line, Col: o.at.Col + len(o.runes)}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opDeleteBack) isInsert() bool { return false }

// opMergeUp: backspace at column 0 (join with previous line).
type opMergeUp struct {
	line int // the line that was removed
	col  int // column where the join happened (= length of previous line)
}

func (o opMergeUp) undo(b *Buffer) {
	// Split line o.line-1 at o.col.
	prev := o.line - 1
	line := b.lines[prev]
	before := cloneRunes(line[:o.col])
	after := cloneRunes(line[o.col:])
	b.lines[prev] = before
	b.lines = append(b.lines, nil)
	copy(b.lines[o.line+1:], b.lines[o.line:])
	b.lines[o.line] = after
	b.cursor = Pos{Line: o.line, Col: 0}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opMergeUp) isInsert() bool { return false }

// opDeleteFwd: Delete key on one rune.
type opDeleteFwd struct {
	at    Pos
	runes []rune
}

func (o opDeleteFwd) undo(b *Buffer) {
	line := b.lines[o.at.Line]
	newLine := make([]rune, 0, len(line)+len(o.runes))
	newLine = append(newLine, line[:o.at.Col]...)
	newLine = append(newLine, o.runes...)
	newLine = append(newLine, line[o.at.Col:]...)
	b.lines[o.at.Line] = newLine
	b.cursor = o.at
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opDeleteFwd) isInsert() bool { return false }

// opMergeDown: Delete key at end of line (join next line).
type opMergeDown struct {
	line int
	col  int // length of line before join
}

func (o opMergeDown) undo(b *Buffer) {
	line := b.lines[o.line]
	before := cloneRunes(line[:o.col])
	after := cloneRunes(line[o.col:])
	b.lines[o.line] = before
	b.lines = append(b.lines, nil)
	copy(b.lines[o.line+2:], b.lines[o.line+1:])
	b.lines[o.line+1] = after
	b.cursor = Pos{Line: o.line, Col: o.col}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opMergeDown) isInsert() bool { return false }

// opDeleteRange: deleting a selection or search-replace.
type opDeleteRange struct {
	start Pos
	end   Pos
	text  string
}

func (o opDeleteRange) undo(b *Buffer) {
	b.cursor = o.start
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.insertTextRaw(o.text)
	b.cursor = o.end
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opDeleteRange) isInsert() bool { return false }

// opInsertAt: insert text at a position (used by search-replace undo).
type opInsertAt struct {
	at   Pos
	text string
	end  Pos
}

func (o opInsertAt) undo(b *Buffer) {
	b.cursor = o.at
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	b.deleteRange(o.at, o.end)
	b.cursor = o.at
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
}
func (o opInsertAt) isInsert() bool { return false }

// opCompound bundles a delete+insert used by search-replace so the pair undoes
// and redoes as a single atomic step.
type opCompound struct {
	del opDeleteRange
	ins opInsertAt
}

func (o opCompound) undo(b *Buffer) {
	// Undo insert first, then undo delete.
	o.ins.undo(b)
	o.del.undo(b)
}
func (o opCompound) isInsert() bool { return false }

// insertTextRaw inserts text at the current cursor without recording any ops.
// Used by replaceRangeLow which records its own compound op.
func (b *Buffer) insertTextRaw(text string) {
	if text == "" {
		return
	}
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		part = strings.TrimSuffix(part, "\r")
		// Direct line insertion without op recording.
		rs := []rune(part)
		if len(rs) > 0 {
			line := b.lines[b.cursor.Line]
			newLine := make([]rune, 0, len(line)+len(rs))
			newLine = append(newLine, line[:b.cursor.Col]...)
			newLine = append(newLine, rs...)
			newLine = append(newLine, line[b.cursor.Col:]...)
			b.lines[b.cursor.Line] = newLine
			b.cursor.Col += len(rs)
			b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		}
		if i < len(parts)-1 {
			// Split line without op recording.
			line := b.lines[b.cursor.Line]
			before := cloneRunes(line[:b.cursor.Col])
			after := cloneRunes(line[b.cursor.Col:])
			b.lines[b.cursor.Line] = before
			newLine := b.cursor.Line + 1
			b.lines = append(b.lines, nil)
			copy(b.lines[newLine+1:], b.lines[newLine:])
			b.lines[newLine] = after
			b.cursor = Pos{Line: newLine, Col: 0}
			b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		}
	}
}

// recordOp adds an op to the pending group and decides whether to flush the
// previous group.
func (b *Buffer) recordOp(o op, now int64) {
	b.redo = b.redo[:0] // any edit discards the redo stack

	if len(b.pending) > 0 {
		// Flush to undo if: different kind, or pause exceeded.
		lastIsInsert := b.pending[len(b.pending)-1].isInsert()
		pause := now - b.lastEditNs
		if lastIsInsert != o.isInsert() || pause > b.undoGroupPause {
			b.undo = append(b.undo, b.pending)
			b.pending = nil
		}
	}
	b.pending = append(b.pending, o)
	b.lastEditNs = now
}

// flushPending moves the pending group to the undo stack (called before Undo
// and after explicit commit points like search-replace).
func (b *Buffer) flushPending() {
	if len(b.pending) > 0 {
		b.undo = append(b.undo, b.pending)
		b.pending = nil
	}
}

// Undo reverts the most recent group of operations.
func (b *Buffer) Undo() bool {
	b.flushPending()
	if len(b.undo) == 0 {
		return false
	}
	group := b.undo[len(b.undo)-1]
	b.undo = b.undo[:len(b.undo)-1]
	// Replay in reverse order.
	for i := len(group) - 1; i >= 0; i-- {
		group[i].undo(b)
	}
	b.redo = append(b.redo, group)
	return true
}

// Redo re-applies the most recently undone group.
// NOTE: Redo replays by re-applying each op's inverse-inverse, which for this
// simple implementation means re-doing the original action via insertText /
// deleteRange. A full redo stack with forward ops is complex; for now we use a
// replay approach: the redo stack holds the original ops and we re-apply them.
func (b *Buffer) Redo() bool {
	if len(b.redo) == 0 {
		return false
	}
	group := b.redo[len(b.redo)-1]
	b.redo = b.redo[:len(b.redo)-1]
	// Re-apply each op forward. Each op type knows how to re-apply itself.
	redoGroup := make([]op, 0, len(group))
	for _, o := range group {
		inv := b.redoOp(o)
		if inv != nil {
			redoGroup = append(redoGroup, inv)
		}
	}
	b.undo = append(b.undo, redoGroup)
	return true
}

// redoOp re-applies a single op and returns the new inverse op for the undo stack.
func (b *Buffer) redoOp(o op) op {
	switch v := o.(type) {
	case opInsert:
		b.cursor = v.start
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		return b.applyInsert(v.runes)
	case opInsertNewline:
		b.cursor = v.pos
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		return b.applySplit()
	case opDeleteBack:
		// Re-delete: move cursor and delete forward.
		b.cursor = Pos{Line: v.at.Line, Col: v.at.Col + len(v.runes)}
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		line := b.lines[v.at.Line]
		b.lines[v.at.Line] = append(cloneRunes(line[:v.at.Col]), line[v.at.Col+len(v.runes):]...)
		b.cursor = v.at
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		return opDeleteBack{at: v.at, runes: v.runes}
	case opMergeUp:
		// Re-merge: join line with previous.
		prev := v.line - 1
		joined := append(cloneRunes(b.lines[prev]), b.lines[v.line]...)
		b.lines[prev] = joined
		b.lines = append(b.lines[:v.line], b.lines[v.line+1:]...)
		b.cursor = Pos{Line: prev, Col: v.col}
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		return opMergeUp{line: v.line, col: v.col}
	case opDeleteFwd:
		b.cursor = v.at
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		line := b.lines[v.at.Line]
		b.lines[v.at.Line] = append(cloneRunes(line[:v.at.Col]), line[v.at.Col+len(v.runes):]...)
		return opDeleteFwd{at: v.at, runes: v.runes}
	case opMergeDown:
		line := b.lines[v.line]
		joined := append(cloneRunes(line), b.lines[v.line+1]...)
		b.lines[v.line] = joined
		b.lines = append(b.lines[:v.line+1], b.lines[v.line+2:]...)
		b.cursor = Pos{Line: v.line, Col: v.col}
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		return opMergeDown{line: v.line, col: v.col}
	case opDeleteRange:
		b.deleteRange(v.start, v.end)
		b.cursor = v.start
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		return opDeleteRange{start: v.start, end: v.end, text: v.text}
	case opInsertAt:
		b.cursor = v.at
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		b.insertTextRaw(v.text)
		return opInsertAt{at: v.at, text: v.text, end: b.cursor}
	case opCompound:
		// Re-apply: delete old range, then insert new text.
		b.deleteRange(v.del.start, v.del.end)
		b.cursor = v.del.start
		b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
		b.insertTextRaw(v.ins.text)
		return opCompound{
			del: opDeleteRange{start: v.del.start, end: v.del.end, text: v.del.text},
			ins: opInsertAt{at: v.ins.at, text: v.ins.text, end: b.cursor},
		}
	}
	return nil
}

// --- String output -------------------------------------------------------

// String returns the buffer content as a string with "\n" line endings.
func (b *Buffer) String() string {
	parts := make([]string, len(b.lines))
	for i, l := range b.lines {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

// --- Search and replace --------------------------------------------------

// FindNext finds the next match of re starting from (or after) the cursor.
// It wraps around if no match is found after the cursor. Returns ok=false if
// there are no matches at all.
func (b *Buffer) FindNext(re *regexp.Regexp) (start, end Pos, ok bool) {
	return b.findFrom(re, b.cursor, true)
}

// FindFrom finds the next match starting at fromPos. wrap=true wraps around.
func (b *Buffer) findFrom(re *regexp.Regexp, fromPos Pos, wrap bool) (start, end Pos, ok bool) {
	// Build full text with line offsets.
	text, offsets := b.buildText()
	absFrom := b.posToOffset(fromPos, offsets)

	// Search from cursor forward.
	loc := re.FindStringIndex(text[absFrom:])
	if loc != nil {
		return b.offsetToPos(absFrom+loc[0], offsets), b.offsetToPos(absFrom+loc[1], offsets), true
	}
	if !wrap || absFrom == 0 {
		return Pos{}, Pos{}, false
	}
	// Wrap around.
	loc = re.FindStringIndex(text[:absFrom])
	if loc != nil {
		return b.offsetToPos(loc[0], offsets), b.offsetToPos(loc[1], offsets), true
	}
	return Pos{}, Pos{}, false
}

// ReplaceNext replaces the next match of re (from cursor) with repl.
// The replacement is a single undoable step.
// Returns ok=false if no match found.
func (b *Buffer) ReplaceNext(re *regexp.Regexp, repl string) bool {
	start, end, ok := b.FindNext(re)
	if !ok {
		return false
	}
	b.flushPending()
	// Expand capture groups using the actual matched text.
	text, offsets := b.buildText()
	absStart := b.posToOffset(start, offsets)
	absEnd := b.posToOffset(end, offsets)
	match := text[absStart:absEnd]
	expanded := re.ReplaceAllString(match, repl)
	b.replaceRangeLow(start, end, match, expanded)
	b.flushPending()
	return true
}

// ReplaceAll replaces all matches of re with repl. The entire operation is a
// single undoable step.
func (b *Buffer) ReplaceAll(re *regexp.Regexp, repl string) int {
	b.flushPending()
	text, offsets := b.buildText()
	locs := re.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return 0
	}
	// Apply replacements from last to first to preserve offsets.
	count := 0
	for i := len(locs) - 1; i >= 0; i-- {
		loc := locs[i]
		start := b.offsetToPos(loc[0], offsets)
		end := b.offsetToPos(loc[1], offsets)
		match := text[loc[0]:loc[1]]
		replacement := re.ReplaceAllString(match, repl)
		b.replaceRangeLow(start, end, match, replacement)
		count++
	}
	b.flushPending()
	return count
}

// replaceRangeLow is the low-level workhorse for search-replace. It edits the
// buffer directly (without going through the recording insertText/deleteRange)
// and appends exactly one opCompound to pending.
func (b *Buffer) replaceRangeLow(start, end Pos, oldText, newText string) {
	now := b.clock()
	b.deleteRange(start, end)
	b.cursor = start
	b.sel = Selection{Anchor: start, Active: start}
	b.insertTextRaw(newText)
	newEnd := b.cursor
	b.pending = append(b.pending, opCompound{
		del: opDeleteRange{start: start, end: end, text: oldText},
		ins: opInsertAt{at: start, text: newText, end: newEnd},
	})
	b.lastEditNs = now
}

// buildText returns the buffer as a single string with '\n' separators and a
// slice of byte offsets: offsets[i] = byte offset of the start of line i.
func (b *Buffer) buildText() (string, []int) {
	offsets := make([]int, len(b.lines))
	var sb strings.Builder
	for i, line := range b.lines {
		offsets[i] = sb.Len()
		sb.WriteString(string(line))
		if i < len(b.lines)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String(), offsets
}

// posToOffset converts a Pos to a byte offset in the flat string.
func (b *Buffer) posToOffset(p Pos, offsets []int) int {
	p = b.clampPos(p)
	lineBytes := []byte(string(b.lines[p.Line][:p.Col]))
	return offsets[p.Line] + len(lineBytes)
}

// offsetToPos converts a byte offset to a Pos.
func (b *Buffer) offsetToPos(offset int, offsets []int) Pos {
	// Binary search for the line.
	lo, hi := 0, len(offsets)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if offsets[mid] <= offset {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	line := lo
	colBytes := offset - offsets[line]
	// Convert byte offset within line to rune count.
	lineStr := string(b.lines[line])
	col := len([]rune(lineStr[:colBytes]))
	return Pos{Line: line, Col: col}
}

// --- Helpers -------------------------------------------------------------

func cloneRunes(rs []rune) []rune {
	if rs == nil {
		return []rune{}
	}
	out := make([]rune, len(rs))
	copy(out, rs)
	return out
}

func leadingWhitespace(line []rune) []rune {
	for i, r := range line {
		if r != ' ' && r != '\t' {
			return cloneRunes(line[:i])
		}
	}
	return cloneRunes(line)
}
