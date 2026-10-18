package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustResolve(t *testing.T, s Spec, rt string) resolved {
	t.Helper()
	r, err := s.resolve(rt)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return r
}

// pair comprueba que flag va seguido de val en args.
func hasPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func has(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}

func baseSpec() Spec {
	return Spec{Root: "/home/u/proj", Image: "alpine:3", Command: []string{"echo", "hola"}}
}

func TestBuildArgsOverlayPodmanDefaults(t *testing.T) {
	r := mustResolve(t, baseSpec(), "podman")
	args := buildArgs(r, "wari-abc", "abc", "/cache/wari/runs/abc")

	if args[0] != "run" || !has(args, "--rm") {
		t.Fatalf("faltan run --rm: %v", args)
	}
	for flag, val := range map[string]string{
		"--name":         "wari-abc",
		"--network":      "none",
		"--cpus":         "2",
		"--memory":       "2g",
		"--memory-swap":  "2g",
		"--pids-limit":   "1024",
		"--workdir":      "/workspace",
		"--entrypoint":   "sh",
		"-v":             "/home/u/proj:/workspace:O",
		"--security-opt": "no-new-privileges",
	} {
		if !hasPair(args, flag, val) {
			t.Errorf("falta %s %s en %v", flag, val, args)
		}
	}
	if !hasPair(args, "--mount", "type=bind,src=/cache/wari/runs/abc,dst=/out") {
		t.Errorf("falta el montaje de salidas: %v", args)
	}
	// El proyecto solo aparece como capa inferior del overlay.
	for _, a := range args {
		if strings.Contains(a, "/home/u/proj") && a != "/home/u/proj:/workspace:O" {
			t.Errorf("el proyecto aparece fuera del overlay: %q", a)
		}
	}
	// Imagen, guion fijo y comando, en ese orden y al final.
	tail := args[len(args)-6:]
	want := []string{"alpine:3", "-c", overlayScript, "wari-sandbox", "echo", "hola"}
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("cola = %v, quería %v", tail, want)
		}
	}
}

func TestBuildArgsCopyDockerReadOnlySource(t *testing.T) {
	r := mustResolve(t, baseSpec(), "docker")
	if r.strategy != StrategyCopy {
		t.Fatalf("estrategia = %q", r.strategy)
	}
	args := buildArgs(r, "wari-abc", "abc", "/o")
	if !hasPair(args, "--mount", "type=bind,src=/home/u/proj,dst=/src,ro") {
		t.Errorf("el proyecto debe montarse en /src de solo lectura: %v", args)
	}
	if !hasPair(args, "--mount", "type=volume,dst=/workspace") {
		t.Errorf("falta el volumen anónimo de /workspace: %v", args)
	}
	if !has(args, copyScript) || !strings.Contains(copyScript, "cp -a /src/. /workspace/") {
		t.Errorf("falta el guion de copia: %v", args)
	}
}

func TestBuildArgsNoShellInterpolation(t *testing.T) {
	evil := []string{"sh", "-c", "echo $(id); rm -rf / # `x`"}
	s := baseSpec()
	s.Command = evil
	s.Env = map[string]string{"MSG": "a b; echo $HOME", "Z": "1"}
	r := mustResolve(t, s, "podman")
	args := buildArgs(r, "wari-abc", "abc", "/o")

	marker := -1
	for i, a := range args {
		if a == "wari-sandbox" {
			marker = i
		}
	}
	if marker < 0 {
		t.Fatalf("falta el marcador: %v", args)
	}
	for i, a := range evil {
		if args[marker+1+i] != a {
			t.Errorf("el comando debe ir como argv separado: %v", args[marker+1:])
		}
	}
	for _, a := range args[:marker+1] {
		if strings.Contains(a, "rm -rf") || strings.Contains(a, "$(id)") {
			t.Errorf("valor del usuario antes del marcador: %q", a)
		}
	}
	if strings.Contains(overlayScript, "rm") || strings.ContainsAny(overlayScript, "`;") {
		t.Errorf("el guion fijo debe ser mínimo: %q", overlayScript)
	}
	if !hasPair(args, "-e", "MSG=a b; echo $HOME") || !hasPair(args, "-e", "WARI_OUT=/out") {
		t.Errorf("variables mal pasadas: %v", args)
	}
	// Orden estable: WARI_OUT, MSG, Z.
	var envs []string
	for i, a := range args {
		if a == "-e" {
			envs = append(envs, args[i+1])
		}
	}
	if strings.Join(envs, "|") != "MSG=a b; echo $HOME|WARI_OUT=/out|Z=1" {
		t.Errorf("env = %v", envs)
	}
}

func TestBuildArgsLimitsNetworkPortsMountsWorkDir(t *testing.T) {
	s := baseSpec()
	s.CPU = 1.5
	s.Memory = "512m"
	s.Network = NetworkBridge
	s.Ports = []Port{{Container: 8080}, {Host: 9090, Container: 9000, Protocol: "udp"}}
	s.ExtraMounts = []Mount{{Host: "/data/ref", Container: "/ref/"}}
	s.WorkDir = "svc/api"
	args := buildArgs(mustResolve(t, s, "podman"), "n", "i", "/o")

	for flag, val := range map[string]string{
		"--cpus":    "1.5",
		"--memory":  "512m",
		"--network": "bridge",
		"-p":        "127.0.0.1:8080:8080/tcp",
		"--workdir": "/workspace/svc/api",
	} {
		if !hasPair(args, flag, val) {
			t.Errorf("falta %s %s en %v", flag, val, args)
		}
	}
	if !hasPair(args, "-p", "127.0.0.1:9090:9000/udp") {
		t.Errorf("falta el puerto udp: %v", args)
	}
	if !hasPair(args, "--mount", "type=bind,src=/data/ref,dst=/ref,ro") {
		t.Errorf("los montajes extra deben ser de solo lectura: %v", args)
	}
	for i, a := range args {
		if a == "--mount" && strings.Contains(args[i+1], "/data/") && !strings.HasSuffix(args[i+1], ",ro") {
			t.Errorf("montaje extra escribible: %q", args[i+1])
		}
	}
}

func TestUniqueNames(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id, err := newID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("id repetido %s", id)
		}
		seen[id] = true
	}
}

func TestResolveRejects(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Spec)
		rt   string
	}{
		{"raíz relativa", func(s *Spec) { s.Root = "proj" }, "podman"},
		{"raíz con coma", func(s *Spec) { s.Root = "/a,b" }, "podman"},
		{"raíz con dos puntos", func(s *Spec) { s.Root = "/a:b" }, "podman"},
		{"imagen como opción", func(s *Spec) { s.Image = "--privileged" }, "podman"},
		{"imagen con espacio", func(s *Spec) { s.Image = "a b" }, "podman"},
		{"red host", func(s *Spec) { s.Network = "host" }, "podman"},
		{"red container", func(s *Spec) { s.Network = "container:db" }, "podman"},
		{"puertos sin red", func(s *Spec) { s.Ports = []Port{{Container: 80}} }, "podman"},
		{"puerto cero", func(s *Spec) { s.Network = "bridge"; s.Ports = []Port{{}} }, "podman"},
		{"protocolo", func(s *Spec) { s.Network = "bridge"; s.Ports = []Port{{Container: 1, Protocol: "sctp"}} }, "podman"},
		{"clave de env", func(s *Spec) { s.Env = map[string]string{"A=B": "x"} }, "podman"},
		{"memoria", func(s *Spec) { s.Memory = "1g --privileged" }, "podman"},
		{"cpu negativa", func(s *Spec) { s.CPU = -1 }, "podman"},
		{"workdir fuera", func(s *Spec) { s.WorkDir = "../x" }, "podman"},
		{"workdir absoluto", func(s *Spec) { s.WorkDir = "/etc" }, "podman"},
		{"overlay con docker", func(s *Spec) { s.Strategy = StrategyOverlay }, "docker"},
		{"estrategia", func(s *Spec) { s.Strategy = "otra" }, "podman"},
		{"montaje relativo", func(s *Spec) { s.ExtraMounts = []Mount{{Host: "x", Container: "/x"}} }, "podman"},
		{"montaje con coma", func(s *Spec) { s.ExtraMounts = []Mount{{Host: "/x,rw", Container: "/x"}} }, "podman"},
		{"montaje sobre workspace", func(s *Spec) { s.ExtraMounts = []Mount{{Host: "/x", Container: "/workspace/x"}} }, "podman"},
		{"montaje sobre out", func(s *Spec) { s.ExtraMounts = []Mount{{Host: "/x", Container: "/out"}} }, "podman"},
		{"montaje en raíz", func(s *Spec) { s.ExtraMounts = []Mount{{Host: "/x", Container: "/"}} }, "podman"},
		{"montaje relativo en contenedor", func(s *Spec) { s.ExtraMounts = []Mount{{Host: "/x", Container: "x"}} }, "podman"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := baseSpec()
			tt.mod(&s)
			if _, err := s.resolve(tt.rt); err == nil {
				t.Fatalf("debía rechazarse: %+v", s)
			}
		})
	}
}

func TestResolveNetworkNoneByDefault(t *testing.T) {
	r := mustResolve(t, baseSpec(), "podman")
	if r.Network != NetworkNone {
		t.Fatalf("Network = %q", r.Network)
	}
}

func TestResolveProfileFillsImageAndCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pom.xml"), []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := mustResolve(t, Spec{Root: dir}, "podman")
	if r.Image != "maven:3-eclipse-temurin-21" || strings.Join(r.Command, " ") != "mvn spring-boot:run" {
		t.Fatalf("perfil no aplicado: %q %v", r.Image, r.Command)
	}
	// Lo explícito gana al perfil.
	r = mustResolve(t, Spec{Root: dir, Image: "alpine", Command: []string{"ls"}}, "podman")
	if r.Image != "alpine" || r.Command[0] != "ls" {
		t.Fatalf("lo explícito debía ganar: %q %v", r.Image, r.Command)
	}
	if _, err := (Spec{Root: dir, Profile: "nope"}).resolve("podman"); err == nil {
		t.Fatal("perfil desconocido debía fallar")
	}
	empty := t.TempDir()
	if _, err := (Spec{Root: empty}).resolve("podman"); err != ErrNoImage {
		t.Fatalf("err = %v, quería ErrNoImage", err)
	}
	if _, err := (Spec{Root: empty, Image: "alpine"}).resolve("podman"); err != ErrNoCommand {
		t.Fatalf("err = %v, quería ErrNoCommand", err)
	}
}

func TestDetectProfile(t *testing.T) {
	tests := []struct {
		files []string
		want  string
	}{
		{[]string{"pom.xml"}, "maven"},
		{[]string{"build.gradle"}, "gradle"},
		{[]string{"build.gradle.kts"}, "gradle"},
		{[]string{"Shop.csproj"}, "dotnet"},
		{[]string{"Shop.CSPROJ"}, "dotnet"},
		{[]string{"pom.xml", "build.gradle"}, "maven"},
		{[]string{"build.gradle", "Shop.csproj"}, "gradle"},
		{[]string{"README.md"}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		for _, f := range tt.files {
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		p, ok := DetectProfile(dir)
		if (tt.want == "") == ok || p.Name != tt.want {
			t.Errorf("%v: perfil %q ok=%v, quería %q", tt.files, p.Name, ok, tt.want)
		}
	}
	// Un directorio llamado pom.xml no cuenta, y una raíz inexistente tampoco.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pom.xml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := DetectProfile(dir); ok {
		t.Error("un directorio no es un pom.xml")
	}
	if _, ok := DetectProfile(filepath.Join(dir, "no")); ok {
		t.Error("raíz inexistente")
	}
}

func TestProfileCommands(t *testing.T) {
	want := map[string][2]string{
		"maven":  {"maven:3-eclipse-temurin-21", "mvn spring-boot:run"},
		"gradle": {"gradle:jdk21", "gradle bootRun"},
		"dotnet": {"mcr.microsoft.com/dotnet/sdk:8.0", "dotnet run"},
	}
	for name, w := range want {
		p, ok := ProfileByName(name)
		if !ok || p.Image != w[0] || strings.Join(p.Command, " ") != w[1] {
			t.Errorf("%s = %+v", name, p)
		}
		if !imageRe.MatchString(p.Image) {
			t.Errorf("imagen %q no pasa la validación", p.Image)
		}
	}
}
