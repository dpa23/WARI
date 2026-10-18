package checkpoint

import (
	"crypto/rand"
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
//
// Every access to the workspace and to the checkpoint tree goes through an
// *os.Root, so no path (manifest key, symlink, race) can reach outside root.
type Store struct {
	mu   sync.RWMutex
	root string
	rt   *os.Root
}

// baseRel is the checkpoint directory relative to the workspace root.
var baseRel = filepath.Join(".wari", "checkpoints")

// maxMode is the widest permission set accepted from a manifest: ordinary
// rwx bits only, never setuid, setgid, sticky or file-type bits.
const maxMode os.FileMode = 0o777

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

	rt, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: open root %q: %w", absRoot, err)
	}
	if err := rt.MkdirAll(baseRel, 0o755); err != nil {
		_ = rt.Close()
		return nil, fmt.Errorf("checkpoint: create checkpoints dir %q: %w", filepath.Join(absRoot, baseRel), err)
	}

	return &Store{root: absRoot, rt: rt}, nil
}

// Root returns the root directory managed by this store.
func (s *Store) Root() string {
	return s.root
}

// Close releases the handle to the workspace root. The Store must not be used afterwards.
func (s *Store) Close() error {
	return s.rt.Close()
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
	if err := s.rt.MkdirAll(turnDir, 0o755); err != nil {
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

	targetPath := filepath.FromSlash(cleanRel)
	fi, err := s.rt.Lstat(targetPath)
	if err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("checkpoint: %q is not a regular file", cleanRel)
		}
		data, err := s.rt.ReadFile(targetPath)
		if err != nil {
			return fmt.Errorf("checkpoint: read file %q: %w", cleanRel, err)
		}

		destPath := filepath.Join(s.turnFilesDir(agent, turn), targetPath)
		perm := fi.Mode().Perm()
		if perm == 0 {
			perm = 0o644
		}
		if err := s.atomicWriteFile(destPath, data, perm); err != nil {
			return err
		}

		m.Files[cleanRel] = fileRecord{
			Existed: true,
			Mode:    perm,
		}
	} else if errors.Is(err, os.ErrNotExist) {
		m.Files[cleanRel] = fileRecord{
			Existed: false,
		}
	} else {
		return fmt.Errorf("checkpoint: stat %q: %w", cleanRel, err)
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

	m, err := s.loadManifest(agent, turn)
	if err != nil {
		return err
	}
	// The whole manifest must be sound, not only the requested entry.
	if err := s.validateManifestKeys(m); err != nil {
		return err
	}
	return s.revertFileLocked(agent, turn, m, cleanRel)
}

// revertFileLocked restores cleanRel from m. Callers must have validated m
// with validateManifestKeys.
func (s *Store) revertFileLocked(agent string, turn int, m *manifest, cleanRel string) error {
	rec, ok := m.Files[cleanRel]
	if !ok {
		return fmt.Errorf("checkpoint: file %q not found in checkpoint for agent %q turn %d", cleanRel, agent, turn)
	}

	target := filepath.FromSlash(cleanRel)
	if !rec.Existed {
		if err := s.rt.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checkpoint: remove %q: %w", cleanRel, err)
		}
		return nil
	}

	src := filepath.Join(s.turnFilesDir(agent, turn), target)
	// The copy must be a plain file: a planted symlink must not be followed
	// (even inside root) to smuggle other content into the workspace.
	fi, err := s.rt.Lstat(src)
	if err != nil {
		return fmt.Errorf("checkpoint: stat checkpoint copy %q: %w", src, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("checkpoint: checkpoint copy %q is not a regular file", src)
	}
	data, err := s.rt.ReadFile(src)
	if err != nil {
		return fmt.Errorf("checkpoint: read checkpoint copy %q: %w", src, err)
	}

	return s.atomicWriteFile(target, data, rec.Mode)
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

	m, err := s.loadManifest(agent, turn)
	if err != nil {
		return err
	}
	if err := s.validateManifestKeys(m); err != nil {
		return err
	}
	return s.revertManifestLocked(agent, turn, m)
}

func (s *Store) revertManifestLocked(agent string, turn int, m *manifest) error {
	for _, f := range sortedKeys(m) {
		if err := s.revertFileLocked(agent, turn, m, f); err != nil {
			return err
		}
	}
	return nil
}

// RevertAgent reverts all turns recorded for agent, from the newest turn to the oldest.
// Every manifest is validated before anything is modified.
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

	manifests := make([]*manifest, len(turns))
	for i, turn := range turns {
		m, err := s.loadManifest(agent, turn)
		if err != nil {
			return fmt.Errorf("checkpoint: revert turn %d: %w", turn, err)
		}
		if err := s.validateManifestKeys(m); err != nil {
			return fmt.Errorf("checkpoint: revert turn %d: %w", turn, err)
		}
		manifests[i] = m
	}

	for i, turn := range turns {
		if err := s.revertManifestLocked(agent, turn, manifests[i]); err != nil {
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
	return sortedKeys(m), nil
}

func sortedKeys(m *manifest) []string {
	files := make([]string, 0, len(m.Files))
	for f := range m.Files {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
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

	target := filepath.FromSlash(cleanRel)
	if rec.Existed {
		srcPath := filepath.Join(s.turnFilesDir(agent, turn), target)
		fi, err := s.rt.Lstat(srcPath)
		if err != nil {
			return nil, nil, fmt.Errorf("checkpoint: stat checkpoint file %q: %w", srcPath, err)
		}
		if !fi.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("checkpoint: checkpoint file %q is not a regular file", srcPath)
		}
		data, err := s.rt.ReadFile(srcPath)
		if err != nil {
			return nil, nil, fmt.Errorf("checkpoint: read checkpoint file %q: %w", srcPath, err)
		}
		old = data
	}

	curData, err := s.rt.ReadFile(target)
	if err == nil {
		current = curData
	} else if errors.Is(err, os.ErrNotExist) {
		current = nil
	} else {
		return nil, nil, fmt.Errorf("checkpoint: read workspace file %q: %w", cleanRel, err)
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

// The helpers below return paths relative to the workspace root; they are only
// ever passed to s.rt.

func (s *Store) turnDir(agent string, turn int) string {
	return filepath.Join(baseRel, agent, strconv.Itoa(turn))
}

func (s *Store) turnFilesDir(agent string, turn int) string {
	return filepath.Join(s.turnDir(agent, turn), "files")
}

func (s *Store) manifestPath(agent string, turn int) string {
	return filepath.Join(s.turnDir(agent, turn), "manifest.json")
}

// loadManifest reads and sanity-checks the manifest of agent/turn. The manifest
// lives inside the workspace and may be forged, so it is untrusted input: every
// key must already be in canonical form and every mode must be a plain
// permission set. A single bad entry rejects the whole manifest.
func (s *Store) loadManifest(agent string, turn int) (*manifest, error) {
	path := s.manifestPath(agent, turn)
	data, err := s.rt.ReadFile(path)
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
	for key, rec := range m.Files {
		clean, err := cleanRelPath(key)
		if err != nil {
			return nil, fmt.Errorf("checkpoint: manifest %q: invalid key: %w", path, err)
		}
		if clean != key {
			return nil, fmt.Errorf("checkpoint: manifest %q: key %q is not canonical (%q)", path, key, clean)
		}
		if rec.Mode&^maxMode != 0 {
			return nil, fmt.Errorf("checkpoint: manifest %q: key %q has invalid mode %#o", path, key, uint32(rec.Mode))
		}
	}
	return &m, nil
}

// validateManifestKeys applies the full path validation (including the
// symlink walk against the current workspace) to every key of m.
func (s *Store) validateManifestKeys(m *manifest) error {
	for key := range m.Files {
		clean, err := s.validateAndCleanRelPath(key)
		if err != nil {
			return fmt.Errorf("checkpoint: manifest key rejected: %w", err)
		}
		if clean != key {
			return fmt.Errorf("checkpoint: manifest key %q is not canonical (%q)", key, clean)
		}
	}
	return nil
}

func (s *Store) saveManifest(agent string, turn int, m *manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("checkpoint: marshal manifest: %w", err)
	}
	return s.atomicWriteFile(s.manifestPath(agent, turn), data, 0o644)
}

func (s *Store) agentTurnsLocked(agent string) ([]int, error) {
	agentDir := filepath.Join(baseRel, agent)
	d, err := s.rt.Open(agentDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint: read agent dir %q: %w", agentDir, err)
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
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

// atomicWriteFile writes data to target (relative to the root) through a
// temporary file in the same directory followed by a rename, all inside s.rt.
func (s *Store) atomicWriteFile(target string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(target)
	if dir != "." {
		if err := s.rt.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("checkpoint: mkdir %q: %w", dir, err)
		}
	}

	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}

	var (
		tmp     *os.File
		tmpName string
	)
	for range 16 {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return fmt.Errorf("checkpoint: random temp name: %w", err)
		}
		tmpName = filepath.Join(dir, ".wari-atomic-"+hex.EncodeToString(rnd[:]))
		f, err := s.rt.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			tmp = f
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("checkpoint: create temp in %q: %w", dir, err)
		}
	}
	if tmp == nil {
		return fmt.Errorf("checkpoint: create temp in %q: too many name collisions", dir)
	}

	success := false
	defer func() {
		if !success {
			_ = s.rt.Remove(tmpName)
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
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("checkpoint: chmod temp %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("checkpoint: close temp %q: %w", tmpName, err)
	}

	if err := s.rt.Rename(tmpName, target); err != nil {
		return fmt.Errorf("checkpoint: rename temp %q to %q: %w", tmpName, target, err)
	}
	success = true
	return nil
}

// cleanRelPath is the purely syntactic part of path validation: it returns the
// cleaned, slash-separated relative path or an error if relPath is empty,
// absolute, contains "..", or points into .wari.
func cleanRelPath(relPath string) (string, error) {
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
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("checkpoint: path %q escapes root", relPath)
	}
	return cleanSlash, nil
}

// validateAndCleanRelPath validates relPath syntactically (cleanRelPath) and
// then walks it inside the root rejecting symlinks and directories. The walk is
// defense in depth; containment itself is guaranteed by s.rt.
func (s *Store) validateAndCleanRelPath(relPath string) (string, error) {
	cleanSlash, err := cleanRelPath(relPath)
	if err != nil {
		return "", err
	}

	parts := strings.Split(cleanSlash, "/")
	curr := ""
	for i, part := range parts {
		curr = filepath.Join(curr, part)
		fi, err := s.rt.Lstat(curr)
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
