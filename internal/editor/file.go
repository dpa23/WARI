package editor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileInfo holds the detected style of an opened file.
type FileInfo struct {
	Path       string
	LineEnding string // "\n" or "\r\n"
	TrailingNL bool   // whether the file ended with a newline
	Perm       os.FileMode
}

// Open reads a UTF-8 file into a new Buffer, preserving its line-ending style
// and trailing-newline status.
func Open(path string) (*Buffer, FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("editor: open %q: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("editor: stat %q: %w", path, err)
	}

	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, FileInfo{}, fmt.Errorf("editor: read %q: %w", path, err)
	}

	fi := FileInfo{
		Path: path,
		Perm: info.Mode().Perm(),
	}

	// Detect line ending by looking at the first newline.
	fi.LineEnding = "\n"
	if idx := bytes.IndexByte(raw, '\n'); idx > 0 && raw[idx-1] == '\r' {
		fi.LineEnding = "\r\n"
	}

	// Detect trailing newline.
	fi.TrailingNL = len(raw) > 0 && raw[len(raw)-1] == '\n'

	// Normalise to LF for the buffer.
	normalised := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))

	// Strip trailing newline so the last "line" is not empty.
	content := string(normalised)
	if fi.TrailingNL {
		content = strings.TrimSuffix(content, "\n")
	}

	b := New()
	b.fileLineEnding = fi.LineEnding
	b.fileTrailingNL = fi.TrailingNL

	if content == "" {
		// Empty file: buffer already has one empty line.
		return b, fi, nil
	}

	parts := strings.Split(content, "\n")
	b.lines = make([][]rune, len(parts))
	for i, p := range parts {
		b.lines[i] = []rune(p)
	}
	b.cursor = Pos{}
	b.sel = Selection{Anchor: b.cursor, Active: b.cursor}
	return b, fi, nil
}

// Save writes the buffer to fi.Path atomically:
//  1. Write to a temp file in the same directory.
//  2. fsync the temp file.
//  3. Rename the temp file over the destination.
//
// Line endings and trailing-newline style are taken from fi (i.e. as detected
// when the file was opened). Permissions are restored.
func (b *Buffer) Save(fi FileInfo) error {
	return b.SaveAs(fi.Path, fi)
}

// SaveAs is like Save but writes to a different path (keeping the same style).
func (b *Buffer) SaveAs(path string, fi FileInfo) error {
	content := b.serialise(fi.LineEnding, fi.TrailingNL)

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".wari-save-*")
	if err != nil {
		return fmt.Errorf("editor: create temp for %q: %w", path, err)
	}
	tmpName := tmp.Name()

	// Cleanup on error.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("editor: write temp for %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("editor: fsync temp for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("editor: close temp for %q: %w", path, err)
	}

	// Restore permissions; new files (Perm == 0) get 0644 instead of the 0600
	// that os.CreateTemp uses.
	perm := fi.Perm
	if perm == 0 {
		perm = 0o644
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("editor: chmod temp for %q: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("editor: rename temp for %q: %w", path, err)
	}
	success = true
	return nil
}

// serialise returns the buffer content as a byte string with the requested
// line-ending style and trailing newline.
func (b *Buffer) serialise(lineEnding string, trailingNL bool) string {
	if lineEnding == "" {
		lineEnding = "\n"
	}
	parts := make([]string, len(b.lines))
	for i, l := range b.lines {
		parts[i] = string(l)
	}
	out := strings.Join(parts, lineEnding)
	if trailingNL {
		out += lineEnding
	}
	return out
}
