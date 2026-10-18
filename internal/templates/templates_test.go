package templates

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func loadTestdata(t *testing.T) map[string]Template {
	t.Helper()
	ts, err := Load(os.DirFS("testdata"), ".")
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Template{}
	for _, tpl := range ts {
		m[tpl.Name] = tpl
	}
	return m
}

func TestLoadTestdata(t *testing.T) {
	ts, err := Load(os.DirFS("testdata"), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Source != "java-class.tmpl" || ts[1].Source != "spring-controller.tmpl" {
		t.Fatalf("plantillas: %+v", ts)
	}
	c := ts[1]
	if c.Name != "Controller" || c.Lang != "java" || c.Ext != "java" || len(c.Vars) != 1 || c.Vars[0] != "basePath" {
		t.Fatalf("cabecera: %+v", c)
	}
	if !strings.HasPrefix(c.Body, "package {{package}};") {
		t.Fatalf("cuerpo: %q", c.Body)
	}
}

// Aceptación fase 9: el package sale de la ruta sin escribirlo.
func TestRenderControllerPackage(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "src", "main", "java", "com", "empresa", "api", "UserController.java")
	got, err := Render(loadTestdata(t)["Controller"], Context{
		Root:       root,
		TargetPath: target,
		Vars:       map[string]string{"basePath": "/users"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package com.empresa.api;\n",
		`@RequestMapping("/users")`,
		"public class UserController {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	root := t.TempDir()
	tpl := loadTestdata(t)["Controller"]
	target := filepath.Join("src", "main", "java", "a", "B.java") // relativa a root
	if _, err := Render(tpl, Context{Root: root, TargetPath: target}); err == nil || !strings.Contains(err.Error(), "basePath") {
		t.Errorf("debería faltar basePath: %v", err)
	}
	vars := map[string]string{"basePath": "/x"}
	if _, err := Render(tpl, Context{Root: root, TargetPath: filepath.Join("docs", "B.java"), Vars: vars}); !errors.Is(err, ErrNoSourceRoot) {
		t.Errorf("esperaba ErrNoSourceRoot: %v", err)
	}
	if _, err := Render(Template{Name: "x", Body: "{{nada}}"}, Context{Root: root, TargetPath: target}); err == nil {
		t.Error("variable desconocida debería fallar")
	}
	// Los valores derivados solo se calculan si se usan.
	if out, err := Render(Template{Name: "x", Body: "hola {{ x }}"}, Context{Vars: map[string]string{"x": "mundo"}}); err != nil || out != "hola mundo" {
		t.Errorf("%q %v", out, err)
	}
	// Vars puede sustituir un valor derivado.
	out, err := Render(loadTestdata(t)["Clase Java"], Context{Root: root, TargetPath: target, Vars: map[string]string{"package": "otro.pkg"}})
	if err != nil || !strings.Contains(out, "package otro.pkg;") || !strings.Contains(out, "class B {") {
		t.Errorf("%q %v", out, err)
	}
}

func TestParse(t *testing.T) {
	good := "---\r\nname: \"Mi clase\"\r\nlang: kotlin\r\next: .kt\r\nvars:\r\n  - a\r\n  - 'b'\r\n---\r\ncuerpo\r\n---\r\nmás\r\n"
	tpl, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Name != "Mi clase" || tpl.Ext != "kt" || len(tpl.Vars) != 2 || tpl.Vars[1] != "b" {
		t.Fatalf("%+v", tpl)
	}
	if tpl.Body != "cuerpo\r\n---\r\nmás\r\n" {
		t.Fatalf("cuerpo: %q", tpl.Body)
	}
	for _, bad := range []string{
		"sin cabecera",
		"---\nname: x\n",
		"---\nlang: java\n---\n",
		"---\nname: x\ncolor: rojo\n---\n",
		"---\nname: x\nvars: a\n---\n",
		"---\nname: x\n- a\n---\n",
		"---\nname: x\nvars: [1a]\n---\n",
		"---\nname x\n---\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) debería fallar", bad)
		}
	}
}

func TestLoadNamesBadFile(t *testing.T) {
	fsys := fstest.MapFS{
		"t/ok.tmpl":    {Data: []byte("---\nname: ok\n---\nx")},
		"t/mal.tmpl":   {Data: []byte("nada")},
		"t/README.txt": {Data: []byte("ignorado")},
	}
	if _, err := Load(fsys, "t"); err == nil || !strings.Contains(err.Error(), "mal.tmpl") {
		t.Fatalf("error esperado con el archivo: %v", err)
	}
	delete(fsys, "t/mal.tmpl")
	ts, err := Load(fsys, "t")
	if err != nil || len(ts) != 1 {
		t.Fatalf("%v %v", ts, err)
	}
}

func TestDerivePackage(t *testing.T) {
	root := filepath.FromSlash("/work/proj")
	cases := []struct {
		path, want string
		err        bool
	}{
		{"src/main/java/com/empresa/api/A.java", "com.empresa.api", false},
		{"src/main/kotlin/com/empresa/A.kt", "com.empresa", false},
		{"svc-a/src/main/java/org/x/A.java", "org.x", false},
		{"src/test/java/com/empresa/AT.java", "com.empresa", false},
		{"src/main/java/A.java", "", false},
		{"/work/proj/src/main/java/a/b/A.java", "a.b", false},
		{"src/main/resources/a/A.java", "", true},
		{"src/main/java/com/mi-app/A.java", "", true},
		{"src/main/java/1a/A.java", "", true},
		{"../otra/src/main/java/a/A.java", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := DerivePackage(root, filepath.FromSlash(c.path))
		if (err != nil) != c.err || got != c.want {
			t.Errorf("DerivePackage(%q) = %q, %v; quiero %q (err=%v)", c.path, got, err, c.want, c.err)
		}
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveNamespace(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Api", "Api.csproj"),
		`<Project Sdk="Microsoft.NET.Sdk.Web"><PropertyGroup><TargetFramework>net9.0</TargetFramework></PropertyGroup><PropertyGroup><RootNamespace>Acme.Api</RootNamespace></PropertyGroup></Project>`)
	writeFile(t, filepath.Join(root, "My App", "My App.csproj"), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	writeFile(t, filepath.Join(root, "Core.Domain", "Core.Domain.csproj"), `<Project></Project>`)
	if err := os.MkdirAll(filepath.Join(root, "Solo"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path, want string
		err        error
	}{
		{"Api/Controllers/UserController.cs", "Acme.Api.Controllers", nil},
		{"Api/Program.cs", "Acme.Api", nil},
		{"Api/Models/Dto/New/X.cs", "Acme.Api.Models.Dto.New", nil}, // carpetas aún inexistentes
		{"My App/1st Folder/X.cs", "My_App._1st_Folder", nil},
		{"Core.Domain/Entities/E.cs", "Core.Domain.Entities", nil},
		{"Solo/X.cs", "", ErrNoProject},
	}
	for _, c := range cases {
		got, err := DeriveNamespace(root, filepath.FromSlash(c.path))
		if !errors.Is(err, c.err) || got != c.want {
			t.Errorf("DeriveNamespace(%q) = %q, %v; quiero %q, %v", c.path, got, err, c.want, c.err)
		}
	}

	out, err := Render(Template{Name: "cs", Body: "namespace {{namespace}};\npublic class {{ClassName}} {}\n"},
		Context{Root: root, TargetPath: filepath.Join(root, "Api", "Controllers", "UserController.cs")})
	if err != nil || out != "namespace Acme.Api.Controllers;\npublic class UserController {}\n" {
		t.Errorf("%q %v", out, err)
	}
}

func TestDeriveNamespaceSymlinkIgnored(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "Outside.csproj"),
		`<Project><PropertyGroup><RootNamespace>Outside</RootNamespace></PropertyGroup></Project>`)
	if err := os.MkdirAll(filepath.Join(root, "Api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "Outside.csproj"), filepath.Join(root, "Api", "Api.csproj")); err != nil {
		t.Fatal(err)
	}

	_, err := DeriveNamespace(root, filepath.Join(root, "Api", "Foo.cs"))
	if !errors.Is(err, ErrNoProject) {
		t.Fatalf("esperaba ErrNoProject al ser symlink, obtuve: %v", err)
	}
}

func TestDeriveNamespaceBigFileRejection(t *testing.T) {
	root := t.TempDir()
	big := make([]byte, 1<<20+10)
	copy(big, `<Project><PropertyGroup><RootNamespace>Big</RootNamespace></PropertyGroup><!-- `)
	for i := 75; i < len(big)-5; i++ {
		big[i] = 'x'
	}
	copy(big[len(big)-5:], ` --></Project>`)
	writeFile(t, filepath.Join(root, "Big", "Big.csproj"), string(big))

	_, err := DeriveNamespace(root, filepath.Join(root, "Big", "Foo.cs"))
	if err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("esperaba error por superar 1 MiB, obtuve: %v", err)
	}
}
