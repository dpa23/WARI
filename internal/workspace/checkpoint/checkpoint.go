package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Store manages file checkpoints per agent and turn under <root>/.wari/checkpoints/.
type Store struct {
	mu      sync.RWMutex
	root    string
	baseDir string
}

type fileRecord struct {
	Existed bool        `json:"existed"`
	Mode    os.FileMode `json:"mode,omitempty"`
}

type manifest struct {
	Files map[string]fileRecord `json:"files"`
}

// Open initializes a checkpoint Store rooted at root.
// Checkpoints are stored in <root>/.wari/checkpoints/.
func Open(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("checkpoint: root cannot be empty")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: resolve root %q: %w", root, err)
	}

	if realRoot, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = realRoot
	}

	fi, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: stat root %q: %w", absRoot, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("checkpoint: root %q is not a directory", absRoot)
	}

	baseDir := filepath.Join(absRoot, ".wari", "checkpoints")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("checkpoint: create checkpoints dir %q: %w", baseDir, err)
	}

	return &Store{
		root:    absRoot,
		baseDir: baseDir,
	}, nil
}

// Root returns the root directory managed by this store.
func (s *Store) Root() string {
	return s.root
}

// Save copies the current state of the file at relPath (or records that it did not exist)
// into <root>/.wari/checkpoints/<agent>/<turn>/.
// It only saves the file the first time per (agent, turn, relPath); subsequent calls
// for the same file in the same turn do not overwrite the initial copy.
// Absolute paths, paths that escape root (..), and symlinks are rejected.
func (s *Store) Save(agent string, turn int, relPath string) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	if err := validateTurn(turn); err != nil {
		return err
	}

	cleanRel, err := s.validateAndCleanRelPath(relPath)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	turnDir := s.turnDir(agent, turn)
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		return fmt.Errorf("checkpoint: create turn dir %q: %w", turnDir, err)
	}

	m, err := s.loadManifest(agent, turn)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		m = &manifest{Files: make(map[string]fileRecord)}
	}

	if _, alreadySaved := m.Files[cleanRel]; alreadySaved {
		return nil
	}

	targetPath := filepath.Join(s.root, filepath.FromSlash(cleanRel))
	fi, err := os.Lstat(targetPath)
	if err == nil {
		data, err := os.ReadFile(targetPath)
		if err != nil {
			return fmt.Errorf("checkpoint: read file %q: %w", targetPath, err)
		}

		destPath := filepath.Join(s.turnFilesDir(agent, turn), filepath.FromSlash(cleanRel))
		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("checkpoint: create checkpoint file dir %q: %w", filepath.Dir(destPath), err)
		}

		perm := fi.Mode().Perm()
		if perm == 0 {
			perm = 0o644
		}
		if err := os.WriteFile(destPath, data, perm); err != nil {
			return fmt.Errorf("checkpoint: write checkpoint file %q: %w", destPath, err)
		}

		m.Files[cleanRel] = fileRecord{
			Existed: true,
			Mode:    fi.Mode(),
		}
	} else if errors.Is(err, os.ErrNotExist) {
		m.Files[cleanRel] = fileRecord{
			Existed: false,
		}
	} else {
		return fmt.Errorf("checkpoint: stat %q: %w", targetPath, err)
	}

	return s.saveManifest(agent, turn, m)
}

// RevertFile restores the file at relPath to the state recorded in <agent>/<turn>.
// If the file did not exist at checkpoint time, it is deleted from the workspace.
// Restoration uses an atomic write.
func (s *Store) RevertFile(agent string, turn int, relPath string) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	if err := validateTurn(turn); err != nil {
		return err
	}

	cleanRel, err := s.validateAndCleanRelPath(relPath)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.revertFileLocked(agent, turn, cleanRel)
}

func (s *Store) revertFileLocked(agent string, turn int, cleanRel string) error {
	m, err := s.loadManifest(agent, turn)
	if err != nil {
		return err
	}

	rec, ok := m.Files[cleanRel]
	if !ok {
		return fmt.Errorf("checkpoint: file %q not found in checkpoint for agent %q turn %d", cleanRel, agent, turn)
	}

	targetPath := filepath.Join(s.root, filepath.FromSlash(cleanRel))
	if !rec.Existed {
		if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checkpoint: remove %q: %w", targetPath, err)
		}
		return nil
	}

	srcPath := filepath.Join(s.turnFilesDir(agent, turn), filepath.FromSlash(cleanRel))
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("checkpoint: read checkpoint copy %q: %w", srcPath, err)
	}

	return s.atomicWriteFile(targetPath, data, rec.Mode)
}

// RevertTurn restores all files checkpointed for the given agent and turn.
func (s *Store) RevertTurn(agent string, turn int) error {
	if err := validateAgent(agent); err != nil {
		return err
	}
	if err := validateTurn(turn); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.revertTurnLocked(agent, turn)
}

func (s *Store) revertTurnLocked(agent string, turn int) error {
	m, err := s.loadManifest(agent, turn)
	if err != nil {
		return err
	}

	files := make([]string, 0, len(m.Files))
	for f := range m.Files {
		files = append(files, f)
	}
	sort.Strings(files)

	for _, f := range files {
		if err := s.revertFileLocked(agent, turn, f); err != nil {
			return err
		}
	}
	return nil
}

// RevertAgent reverts all turns recorded for agent, from the newest turn to the oldest.
func (s *Store) RevertAgent(agent string) error {
	if err := validateAgent(agent); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	turns, err := s.agentTurnsLocked(agent)
	if err != nil {
		return err
	}

	for _, turn := range turns {
		if err := s.revertTurnLocked(agent, turn); err != nil {
			return fmt.Errorf("checkpoint: revert turn %d: %w", turn, err)
		}
	}
	return nil
}

// Files returns the list of relative file paths tracked in the given agent and turn, sorted alphabetically.
func (s *Store) Files(agent string, turn int) ([]string, error) {
	if err := validateAgent(agent); err != nil {
		return nil, err
	}
	if err := validateTurn(turn); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	m, err := s.loadManifest(agent, turn)
	if err != nil {
		return nil, err
	}

	files := make([]string, 0, len(m.Files))
	for f := range m.Files {
		files = append(files, f)
	}
	sort.Strings(files)
	return files, nil
}

// Diff returns the checkpointed content (old) and current workspace content (current)
// for the given file in the given agent turn.
// If the file did not exist in the checkpoint, old is nil.
// If the file does not currently exist in the workspace, current is nil.
func (s *Store) Diff(agent string, turn int, relPath string) (old, current []byte, err error) {
	if err := validateAgent(agent); err != nil {
		return nil, nil, err
	}
	if err := validateTurn(turn); err != nil {
		return nil, nil, err
	}

	cleanRel, err := s.validateAndCleanRelPath(relPath)
	if err != nil {
		return nil, nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	m, err := s.loadManifest(agent, turn)
	if err != nil {
		return nil, nil, err
	}

	rec, ok := m.Files[cleanRel]
	if !ok {
		return nil, nil, fmt.Errorf("checkpoint: file %q not found in checkpoint for agent %q turn %d", cleanRel, agent, turn)
	}

	if rec.Existed {
		srcPath := filepath.Join(s.turnFilesDir(agent, turn), filepath.FromSlash(cleanRel))
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, nil, fmt.Errorf("checkpoint: read checkpoint file %q: %w", srcPath, err)
		}
		old = data
	}

	targetPath := filepath.Join(s.root, filepath.FromSlash(cleanRel))
	curData, err := os.ReadFile(targetPath)
	if err == nil {
		current = curData
	} else if errors.Is(err, os.ErrNotExist) {
		current = nil
	} else {
		return nil, nil, fmt.Errorf("checkpoint: read workspace file %q: %w", targetPath, err)
	}

	return old, current, nil
}

// SHA256 calculates the hexadecimal SHA-256 hash of the file at path.
func SHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checkpoint: sha256 open %q: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("checkpoint: sha256 read %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) turnDir(agent string, turn int) string {
	return filepath.Join(s.baseDir, agent, strconv.Itoa(turn))
}

func (s *Store) turnFilesDir(agent string, turn int) string {
	return filepath.Join(s.turnDir(agent, turn), "files")
}

func (s *Store) manifestPath(agent string, turn int) string {
	return filepath.Join(s.turnDir(agent, turn), "manifest.json")
}

func (s *Store) loadManifest(agent string, turn int) (*manifest, error) {
	path := s.manifestPath(agent, turn)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("checkpoint: turn %d not found for agent %q: %w", turn, agent, os.ErrNotExist)
		}
		return nil, fmt.Errorf("checkpoint: read manifest %q: %w", path, err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("checkpoint: parse manifest %q: %w", path, err)
	}
	if m.Files == nil {
		m.Files = make(map[string]fileRecord)
	}
	return &m, nil
}

func (s *Store) saveManifest(agent string, turn int, m *manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: marshal manifest: %w", err)
	}
	return s.atomicWriteFile(s.manifestPath(agent, turn), data, 0o644)
}

func (s *Store) agentTurnsLocked(agent string) ([]int, error) {
	agentDir := filepath.Join(s.baseDir, agent)
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint: read agent dir %q: %w", agentDir, err)
	}

	var turns []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t, err := strconv.Atoi(entry.Name())
		if err != nil || t < 0 {
			continue
		}
		turns = append(turns, t)
	}

	sort.Slice(turns, func(i, j int) bool {
		return turns[i] > turns[j]
	})

	return turns, nil
}

func (s *Store) atomicWriteFile(targetPath string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("checkpoint: mkdir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".wari-atomic-*")
	if err != nil {
		return fmt.Errorf("checkpoint: create temp in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("checkpoint: write temp %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("checkpoint: sync temp %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("checkpoint: close temp %q: %w", tmpName, err)
	}

	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("checkpoint: chmod temp %q: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("checkpoint: rename temp %q to %q: %w", tmpName, targetPath, err)
	}
	success = true
	return nil
}

func (s *Store) validateAndCleanRelPath(relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("checkpoint: path cannot be empty")
	}

	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, `\`) {
		return "", fmt.Errorf("checkpoint: absolute paths not allowed: %q", relPath)
	}

	if len(relPath) >= 2 && relPath[1] == ':' {
		return "", fmt.Errorf("checkpoint: absolute paths not allowed: %q", relPath)
	}

	if strings.Contains(relPath, "..") {
		return "", fmt.Errorf("checkpoint: path %q containing '..' is not allowed", relPath)
	}

	clean := filepath.Clean(relPath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("checkpoint: path %q escapes root", relPath)
	}

	cleanSlash := filepath.ToSlash(clean)
	if cleanSlash == ".wari" || strings.HasPrefix(cleanSlash, ".wari/") {
		return "", fmt.Errorf("checkpoint: path %q inside .wari is not allowed", relPath)
	}

	target := filepath.Join(s.root, filepath.FromSlash(cleanSlash))
	rel, err := filepath.Rel(s.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("checkpoint: path %q escapes root", relPath)
	}

	parts := strings.Split(cleanSlash, "/")
	curr := s.root
	for i, part := range parts {
		curr = filepath.Join(curr, part)
		fi, err := os.Lstat(curr)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			return "", fmt.Errorf("checkpoint: stat %q: %w", curr, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("checkpoint: symlinks are not allowed: %q", relPath)
		}
		if i == len(parts)-1 && fi.IsDir() {
			return "", fmt.Errorf("checkpoint: %q is a directory, expected file", relPath)
		}
	}

	return cleanSlash, nil
}

func validateAgent(agent string) error {
	if agent == "" {
		return fmt.Errorf("checkpoint: agent cannot be empty")
	}
	if strings.ContainsAny(agent, `/\`) || agent == ".." || strings.Contains(agent, "..") {
		return fmt.Errorf("checkpoint: invalid agent name %q", agent)
	}
	return nil
}

func validateTurn(turn int) error {
	if turn < 0 {
		return fmt.Errorf("checkpoint: turn must be non-negative, got %d", turn)
	}
	return nil
}
