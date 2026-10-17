package checkpoint_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/dpa23/wari/internal/workspace/checkpoint"
)

// TestSaveEditRevertHash verifies that saving a file, editing it, and reverting it
// leaves the SHA-256 hash identical to the original file.
func TestSaveEditRevertHash(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	relPath := "src/main.go"
	fullPath := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	originalContent := []byte("package main\n\nfunc main() {\n\tprintln(\"v1\")\n}\n")
	if err := os.WriteFile(fullPath, originalContent, 0o644); err != nil {
		t.Fatalf("writeFile failed: %v", err)
	}

	origHash, err := checkpoint.SHA256(fullPath)
	if err != nil {
		t.Fatalf("SHA256 failed: %v", err)
	}

	// 1. Save checkpoint for turn 1
	if err := store.Save("claude", 1, relPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 2. Edit file
	editedContent := []byte("package main\n\nfunc main() {\n\tprintln(\"v2 edited by agent\")\n}\n")
	if err := os.WriteFile(fullPath, editedContent, 0o644); err != nil {
		t.Fatalf("writeFile edit failed: %v", err)
	}

	editedHash, err := checkpoint.SHA256(fullPath)
	if err != nil {
		t.Fatalf("SHA256 failed: %v", err)
	}
	if editedHash == origHash {
		t.Fatal("edited hash should be different from original hash")
	}

	// Verify Diff sees old vs edited
	oldBytes, curBytes, err := store.Diff("claude", 1, relPath)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if !bytes.Equal(oldBytes, originalContent) {
		t.Fatalf("Diff old content mismatch: got %q, want %q", oldBytes, originalContent)
	}
	if !bytes.Equal(curBytes, editedContent) {
		t.Fatalf("Diff cur content mismatch: got %q, want %q", curBytes, editedContent)
	}

	// 3. Revert file
	if err := store.RevertFile("claude", 1, relPath); err != nil {
		t.Fatalf("RevertFile failed: %v", err)
	}

	revertedHash, err := checkpoint.SHA256(fullPath)
	if err != nil {
		t.Fatalf("SHA256 after revert failed: %v", err)
	}

	if revertedHash != origHash {
		t.Fatalf("hash mismatch after revert: got %s, want %s", revertedHash, origHash)
	}

	revertedContent, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("ReadFile after revert failed: %v", err)
	}
	if !bytes.Equal(revertedContent, originalContent) {
		t.Fatalf("content mismatch after revert: got %q, want %q", revertedContent, originalContent)
	}
}

// TestNewFileRevertDisappears verifies that a newly created file, once reverted, disappears.
func TestNewFileRevertDisappears(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	relPath := "new_file.txt"
	fullPath := filepath.Join(root, relPath)

	// File does not exist yet; save checkpoint tracking that it did not exist
	if err := store.Save("agy", 1, relPath); err != nil {
		t.Fatalf("Save non-existent file failed: %v", err)
	}

	// Agent creates the file
	newContent := []byte("created by agent")
	if err := os.WriteFile(fullPath, newContent, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Verify Diff shows old=nil, cur=newContent
	oldBytes, curBytes, err := store.Diff("agy", 1, relPath)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if oldBytes != nil {
		t.Fatalf("expected oldBytes to be nil for new file, got %q", oldBytes)
	}
	if !bytes.Equal(curBytes, newContent) {
		t.Fatalf("Diff cur content mismatch: got %q, want %q", curBytes, newContent)
	}

	// Revert the file: it must be deleted
	if err := store.RevertFile("agy", 1, relPath); err != nil {
		t.Fatalf("RevertFile failed: %v", err)
	}

	if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
		t.Fatalf("expected file %s to be deleted after revert, but stat returned %v", fullPath, err)
	}
}

// TestSecondSaveSameTurnDoesNotOverwrite verifies that calling Save a second time
// for the same file in the same turn does not overwrite the checkpoint copy.
func TestSecondSaveSameTurnDoesNotOverwrite(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	relPath := "notes.txt"
	fullPath := filepath.Join(root, relPath)
	firstContent := []byte("turn 1 initial content")
	if err := os.WriteFile(fullPath, firstContent, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// First save in turn 1
	if err := store.Save("claude", 1, relPath); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}

	// Agent modifies file mid-turn
	modifiedContent := []byte("turn 1 mid-turn modification")
	if err := os.WriteFile(fullPath, modifiedContent, 0o644); err != nil {
		t.Fatalf("WriteFile edit failed: %v", err)
	}

	// Second save in the same turn 1 (e.g. before another tool call in same turn)
	if err := store.Save("claude", 1, relPath); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	// Reverting turn 1 should restore firstContent, not modifiedContent
	if err := store.RevertFile("claude", 1, relPath); err != nil {
		t.Fatalf("RevertFile failed: %v", err)
	}

	restored, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(restored, firstContent) {
		t.Fatalf("second save overwrote initial checkpoint: got %q, want %q", restored, firstContent)
	}
}

// TestRevertAgentMultipleTurns verifies that RevertAgent reverts all turns
// from newest to oldest.
func TestRevertAgentMultipleTurns(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	fileA := "fileA.txt"
	fileB := "fileB.txt"
	fileC := "fileC.txt"

	fullA := filepath.Join(root, fileA)
	fullB := filepath.Join(root, fileB)
	fullC := filepath.Join(root, fileC)

	initialA := []byte("fileA v0")
	if err := os.WriteFile(fullA, initialA, 0o644); err != nil {
		t.Fatalf("write initial A failed: %v", err)
	}

	// Turn 1:
	// - touches fileA (v0 -> v1)
	// - touches fileB (does not exist -> creates v1)
	if err := store.Save("agent1", 1, fileA); err != nil {
		t.Fatalf("turn 1 save A: %v", err)
	}
	if err := store.Save("agent1", 1, fileB); err != nil {
		t.Fatalf("turn 1 save B: %v", err)
	}
	if err := os.WriteFile(fullA, []byte("fileA v1"), 0o644); err != nil {
		t.Fatalf("turn 1 write A: %v", err)
	}
	if err := os.WriteFile(fullB, []byte("fileB v1"), 0o644); err != nil {
		t.Fatalf("turn 1 write B: %v", err)
	}

	// Turn 2:
	// - touches fileA (v1 -> v2)
	// - touches fileC (does not exist -> creates v1)
	if err := store.Save("agent1", 2, fileA); err != nil {
		t.Fatalf("turn 2 save A: %v", err)
	}
	if err := store.Save("agent1", 2, fileC); err != nil {
		t.Fatalf("turn 2 save C: %v", err)
	}
	if err := os.WriteFile(fullA, []byte("fileA v2"), 0o644); err != nil {
		t.Fatalf("turn 2 write A: %v", err)
	}
	if err := os.WriteFile(fullC, []byte("fileC v1"), 0o644); err != nil {
		t.Fatalf("turn 2 write C: %v", err)
	}

	// Turn 3:
	// - touches fileA (v2 -> v3)
	// - touches fileB (v1 -> v2)
	if err := store.Save("agent1", 3, fileA); err != nil {
		t.Fatalf("turn 3 save A: %v", err)
	}
	if err := store.Save("agent1", 3, fileB); err != nil {
		t.Fatalf("turn 3 save B: %v", err)
	}
	if err := os.WriteFile(fullA, []byte("fileA v3"), 0o644); err != nil {
		t.Fatalf("turn 3 write A: %v", err)
	}
	if err := os.WriteFile(fullB, []byte("fileB v2"), 0o644); err != nil {
		t.Fatalf("turn 3 write B: %v", err)
	}

	// Check files tracked in each turn
	filesT1, err := store.Files("agent1", 1)
	if err != nil {
		t.Fatalf("Files turn 1: %v", err)
	}
	if !reflect.DeepEqual(filesT1, []string{"fileA.txt", "fileB.txt"}) {
		t.Fatalf("unexpected files turn 1: %v", filesT1)
	}

	filesT2, err := store.Files("agent1", 2)
	if err != nil {
		t.Fatalf("Files turn 2: %v", err)
	}
	if !reflect.DeepEqual(filesT2, []string{"fileA.txt", "fileC.txt"}) {
		t.Fatalf("unexpected files turn 2: %v", filesT2)
	}

	filesT3, err := store.Files("agent1", 3)
	if err != nil {
		t.Fatalf("Files turn 3: %v", err)
	}
	if !reflect.DeepEqual(filesT3, []string{"fileA.txt", "fileB.txt"}) {
		t.Fatalf("unexpected files turn 3: %v", filesT3)
	}

	// Now revert all turns of agent1
	if err := store.RevertAgent("agent1"); err != nil {
		t.Fatalf("RevertAgent failed: %v", err)
	}

	// fileA should be back to initialA (v0)
	gotA, err := os.ReadFile(fullA)
	if err != nil {
		t.Fatalf("read A after revert: %v", err)
	}
	if !bytes.Equal(gotA, initialA) {
		t.Fatalf("fileA mismatch: got %q, want %q", gotA, initialA)
	}

	// fileB should not exist
	if _, err := os.Stat(fullB); !os.IsNotExist(err) {
		t.Fatalf("fileB should have been removed, but stat was: %v", err)
	}

	// fileC should not exist
	if _, err := os.Stat(fullC); !os.IsNotExist(err) {
		t.Fatalf("fileC should have been removed, but stat was: %v", err)
	}
}

// TestRevertTurnSingle verifies reverting a specific turn restores only that turn's changes.
func TestRevertTurnSingle(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	f1 := filepath.Join(root, "1.txt")
	f2 := filepath.Join(root, "2.txt")
	_ = os.WriteFile(f1, []byte("1 original"), 0o644)
	_ = os.WriteFile(f2, []byte("2 original"), 0o644)

	if err := store.Save("c", 1, "1.txt"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("c", 1, "2.txt"); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(f1, []byte("1 modified"), 0o644)
	_ = os.WriteFile(f2, []byte("2 modified"), 0o644)

	if err := store.RevertTurn("c", 1); err != nil {
		t.Fatalf("RevertTurn failed: %v", err)
	}

	b1, _ := os.ReadFile(f1)
	b2, _ := os.ReadFile(f2)
	if string(b1) != "1 original" || string(b2) != "2 original" {
		t.Fatalf("files not restored: 1=%q, 2=%q", b1, b2)
	}
}

// TestRejectedPaths verifies that absolute paths, paths with .., and symlinks are rejected.
func TestRejectedPaths(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Create a real file inside root
	validFile := filepath.Join(root, "valid.txt")
	if err := os.WriteFile(validFile, []byte("valid"), 0o644); err != nil {
		t.Fatal(err)
	}

	invalidPaths := []string{
		"/etc/passwd",
		"/tmp/test.txt",
		"../outside.txt",
		"../../outside.txt",
		"src/../../outside.txt",
		"a/../b.txt",
		"..",
		"",
		".",
		".wari/checkpoints/sneaky",
		".wari/secret",
	}

	for _, p := range invalidPaths {
		if err := store.Save("agent", 1, p); err == nil {
			t.Errorf("Save should reject path %q, got nil error", p)
		}
		if err := store.RevertFile("agent", 1, p); err == nil {
			t.Errorf("RevertFile should reject path %q, got nil error", p)
		}
		if _, _, err := store.Diff("agent", 1, p); err == nil {
			t.Errorf("Diff should reject path %q, got nil error", p)
		}
	}

	// Test symlink rejected
	symlinkPath := filepath.Join(root, "symlink.txt")
	if err := os.Symlink(validFile, symlinkPath); err == nil {
		if err := store.Save("agent", 1, "symlink.txt"); err == nil {
			t.Error("Save should reject symlink file, got nil error")
		}
	}

	// Test symlink directory rejected
	subDir := filepath.Join(root, "realdirectory")
	_ = os.MkdirAll(subDir, 0o755)
	symlinkDir := filepath.Join(root, "symdir")
	if err := os.Symlink(subDir, symlinkDir); err == nil {
		if err := store.Save("agent", 1, "symdir/file.txt"); err == nil {
			t.Error("Save should reject path containing directory symlink, got nil error")
		}
	}
}

// TestAgentValidation verifies agent names and turn constraints.
func TestAgentValidation(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Save("", 1, "a.txt"); err == nil {
		t.Error("expected error for empty agent")
	}
	if err := store.Save("../bad", 1, "a.txt"); err == nil {
		t.Error("expected error for agent containing ..")
	}
	if err := store.Save("a/b", 1, "a.txt"); err == nil {
		t.Error("expected error for agent containing /")
	}
	if err := store.Save("agent", -1, "a.txt"); err == nil {
		t.Error("expected error for negative turn")
	}
	if err := store.RevertAgent(""); err == nil {
		t.Error("expected error for empty agent in RevertAgent")
	}
}

// TestDiffDeletedFile verifies Diff when a file was checkpointed and later deleted.
func TestDiffDeletedFile(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}

	relPath := "will_delete.txt"
	fullPath := filepath.Join(root, relPath)
	content := []byte("to be deleted")
	if err := os.WriteFile(fullPath, content, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := store.Save("agent", 1, relPath); err != nil {
		t.Fatal(err)
	}

	// Delete file in workspace
	if err := os.Remove(fullPath); err != nil {
		t.Fatal(err)
	}

	oldBytes, curBytes, err := store.Diff("agent", 1, relPath)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if !bytes.Equal(oldBytes, content) {
		t.Fatalf("old bytes mismatch: got %q, want %q", oldBytes, content)
	}
	if curBytes != nil {
		t.Fatalf("expected curBytes to be nil when file deleted, got %q", curBytes)
	}

	// Revert should bring it back
	if err := store.RevertFile("agent", 1, relPath); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, content) {
		t.Fatalf("restored mismatch: got %q, want %q", restored, content)
	}
}

// TestConcurrentAccess verifies thread-safety of Store under concurrent saves and reads.
func TestConcurrentAccess(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}

	const numFiles = 10
	for i := 0; i < numFiles; i++ {
		name := filepath.Join(root, "file_"+string(rune('a'+i))+".txt")
		_ = os.WriteFile(name, []byte("data"), 0o644)
	}

	var wg sync.WaitGroup
	for i := 0; i < numFiles; i++ {
		wg.Add(1)
		rel := "file_" + string(rune('a'+i)) + ".txt"
		go func(path string) {
			defer wg.Done()
			_ = store.Save("concurrent_agent", 1, path)
			_, _, _ = store.Diff("concurrent_agent", 1, path)
			_, _ = store.Files("concurrent_agent", 1)
		}(rel)
	}
	wg.Wait()

	files, err := store.Files("concurrent_agent", 1)
	if err != nil {
		t.Fatalf("Files failed: %v", err)
	}
	if len(files) != numFiles {
		t.Fatalf("expected %d files, got %d", numFiles, len(files))
	}
}
