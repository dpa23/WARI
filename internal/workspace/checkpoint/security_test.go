package checkpoint_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dpa23/wari/internal/workspace/checkpoint"
)

// attackEnv prepares <base>/proj (the workspace root) and <base>/outside.
type attackEnv struct {
	base  string
	root  string
	store *checkpoint.Store
}

func newAttackEnv(t *testing.T) *attackEnv {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &attackEnv{base: base, root: root, store: store}
}

func (e *attackEnv) turnDir(agent string, turn int) string {
	return filepath.Join(e.root, ".wari", "checkpoints", agent, fmt.Sprint(turn))
}

// plant fabricates a manifest (raw JSON for files) for agent/turn.
func (e *attackEnv) plant(t *testing.T, agent string, turn int, filesJSON string) {
	t.Helper()
	dir := e.turnDir(agent, turn)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"files": {%s}}`, filesJSON)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// plantCopy fabricates a checkpoint copy at files/<key> (key may contain "..").
func (e *attackEnv) plantCopy(t *testing.T, agent string, turn int, key, content string) {
	t.Helper()
	p := filepath.Join(e.turnDir(agent, turn), "files", filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// revertAll runs every revert entry point; each must refuse the forged manifest.
func (e *attackEnv) revertAll(t *testing.T, agent string, turn int, key string) {
	t.Helper()
	if err := e.store.RevertTurn(agent, turn); err == nil {
		t.Errorf("RevertTurn accepted forged manifest key %q", key)
	}
	if err := e.store.RevertAgent(agent); err == nil {
		t.Errorf("RevertAgent accepted forged manifest key %q", key)
	}
	if err := e.store.RevertFile(agent, turn, key); err == nil {
		t.Errorf("RevertFile accepted forged key %q", key)
	}
}

func TestForgedManifestTraversalExistedFalseDoesNotDeleteOutside(t *testing.T) {
	for _, key := range []string{"../fuera.txt", "../../fuera.txt", "../outside/fuera.txt", "sub/../../fuera.txt"} {
		t.Run(key, func(t *testing.T) {
			e := newAttackEnv(t)
			victims := []string{
				filepath.Join(e.base, "fuera.txt"),
				filepath.Join(filepath.Dir(e.base), "fuera.txt"),
				filepath.Join(e.base, "outside", "fuera.txt"),
			}
			for _, v := range victims {
				if err := os.WriteFile(v, []byte("precious"), 0o644); err != nil {
					t.Skipf("cannot create victim %s: %v", v, err)
				}
				defer os.Remove(v)
			}
			e.plant(t, "evil", 1, fmt.Sprintf(`%q: {"existed": false}`, key))
			e.revertAll(t, "evil", 1, key)
			for _, v := range victims {
				if got := mustRead(t, v); got != "precious" {
					t.Errorf("%s modified: %q", v, got)
				}
			}
		})
	}
}

func TestForgedManifestTraversalExistedTrueDoesNotWriteOutside(t *testing.T) {
	for _, key := range []string{"../fuera.txt", "../../fuera.txt", "../../../fuera.txt"} {
		t.Run(key, func(t *testing.T) {
			e := newAttackEnv(t)
			e.plant(t, "evil", 1, fmt.Sprintf(`%q: {"existed": true, "mode": 420}`, key))
			e.plantCopy(t, "evil", 1, key, "pwned")
			e.revertAll(t, "evil", 1, key)
			for _, dir := range []string{e.base, filepath.Dir(e.base), filepath.Dir(filepath.Dir(e.base))} {
				if _, err := os.Lstat(filepath.Join(dir, "fuera.txt")); err == nil {
					t.Errorf("fuera.txt written in %s", dir)
					_ = os.Remove(filepath.Join(dir, "fuera.txt"))
				}
			}
		})
	}
}

func TestForgedManifestWariKeyRejected(t *testing.T) {
	e := newAttackEnv(t)
	victim := filepath.Join(e.root, ".wari", "x")
	if err := os.WriteFile(victim, []byte("internal"), 0o644); err != nil {
		t.Fatal(err)
	}

	e.plant(t, "evil", 1, `".wari/x": {"existed": false}`)
	e.revertAll(t, "evil", 1, ".wari/x")
	if got := mustRead(t, victim); got != "internal" {
		t.Fatalf(".wari/x deleted/modified: %q", got)
	}

	e.plant(t, "evil", 2, `".wari/x": {"existed": true, "mode": 420}`)
	e.plantCopy(t, "evil", 2, ".wari/x", "pwned")
	e.revertAll(t, "evil", 2, ".wari/x")
	if got := mustRead(t, victim); got != "internal" {
		t.Fatalf(".wari/x overwritten: %q", got)
	}
}

func TestForgedManifestNonCanonicalKeyRejected(t *testing.T) {
	e := newAttackEnv(t)
	victim := filepath.Join(e.root, "a.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"./a.txt", "sub//../a.txt", "a.txt/", "/a.txt"} {
		e.plant(t, "evil", i+1, fmt.Sprintf(`%q: {"existed": false}`, key))
		if err := e.store.RevertTurn("evil", i+1); err == nil {
			t.Errorf("RevertTurn accepted non-canonical key %q", key)
		}
	}
	if got := mustRead(t, victim); got != "keep" {
		t.Fatalf("a.txt modified: %q", got)
	}
}

func TestForgedManifestSymlinkDirEscape(t *testing.T) {
	e := newAttackEnv(t)
	outside := filepath.Join(e.base, "outside")
	if err := os.Symlink(outside, filepath.Join(e.root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	victim := filepath.Join(outside, "f.txt")
	if err := os.WriteFile(victim, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	e.plant(t, "evil", 1, `"link/f.txt": {"existed": false}`)
	e.revertAll(t, "evil", 1, "link/f.txt")
	if got := mustRead(t, victim); got != "precious" {
		t.Fatalf("outside file modified through symlink: %q", got)
	}

	e.plant(t, "evil", 2, `"link/f.txt": {"existed": true, "mode": 420}`)
	e.plantCopy(t, "evil", 2, "link/f.txt", "pwned")
	e.revertAll(t, "evil", 2, "link/f.txt")
	if got := mustRead(t, victim); got != "precious" {
		t.Fatalf("outside file overwritten through symlink: %q", got)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Fatalf("stray files written outside root: %v", entries)
	}
}

// A symlink planted inside the checkpoint copy tree must not be followed outside root.
func TestForgedCheckpointCopySymlinkEscape(t *testing.T) {
	e := newAttackEnv(t)
	secret := filepath.Join(e.base, "outside", "secret.txt")
	if err := os.WriteFile(secret, []byte("topsecret"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.plant(t, "evil", 1, `"a.txt": {"existed": true, "mode": 420}`)
	copyPath := filepath.Join(e.turnDir("evil", 1), "files", "a.txt")
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, copyPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := e.store.RevertTurn("evil", 1); err == nil {
		t.Error("RevertTurn followed a symlinked checkpoint copy outside root")
	}
	if _, err := os.Lstat(filepath.Join(e.root, "a.txt")); err == nil {
		t.Error("secret content leaked into workspace")
	}
}

func TestForgedManifestModeOutOfRange(t *testing.T) {
	const (
		setuid = 1 << 23
		setgid = 1 << 22
		sticky = 1 << 20
		dir    = 1 << 31
	)
	for name, mode := range map[string]uint32{
		"setuid": setuid | 0o755,
		"setgid": setgid | 0o755,
		"sticky": sticky | 0o755,
		"dir":    dir | 0o755,
		"huge":   0xFFFFFFFF,
	} {
		t.Run(name, func(t *testing.T) {
			e := newAttackEnv(t)
			target := filepath.Join(e.root, "a.txt")
			if err := os.WriteFile(target, []byte("current"), 0o644); err != nil {
				t.Fatal(err)
			}
			e.plant(t, "evil", 1, fmt.Sprintf(`"a.txt": {"existed": true, "mode": %d}`, mode))
			e.plantCopy(t, "evil", 1, "a.txt", "restored")
			e.revertAll(t, "evil", 1, "a.txt")
			if got := mustRead(t, target); got != "current" {
				t.Fatalf("a.txt modified despite invalid mode: %q", got)
			}
			fi, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode() != 0o644 {
				t.Fatalf("mode changed to %v", fi.Mode())
			}
		})
	}
}

// Save must record only permission bits (no setuid/setgid/sticky) so that its
// own manifests always pass validation.
func TestSaveRecordsOnlyPermissionBits(t *testing.T) {
	e := newAttackEnv(t)
	target := filepath.Join(e.root, "s.sh")
	if err := os.WriteFile(target, []byte("orig"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755|os.ModeSetuid); err != nil {
		t.Skipf("setuid unsupported: %v", err)
	}
	if err := e.store.Save("a", 1, "s.sh"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.store.RevertTurn("a", 1); err != nil {
		t.Fatalf("RevertTurn of own checkpoint failed: %v", err)
	}
	if got := mustRead(t, target); got != "orig" {
		t.Fatalf("got %q", got)
	}
	fi, _ := os.Stat(target)
	if fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("special bits restored: %v", fi.Mode())
	}
}
