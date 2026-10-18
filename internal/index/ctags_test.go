package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Salida real de Universal Ctags 6.2.1 (--output-format=json) sobre un .java
// y un .kt, recortada.
const cannedTags = `{"_type": "tag", "name": "com.x", "path": "./J.java", "pattern": "/^package com.x;$/", "line": 1, "kind": "package"}
{"_type": "tag", "name": "J", "path": "./J.java", "pattern": "/^public class J {$/", "line": 3, "kind": "class"}
{"_type": "tag", "name": "J", "path": "./J.java", "pattern": "/^  public J(int a) { this.a = a; }$/", "line": 5, "kind": "method", "signature": "(int a)", "scope": "J", "scopeKind": "class"}
{"_type": "tag", "name": "A", "path": "./J.java", "pattern": "/^  enum E { A, B }$/", "line": 6, "kind": "enumConstant", "scope": "J.E", "scopeKind": "enum"}
{"_type": "ptag", "name": "TAG_FORMAT", "path": "2"}
{"_type": "tag", "name": "a.b", "path": "./K.kt", "pattern": "/^package a.b$/", "line": 1, "kind": "package"}
{"_type": "tag", "name": "f", "path": "./K.kt", "pattern": "/^object O { fun f() {} }$/", "line": 3, "kind": "method", "scope": "a.b.O", "scopeKind": "object"}
{"_type": "tag", "name": "x", "path": "./K.kt", "pattern": "/^data class P(val x:Int)$/", "line": 2, "kind": "constant", "scope": "a.b.P", "scopeKind": "class"}
`

func TestParseCannedCtagsOutput(t *testing.T) {
	tags, err := parseTags([]byte(cannedTags))
	if err != nil {
		t.Fatal(err)
	}
	if len(tags["J.java"]) != 4 || len(tags["K.kt"]) != 3 {
		t.Fatalf("etiquetas: %v", tags)
	}
	j := fileFromTags("J.java", tags["J.java"])
	wantOutline(t, j, "package com.x", "class J", "constructor J.J", "constant J.E.A")
	for _, s := range j.Symbols {
		if s.Package != "com.x" {
			t.Errorf("%s: package %q", s.Name, s.Package)
		}
	}
	if s := j.Symbols[2]; s.Line != 4 || s.Col != 9 || s.Signature != "(int a)" {
		t.Errorf("constructor: %+v", s)
	}
	k := fileFromTags("K.kt", tags["K.kt"])
	wantOutline(t, k, "package a.b", "method O.f", "property P.x")
	if k.Symbols[1].Package != "a.b" || k.Symbols[1].Col != 15 {
		t.Errorf("f: %+v", k.Symbols[1])
	}
	if _, err := parseTags([]byte("{no es json}\n")); err == nil {
		t.Error("una línea inválida debía fallar")
	}
}

func fakeCtags(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script de shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ctags"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestCtagsMissingFallsBackToNative(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, _, err := CtagsVersion(context.Background()); !errors.Is(err, ErrNoCtags) {
		t.Fatalf("sin ctags: %v", err)
	}
	ix, st := newIndexed(t, Options{Ctags: true})
	if st.Source != "go" || st.Files != 9 {
		t.Errorf("sin ctags debía usar el analizador propio: %+v", st)
	}
	if got := ix.Symbols("src/main/java/com/acme/shop/UserController.java"); len(got) == 0 || len(got[1].Annotations) != 2 {
		t.Error("el analizador propio debía dar las anotaciones")
	}
}

func TestNonUniversalCtagsIsRejected(t *testing.T) {
	fakeCtags(t, "echo 'Exuberant Ctags 5.8, Copyright (C) 1996-2009 Darren Hiebert'\n")
	if _, _, err := CtagsVersion(context.Background()); !errors.Is(err, ErrNoCtags) {
		t.Fatalf("ctags clásico debía rechazarse: %v", err)
	}
	if _, st := newIndexed(t, Options{Ctags: true}); st.Source != "go" {
		t.Errorf("source = %q", st.Source)
	}
}

func TestCtagsFailureFallsBackPerBatch(t *testing.T) {
	fakeCtags(t, `case "$1" in --version) echo "Universal Ctags 9.9.9, Copyright"; exit 0;; esac
echo "ctags: roto" >&2
exit 1
`)
	ver := ""
	if _, v, err := CtagsVersion(context.Background()); err != nil {
		t.Fatal(err)
	} else {
		ver = v
	}
	if ver != "9.9.9" {
		t.Fatalf("versión = %q", ver)
	}
	_, st := newIndexed(t, Options{Ctags: true})
	if st.Source != "ctags 9.9.9" || st.Fallback != 9 || st.Files != 9 || st.Symbols < 60 {
		t.Errorf("con ctags roto debía recurrir al analizador propio: %+v", st)
	}
}

// Con un universal-ctags real, comprueba la integración contra el analizador
// propio. Se omite si no está instalado.
func TestRealCtagsAgreesWithNative(t *testing.T) {
	bin, ver, err := CtagsVersion(context.Background())
	if err != nil {
		t.Skip("universal-ctags no disponible:", err)
	}
	t.Logf("universal-ctags %s en %s", ver, bin)
	native, _ := newIndexed(t, Options{})
	viaCtags, st := newIndexed(t, Options{Ctags: true})
	if st.Source != "ctags "+ver || st.Fallback != 0 || st.Files != 9 {
		t.Fatalf("stats con ctags: %+v", st)
	}
	type key struct {
		path, name string
		line       int
	}
	have := map[key]Kind{}
	for _, f := range viaCtags.view().files {
		for _, s := range f.f.Symbols {
			have[key{s.Path, s.Name, s.Line}] = s.Kind
		}
	}
	checked, missed := 0, 0
	for rel, e := range native.view().files {
		for _, s := range e.f.Symbols {
			switch s.Kind {
			case KindClass, KindInterface, KindEnum, KindObject, KindMethod, KindConstructor, KindNamespace, KindPackage:
				checked++
				if _, ok := have[key{rel, s.Name, s.Line}]; !ok {
					// ctags no reconoce, p. ej., los métodos de C# con cuerpo de expresión.
					missed++
					t.Logf("ctags no dio %s %s en %s:%d", s.Kind, s.Name, rel, s.Line+1)
				}
			}
		}
	}
	if missed*10 > checked {
		t.Errorf("ctags no dio %d de %d símbolos", missed, checked)
	}
	if checked < 30 {
		t.Errorf("se compararon pocos símbolos: %d", checked)
	}
	// Mismo comportamiento de "ir a definición" y de los imports.
	ctrl := parseTD(t, "src/main/java/com/acme/shop/UserController.java")
	for _, ix := range []*Index{native, viaCtags} {
		got := ix.Resolve("User", ctrl, -1, 0)
		if len(got) != 1 || !strings.HasSuffix(got[0].Path, "shop/model/User.java") {
			t.Errorf("Resolve(User) = %v", got)
		}
		if m := ix.ImportMap()["java"]["User"]; len(m) != 2 {
			t.Errorf("ImportMap(User) = %v", m)
		}
	}
	// La columna se recupera del patrón.
	for _, s := range viaCtags.Symbols("src/main/java/com/acme/shop/UserController.java") {
		if s.Name == "find" && s.Col != 16 {
			t.Errorf("columna de find vía ctags = %d, quería 16", s.Col)
		}
	}
}
