package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func setup(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está en el PATH")
	}
	// Sin configuración global ni del sistema: el resultado no depende de la
	// máquina.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, _, err := git(context.Background(), dir, maxGitOut, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newRepo crea un repo con un commit que versiona a.txt, b.txt y .env.example.
func newRepo(t *testing.T) string {
	t.Helper()
	setup(t)
	root := t.TempDir()
	run(t, root, "init", "-q", "-b", "main")
	run(t, root, "config", "user.name", "Test")
	run(t, root, "config", "user.email", "t@example.com")
	write(t, filepath.Join(root, "a.txt"), "uno\ndos\ntres\n")
	write(t, filepath.Join(root, "b.txt"), "bbb\n")
	write(t, filepath.Join(root, ".env.example"), "X=\n")
	run(t, root, "add", ".")
	run(t, root, "commit", "-q", "-m", "inicial")
	return root
}

func mustCreate(t *testing.T, root, name string) Info {
	t.Helper()
	info, err := Create(context.Background(), root, name, nil)
	if err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
	return info
}

func TestCreate(t *testing.T) {
	root := newRepo(t)
	info := mustCreate(t, root, "Claude Uno")
	if info.Name != "claude-uno" || info.Branch != "wari/claude-uno" {
		t.Fatalf("info = %+v", info)
	}
	if want := filepath.Join(root, ".wari", "worktrees", "claude-uno"); info.Path != want {
		t.Fatalf("Path = %s, quería %s", info.Path, want)
	}
	if read(t, filepath.Join(info.Path, "a.txt")) != "uno\ndos\ntres\n" {
		t.Fatal("el worktree no tiene los archivos de HEAD")
	}
	if head := strings.TrimSpace(run(t, root, "rev-parse", "HEAD")); info.Base != head {
		t.Fatalf("Base = %s, HEAD = %s", info.Base, head)
	}
	if !strings.Contains(run(t, root, "branch", "--list", "wari/claude-uno"), "wari/claude-uno") {
		t.Fatal("falta la rama")
	}
	if got := run(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("el árbol principal quedó sucio: %q", got)
	}
	// No se duplica la línea de exclude al crear otro.
	mustCreate(t, root, "otro")
	ex := read(t, filepath.Join(root, ".git", "info", "exclude"))
	if strings.Count(ex, ".wari/worktrees/") != 1 {
		t.Fatalf("exclude = %q", ex)
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); err == nil {
		t.Fatal("no debe crear .gitignore")
	}
}

func TestCreateNames(t *testing.T) {
	root := newRepo(t)
	for _, c := range []struct{ in, want string }{
		{"../x", "x"},
		{"a b", "a-b"},
		{"", "agente"},
		{"///", "agente-2"},
		{"Ñandú__Test", "and-test"},
		{"x", "x-2"}, // "x" ya existe por "../x"
		{"x", "x-3"},
		{strings.Repeat("a", 60), strings.Repeat("a", 40)},
	} {
		info := mustCreate(t, root, c.in)
		if info.Name != c.want {
			t.Errorf("Create(%q).Name = %q, quería %q", c.in, info.Name, c.want)
		}
		if err := info.valid(); err != nil {
			t.Errorf("%q: %v", c.in, err)
		}
	}
}

func TestCreateCopiesInclude(t *testing.T) {
	root := newRepo(t)
	outside := filepath.Join(t.TempDir(), "secreto")
	write(t, outside, "TOP=1\n")
	write(t, filepath.Join(root, ".env"), "KEY=1\n")
	write(t, filepath.Join(root, "src/main/resources/application-dev.yml"), "a: 1\n")
	write(t, filepath.Join(root, "otro.txt"), "no\n")
	if err := os.Symlink(outside, filepath.Join(root, ".env.local")); err != nil {
		t.Skip("sin enlaces simbólicos:", err)
	}
	info, err := Create(context.Background(), root, "inc", LoadInclude(root))
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(info.Path, ".env")) != "KEY=1\n" {
		t.Fatal(".env no se copió")
	}
	if read(t, filepath.Join(info.Path, "src/main/resources/application-dev.yml")) != "a: 1\n" {
		t.Fatal("falta application-dev.yml")
	}
	if _, err := os.Lstat(filepath.Join(info.Path, ".env.local")); err == nil {
		t.Fatal("se copió un enlace simbólico")
	}
	if _, err := os.Stat(filepath.Join(info.Path, "otro.txt")); err == nil {
		t.Fatal("se copió un archivo que no encaja")
	}
	// Los copiados no cuentan como cambios ni entran en el commit de Merge.
	ch, err := info.Changes(context.Background())
	if err != nil || len(ch) != 0 {
		t.Fatalf("Changes = %v, %v", ch, err)
	}
	write(t, filepath.Join(info.Path, "n.txt"), "n\n")
	res, err := info.Merge(context.Background())
	if err != nil || !res.Merged {
		t.Fatalf("Merge = %+v, %v", res, err)
	}
	if out := run(t, root, "ls-files", ".env", "src/main/resources/application-dev.yml"); out != "" {
		t.Fatalf("un archivo copiado entró en git: %q", out)
	}
}

func TestChangesDiffPatch(t *testing.T) {
	root := newRepo(t)
	info := mustCreate(t, root, "cdp")
	ctx := context.Background()
	write(t, filepath.Join(info.Path, "a.txt"), "uno\nDOS\ntres\n")
	write(t, filepath.Join(info.Path, "nuevo/n.txt"), "hola\n")
	if err := os.Remove(filepath.Join(info.Path, "b.txt")); err != nil {
		t.Fatal(err)
	}
	ch, err := info.Changes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{"M", "a.txt"}, {"D", "b.txt"}, {"?", "nuevo/n.txt"}}
	if !reflect.DeepEqual(ch, want) {
		t.Fatalf("Changes = %v, quería %v", ch, want)
	}

	old, cur, err := info.Diff(ctx, "a.txt")
	if err != nil || string(old) != "uno\ndos\ntres\n" || string(cur) != "uno\nDOS\ntres\n" {
		t.Fatalf("Diff a.txt = %q %q %v", old, cur, err)
	}
	old, cur, err = info.Diff(ctx, "nuevo/n.txt")
	if err != nil || old != nil || string(cur) != "hola\n" {
		t.Fatalf("Diff nuevo = %q %q %v", old, cur, err)
	}
	old, cur, err = info.Diff(ctx, "b.txt")
	if err != nil || string(old) != "bbb\n" || cur != nil {
		t.Fatalf("Diff borrado = %q %q %v", old, cur, err)
	}
	for _, bad := range []string{"../a.txt", "/etc/passwd"} {
		if _, _, err := info.Diff(ctx, bad); err == nil {
			t.Errorf("Diff(%q) debía fallar", bad)
		}
	}

	patch, err := info.Patch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"-dos", "+DOS", "deleted file mode", "b/nuevo/n.txt", "+hola"} {
		if !strings.Contains(patch, s) {
			t.Errorf("el parche no contiene %q:\n%s", s, patch)
		}
	}

	// Un cambio confirmado en la rama sigue contando contra Base.
	run(t, info.Path, "add", "-A")
	run(t, info.Path, "commit", "-q", "-m", "x")
	ch, _ = info.Changes(ctx)
	if !reflect.DeepEqual(ch, []Change{{"M", "a.txt"}, {"D", "b.txt"}, {"A", "nuevo/n.txt"}}) {
		t.Fatalf("Changes tras commit = %v", ch)
	}
}

func TestMergeClean(t *testing.T) {
	root := newRepo(t)
	info := mustCreate(t, root, "ok")
	ctx := context.Background()
	write(t, filepath.Join(info.Path, "nuevo.txt"), "hola\n")
	res, err := info.Merge(ctx)
	if err != nil || !res.Merged {
		t.Fatalf("Merge = %+v, %v", res, err)
	}
	if read(t, filepath.Join(root, "nuevo.txt")) != "hola\n" {
		t.Fatal("el árbol principal no recibió el archivo")
	}
	if got := run(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("árbol principal sucio: %q", got)
	}
	// Segunda fusión sin nada nuevo.
	res, err = info.Merge(ctx)
	if err != nil || !res.Merged || res.Message != "sin cambios" {
		t.Fatalf("Merge repetido = %+v, %v", res, err)
	}
}

func assertMainIntact(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("quedó MERGE_HEAD")
	}
}

func TestMergeConflict(t *testing.T) {
	root := newRepo(t)
	info := mustCreate(t, root, "conf")
	ctx := context.Background()
	write(t, filepath.Join(info.Path, "a.txt"), "uno\nAGENTE\ntres\n")
	write(t, filepath.Join(root, "a.txt"), "uno\nPRINCIPAL\ntres\n")
	run(t, root, "commit", "-q", "-am", "principal")

	res, err := info.Merge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged || !reflect.DeepEqual(res.Conflicts, []string{"a.txt"}) || res.Message == "" {
		t.Fatalf("Merge = %+v", res)
	}
	assertMainIntact(t, root)
	if got := run(t, root, "status", "--porcelain"); got != "" {
		t.Fatalf("árbol principal sucio: %q", got)
	}
	if read(t, filepath.Join(root, "a.txt")) != "uno\nPRINCIPAL\ntres\n" {
		t.Fatal("el archivo principal cambió")
	}
}

func TestMergeDirtyMain(t *testing.T) {
	root := newRepo(t)
	info := mustCreate(t, root, "sucio")
	write(t, filepath.Join(info.Path, "a.txt"), "uno\nAGENTE\ntres\n")
	write(t, filepath.Join(root, "a.txt"), "LOCAL SIN CONFIRMAR\n")

	res, err := info.Merge(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged || !reflect.DeepEqual(res.Conflicts, []string{"a.txt"}) {
		t.Fatalf("Merge = %+v", res)
	}
	assertMainIntact(t, root)
	if read(t, filepath.Join(root, "a.txt")) != "LOCAL SIN CONFIRMAR\n" {
		t.Fatal("se perdió el cambio local del árbol principal")
	}
}

func TestMergeKeepsAuthorsMergeInProgress(t *testing.T) {
	root := newRepo(t)
	// Una fusión del autor sin concluir: otra rama en conflicto con main.
	run(t, root, "checkout", "-q", "-b", "otra")
	write(t, filepath.Join(root, "b.txt"), "otra\n")
	run(t, root, "commit", "-q", "-am", "otra")
	run(t, root, "checkout", "-q", "main")
	write(t, filepath.Join(root, "b.txt"), "main\n")
	run(t, root, "commit", "-q", "-am", "main")
	if _, _, err := git(context.Background(), root, 1<<20, "merge", "otra"); err == nil {
		t.Fatal("esperaba conflicto")
	}
	info := mustCreate(t, root, "x")
	res, err := info.Merge(context.Background())
	if err != nil || res.Merged {
		t.Fatalf("Merge = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "MERGE_HEAD")); err != nil {
		t.Fatal("se abortó la fusión del autor")
	}
}

func TestRemoveIdempotent(t *testing.T) {
	root := newRepo(t)
	info := mustCreate(t, root, "rm")
	ctx := context.Background()
	write(t, filepath.Join(info.Path, "sucio.txt"), "x\n")
	write(t, filepath.Join(info.Path, "a.txt"), "cambiado\n")
	for n := 0; n < 2; n++ {
		if err := info.Remove(ctx); err != nil {
			t.Fatalf("Remove #%d: %v", n+1, err)
		}
	}
	if _, err := os.Stat(info.Path); err == nil {
		t.Fatal("la carpeta sigue")
	}
	if run(t, root, "branch", "--list", info.Branch) != "" {
		t.Fatal("la rama sigue")
	}
	// Si la carpeta se borró a mano, también se limpia.
	info2 := mustCreate(t, root, "rm")
	if err := os.RemoveAll(info2.Path); err != nil {
		t.Fatal(err)
	}
	if err := info2.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run(t, root, "worktree", "list"), info2.Name) {
		t.Fatal("el worktree sigue registrado")
	}
	// Una Info forjada se rechaza.
	if err := (Info{Root: root, Path: root, Branch: "wari/x"}).Remove(ctx); err == nil {
		t.Fatal("debía rechazar un Path fuera de .wari/worktrees")
	}
}

func TestAvailable(t *testing.T) {
	setup(t)
	ctx := context.Background()
	if err := Available(ctx, t.TempDir(), 0); err == nil || !strings.Contains(err.Error(), "repositorio git") {
		t.Fatalf("sin git: %v", err)
	}
	empty := t.TempDir()
	run(t, empty, "init", "-q")
	if err := Available(ctx, empty, 0); err == nil || !strings.Contains(err.Error(), "ningún commit") {
		t.Fatalf("sin commits: %v", err)
	}
	root := newRepo(t)
	if err := Available(ctx, root, 0); err != nil {
		t.Fatal(err)
	}
	if err := Available(ctx, root, 1<<62); err == nil || !strings.Contains(err.Error(), "espacio") {
		t.Fatalf("poco espacio: %v", err)
	}
}

func TestLoadInclude(t *testing.T) {
	root := t.TempDir()
	if got := LoadInclude(root); !reflect.DeepEqual(got, defaultInclude) {
		t.Fatalf("predeterminados = %v", got)
	}
	write(t, filepath.Join(root, ".wari/worktree-include"), "# nota\n\n  *.secret  \nconfig/*.toml\n")
	if got := LoadInclude(root); !reflect.DeepEqual(got, []string{"*.secret", "config/*.toml"}) {
		t.Fatalf("con archivo = %v", got)
	}
	var many strings.Builder
	for n := 0; n < 100; n++ {
		many.WriteString("p\n")
	}
	write(t, filepath.Join(root, ".wari/worktree-include"), many.String())
	if got := LoadInclude(root); len(got) != maxPatterns {
		t.Fatalf("len = %d", len(got))
	}
}

func TestAdvanceHidesWhatWasMerged(t *testing.T) {
	root := newRepo(t)
	info := mustCreate(t, root, "adv")
	ctx := context.Background()
	write(t, filepath.Join(info.Path, "uno.txt"), "1\n")
	if res, err := info.Merge(ctx); err != nil || !res.Merged {
		t.Fatalf("Merge = %+v, %v", res, err)
	}
	info, err := info.Advance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ch, err := info.Changes(ctx); err != nil || len(ch) != 0 {
		t.Fatalf("tras fusionar y avanzar no debe haber cambios: %v %v", ch, err)
	}
	write(t, filepath.Join(info.Path, "dos.txt"), "2\n")
	ch, err := info.Changes(ctx)
	if err != nil || len(ch) != 1 || ch[0].Path != "dos.txt" {
		t.Fatalf("solo lo nuevo debe salir: %v %v", ch, err)
	}
}

func TestPruneRemovesOnlyDeadOwnersWithNothingToLose(t *testing.T) {
	root := newRepo(t)
	ctx := context.Background()
	mine := mustCreate(t, root, "vivo")   // dueño: este proceso, vivo
	dead := mustCreate(t, root, "muerto") // dueño muerto, limpio
	dirty := mustCreate(t, root, "sucio") // dueño muerto, con trabajo
	for _, i := range []Info{dead, dirty} {
		p, err := i.ownerPath(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("2147483646"), 0o600); err != nil { // PID inexistente
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(dirty.Path, "trabajo.txt"), "no perder\n")

	if n := Prune(ctx, root); n != 1 {
		t.Fatalf("Prune borró %d, quería 1", n)
	}
	if _, err := os.Stat(dead.Path); err == nil {
		t.Error("el worktree sin dueño y limpio debía borrarse")
	}
	for _, i := range []Info{mine, dirty} {
		if _, err := os.Stat(i.Path); err != nil {
			t.Errorf("%s no debía borrarse: %v", i.Name, err)
		}
	}
}
