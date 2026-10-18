package imports

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func split(s string) []string { return strings.Split(s, "\n") }

// insert aplica Insert y devuelve el texto resultante ("" y ok=false si no hay cambio).
func insert(t *testing.T, lang, src, cand string) (string, bool) {
	t.Helper()
	lines := split(src)
	e, ok := Insert(lang, lines, cand)
	if !ok {
		return src, false
	}
	return Apply(lines, e), true
}

func TestLoadAndMerge(t *testing.T) {
	m, err := Load(strings.NewReader(`{"List": ["java.util.List", "java.awt.List"], "Foo": "a.b.Foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Candidates("List"); len(got) != 2 || got[0] != "java.util.List" {
		t.Errorf("List: %v", got)
	}
	if got := m.Candidates("Foo"); len(got) != 1 || got[0] != "a.b.Foo" {
		t.Errorf("Foo: %v", got)
	}
	if m.Candidates("Nada") != nil {
		t.Error("tipo desconocido")
	}
	over, _ := Load(strings.NewReader(`{"Foo": ["x.Foo"], "Bar": ["x.Bar"]}`))
	mm := m.Merge(over)
	if got := mm.Candidates("Foo"); len(got) != 1 || got[0] != "x.Foo" {
		t.Errorf("Merge: %v", got)
	}
	if len(m.Candidates("Foo")) != 1 || m.Candidates("Foo")[0] != "a.b.Foo" || m.Candidates("Bar") != nil {
		t.Error("Merge no debe modificar el original")
	}
	if got := strings.Join(mm.Types(), ","); got != "Bar,Foo,List" {
		t.Errorf("Types: %s", got)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, js := range []string{`{`, `{"A": []}`, `{"A": [""]}`, `{"": ["x"]}`, `{"A": 3}`} {
		if _, err := Load(strings.NewReader(js)); err == nil {
			t.Errorf("Load(%s) debía fallar", js)
		}
	}
}

func TestLoadDir(t *testing.T) {
	fsys := fstest.MapFS{
		"imports/java.json":   {Data: []byte(`{"List": ["java.util.List"]}`)},
		"imports/csharp.json": {Data: []byte(`{"List": ["System.Collections.Generic"]}`)},
		"imports/notes.txt":   {Data: []byte("x")},
	}
	got, err := LoadDir(fsys, "imports")
	if err != nil || len(got) != 2 || got["java"].Candidates("List") == nil {
		t.Fatalf("LoadDir: %v %v", got, err)
	}
	fsys["imports/bad.json"] = &fstest.MapFile{Data: []byte("{")}
	if _, err := LoadDir(fsys, "imports"); err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("el error debía nombrar el archivo: %v", err)
	}
}

func TestLoadDirIgnoresSymlinkAndRejectsBigFile(t *testing.T) {
	fsys := fstest.MapFS{
		"imports/symlink.json": &fstest.MapFile{
			Mode: fs.ModeSymlink,
			Data: []byte(`{"Link": ["link.Link"]}`),
		},
	}
	got, err := LoadDir(fsys, "imports")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("debía ignorar symlinks, pero cargó: %+v", got)
	}

	big := make([]byte, 1<<20+10)
	copy(big, `{"Big": ["`)
	for i := 10; i < len(big)-4; i++ {
		big[i] = 'a'
	}
	copy(big[len(big)-4:], `"]}`)
	fsys["imports/big.json"] = &fstest.MapFile{Data: big}

	if _, err := LoadDir(fsys, "imports"); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("debía fallar por superar el límite de 1 MiB, obtuve: %v", err)
	}
}

func TestJavaInsertSortedWithinBlock(t *testing.T) {
	src := `package com.empresa.api;

import java.util.ArrayList;
import java.util.Map;
import org.springframework.stereotype.Service;

public class A {}`
	got, ok := insert(t, Java, src, "java.util.List")
	if !ok {
		t.Fatal("debía insertar")
	}
	want := strings.Replace(src, "import java.util.Map;", "import java.util.List;\nimport java.util.Map;", 1)
	if got != want {
		t.Errorf("\n%s\nquiero:\n%s", got, want)
	}
}

func TestJavaInsertGroupsAndStatic(t *testing.T) {
	block := []string{
		"java.util.Map", "jakarta.persistence.Id", "org.junit.Test", "com.x.Y", "static org.junit.Assert.assertEquals",
	}
	var src strings.Builder
	src.WriteString("package a;\n\n")
	for _, b := range block {
		src.WriteString("import " + b + ";\n")
	}
	src.WriteString("\nclass A {}")

	cases := []struct {
		cand  string
		after string // el import tras el cual debe quedar
	}{
		{"java.io.File", ""},
		{"java.util.Set", "java.util.Map"},
		{"javax.sql.DataSource", "jakarta.persistence.Id"},
		{"org.slf4j.Logger", "org.junit.Test"},
		{"com.a.B", "org.junit.Test"},
		{"zzz.Last", "com.x.Y"}, // los import static quedan siempre al final
	}
	for _, c := range cases {
		got, ok := insert(t, Java, src.String(), c.cand)
		if !ok {
			t.Fatalf("%s: no insertó", c.cand)
		}
		var imps []string
		for _, l := range split(got) {
			if strings.HasPrefix(l, "import ") {
				imps = append(imps, strings.TrimSuffix(strings.TrimPrefix(l, "import "), ";"))
			}
		}
		pos := -1
		for i, x := range imps {
			if x == c.cand {
				pos = i
			}
		}
		if pos < 0 {
			t.Fatalf("%s no aparece: %v", c.cand, imps)
		}
		prev := ""
		if pos > 0 {
			prev = imps[pos-1]
		}
		if prev != c.after {
			t.Errorf("%s quedó tras %q, quiero tras %q: %v", c.cand, prev, c.after, imps)
		}
		if strings.HasPrefix(imps[len(imps)-1], "static ") == false {
			t.Errorf("el import static debía seguir el último: %v", imps)
		}
	}
}

func TestJavaInsertOrderExact(t *testing.T) {
	src := "package a;\n\nimport com.x.Y;\nimport org.junit.Test;\nimport java.util.Map;\n"
	// El bloque está desordenado: el nuevo va antes del primero que lo sigue.
	got, _ := insert(t, Java, src, "java.io.File")
	if !strings.Contains(got, "import java.io.File;\nimport com.x.Y;") {
		t.Errorf("%s", got)
	}
	// Orden normal: tras el último cuando nada lo sigue.
	src2 := "package a;\n\nimport java.util.Map;\nimport org.junit.Test;\n\nclass A {}"
	got, _ = insert(t, Java, src2, "org.zed.Z")
	if !strings.Contains(got, "import org.junit.Test;\nimport org.zed.Z;\n\nclass A") {
		t.Errorf("%s", got)
	}
	got, _ = insert(t, Java, src2, "jakarta.persistence.Id")
	if !strings.Contains(got, "import java.util.Map;\nimport jakarta.persistence.Id;\nimport org.junit.Test;") {
		t.Errorf("%s", got)
	}
}

func TestJavaNoDuplicatesNorCoveredTypes(t *testing.T) {
	src := `package com.empresa.api;

import java.util.List;
import java.time.*;
import static java.lang.Math.max;

class A {}`
	for _, cand := range []string{
		"java.util.List",        // ya importado
		"java.time.LocalDate",   // cubierto por comodín
		"com.empresa.api.Other", // mismo package
	} {
		if _, ok := insert(t, Java, src, cand); ok {
			t.Errorf("%s: no debía insertar", cand)
		}
	}
	if _, ok := insert(t, Java, src, "java.util.Map"); !ok {
		t.Error("java.util.Map sí faltaba")
	}
	// El comodín de otro paquete no cubre.
	if _, ok := insert(t, Java, src, "java.time.format.DateTimeFormatter"); !ok {
		t.Error("el comodín java.time.* no cubre subpaquetes")
	}
	// Un import static no cuenta.
	if _, ok := insert(t, Java, src, "java.lang.Math"); !ok {
		t.Error("un import static de un miembro no importa la clase")
	}
}

func TestImportedSimpleNameConflict(t *testing.T) {
	lines := split("package a;\n\nimport java.awt.List;\n\nclass A {}")
	cands := []string{"java.util.List", "java.awt.List"}
	if !Imported(Java, lines, "List", cands) {
		t.Error("List ya está importado (java.awt.List)")
	}
	if Imported(Java, split("package a;\n\nclass A {}"), "List", cands) {
		t.Error("List no está importado")
	}
	if !Imported(Java, split("package a;\n\nimport java.util.*;\nclass A {}"), "List", cands) {
		t.Error("el comodín java.util.* cubre List")
	}
}

func TestJavaNoImportsInsertsAfterPackage(t *testing.T) {
	got, _ := insert(t, Java, "package a.b;\n\npublic class A {}\n", "java.util.List")
	if want := "package a.b;\n\nimport java.util.List;\n\npublic class A {}\n"; got != want {
		t.Errorf("%q\nquiero %q", got, want)
	}
	got, _ = insert(t, Java, "package a.b;\npublic class A {}", "java.util.List")
	if want := "package a.b;\n\nimport java.util.List;\n\npublic class A {}"; got != want {
		t.Errorf("%q\nquiero %q", got, want)
	}
	got, _ = insert(t, Java, "package a.b;", "java.util.List")
	if want := "package a.b;\n\nimport java.util.List;"; got != want {
		t.Errorf("%q\nquiero %q", got, want)
	}
	got, _ = insert(t, Java, "package a.b;\n", "java.util.List")
	if want := "package a.b;\n\nimport java.util.List;\n"; got != want {
		t.Errorf("%q\nquiero %q", got, want)
	}
}

func TestJavaNoPackage(t *testing.T) {
	got, _ := insert(t, Java, "/* licencia\n  mas */\n// nota\npublic class A {}", "java.util.List")
	if want := "/* licencia\n  mas */\n// nota\nimport java.util.List;\n\npublic class A {}"; got != want {
		t.Errorf("%q", got)
	}
	got, _ = insert(t, Java, "class A {}", "java.util.List")
	if want := "import java.util.List;\n\nclass A {}"; got != want {
		t.Errorf("%q", got)
	}
	got, _ = insert(t, Java, "", "java.util.List")
	if want := "import java.util.List;\n"; got != want {
		t.Errorf("%q", got)
	}
}

func TestKotlinImports(t *testing.T) {
	src := "package a\n\nimport java.util.UUID\nimport org.junit.Test\n\nclass A"
	got, _ := insert(t, Kotlin, src, "java.time.LocalDate")
	if want := "package a\n\nimport java.time.LocalDate\nimport java.util.UUID\nimport org.junit.Test\n\nclass A"; got != want {
		t.Errorf("%s", got)
	}
	// Kotlin: alfabético estricto; sin grupos de Java (jakarta antes que org y java).
	got, _ = insert(t, Kotlin, src, "jakarta.persistence.Id")
	if !strings.Contains(got, "import jakarta.persistence.Id\nimport java.util.UUID") {
		t.Errorf("%s", got)
	}
	if got, _ = insert(t, Kotlin, "package a\n\nclass A", "java.util.UUID"); got != "package a\n\nimport java.util.UUID\n\nclass A" {
		t.Errorf("%q", got)
	}
	// Alias: el nombre simple es el alias.
	lines := split("package a\n\nimport java.util.Date as UDate\n")
	if !Imported(Kotlin, lines, "UDate", nil) || Imported(Kotlin, lines, "Date", nil) {
		t.Error("el alias debía contar como nombre simple")
	}
}

func TestCSharpInsertSystemFirst(t *testing.T) {
	src := "using System;\nusing System.Linq;\nusing Microsoft.AspNetCore.Mvc;\n\nnamespace App.Api;\n\npublic class A {}"
	got, _ := insert(t, CSharp, src, "System.Collections.Generic")
	if want := "using System;\nusing System.Collections.Generic;\nusing System.Linq;\nusing Microsoft.AspNetCore.Mvc;\n\nnamespace App.Api;\n\npublic class A {}"; got != want {
		t.Errorf("%s", got)
	}
	got, _ = insert(t, CSharp, src, "Microsoft.EntityFrameworkCore")
	if !strings.Contains(got, "using Microsoft.AspNetCore.Mvc;\nusing Microsoft.EntityFrameworkCore;\n\nnamespace") {
		t.Errorf("%s", got)
	}
	// Un namespace no System va tras todos los System aunque sea alfabéticamente menor.
	got, _ = insert(t, CSharp, "using System;\nusing Zeta;\n\nclass A {}", "Alfa")
	if !strings.Contains(got, "using System;\nusing Alfa;\nusing Zeta;") {
		t.Errorf("%s", got)
	}
	// System va antes que los demás.
	got, _ = insert(t, CSharp, "using Alfa;\n\nclass A {}", "System.IO")
	if !strings.HasPrefix(got, "using System.IO;\nusing Alfa;") {
		t.Errorf("%s", got)
	}
}

func TestCSharpNoDuplicatesAndNamespace(t *testing.T) {
	src := "using System;\nusing System.Collections.Generic; // lista\n\nnamespace App.Models;\n\nclass A {}"
	for _, cand := range []string{"System", "System.Collections.Generic", "App.Models", "App"} {
		if _, ok := insert(t, CSharp, src, cand); ok {
			t.Errorf("%s: no debía insertar", cand)
		}
	}
	if _, ok := insert(t, CSharp, src, "App.Other"); !ok {
		t.Error("App.Other sí faltaba")
	}
	if !Imported(CSharp, split(src), "List", []string{"System.Collections.Generic"}) {
		t.Error("List ya está disponible")
	}
	// Los using de dentro de métodos no cuentan.
	body := "namespace A;\nclass B { void M() {\n    using (var x = y) {}\n    using System.IO;\n} }"
	if Covered(CSharp, split(body), "System.IO") {
		t.Error("un using indentado no es un import")
	}
}

func TestCSharpNoUsingsBlock(t *testing.T) {
	got, _ := insert(t, CSharp, "namespace A;\n\nclass B {}", "System.Collections.Generic")
	if want := "using System.Collections.Generic;\n\nnamespace A;\n\nclass B {}"; got != want {
		t.Errorf("%q", got)
	}
	got, _ = insert(t, CSharp, "// cabecera\n#nullable enable\n\nnamespace A;", "System")
	if want := "// cabecera\n#nullable enable\n\nusing System;\n\nnamespace A;"; got != want {
		t.Errorf("%q\nquiero %q", got, want)
	}
	got, _ = insert(t, CSharp, "", "System")
	if got != "using System;\n" {
		t.Errorf("%q", got)
	}
}

func TestCSharpAliasAndStaticAtEnd(t *testing.T) {
	src := "using System;\nusing static System.Math;\nusing J = Newtonsoft.Json;\n\nclass A {}"
	got, _ := insert(t, CSharp, src, "Zeta")
	if !strings.Contains(got, "using System;\nusing Zeta;\nusing static System.Math;") {
		t.Errorf("%s", got)
	}
}

func TestExtend(t *testing.T) {
	base := Map{"List": {"java.util.List", "java.awt.List"}, "Date": {"java.util.Date"}}
	extra := Map{"List": {"com.acme.List", "java.util.List"}, "User": {"com.acme.User"}}
	got := base.Extend(extra)
	if l := got.Candidates("List"); strings.Join(l, ",") != "com.acme.List,java.util.List,java.awt.List" {
		t.Errorf("List: %v (los del proyecto van primero, sin repetir)", l)
	}
	if got.Candidates("User")[0] != "com.acme.User" || got.Candidates("Date")[0] != "java.util.Date" {
		t.Errorf("User/Date: %v", got)
	}
	if len(base.Candidates("List")) != 2 || base.Candidates("User") != nil {
		t.Error("Extend no debe modificar el mapa original")
	}
	if len(Map(nil).Extend(extra)) != 2 {
		t.Error("Extend sobre un mapa nil")
	}
}
