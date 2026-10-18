package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func proyecto(t testing.TB) string {
	t.Helper()
	return filepath.Join("..", "..", "testdata", "proyecto")
}

func readTD(t testing.TB, rel string) (string, []byte) {
	t.Helper()
	p := filepath.Join(proyecto(t), filepath.FromSlash(rel))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return rel, b
}

// outline resume los símbolos como "kind Contenedor.Nombre".
func outline(f *File) []string {
	var out []string
	for _, s := range f.Symbols {
		n := s.Name
		if s.Container != "" {
			n = s.Container + "." + n
		}
		out = append(out, string(s.Kind)+" "+n)
	}
	return out
}

func wantOutline(t *testing.T, f *File, want ...string) {
	t.Helper()
	got := outline(f)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("outline distinto.\ngot:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func parseTD(t *testing.T, rel string) *File {
	t.Helper()
	_, b := readTD(t, rel)
	return Parse(LangOf(rel), rel, b)
}

func TestOutlineJava(t *testing.T) {
	f := parseTD(t, "src/main/java/com/acme/shop/UserController.java")
	if f.Package != "com.acme.shop" {
		t.Errorf("package = %q", f.Package)
	}
	wantOutline(t, f,
		"package com.acme.shop",
		"class UserController",
		"field UserController.service",
		"field UserController.PREFIJO",
		"constructor UserController.UserController",
		"method UserController.find",
		"method UserController.create",
		"enum UserController.Estado",
		"constant UserController.Estado.ACTIVO",
		"constant UserController.Estado.INACTIVO",
		"class UserController.Interna",
		"field UserController.Interna.contador",
		"method UserController.Interna.incrementar",
	)
	byName := map[string]Symbol{}
	for _, s := range f.Symbols {
		if s.Kind != KindConstructor {
			byName[s.Name] = s
		}
	}
	if got := byName["UserController"].Annotations; len(got) != 2 || got[0] != "@RestController" || got[1] != `@RequestMapping("/users")` {
		t.Errorf("anotaciones de la clase: %q", got)
	}
	if got := byName["find"].Annotations; len(got) != 1 || got[0] != `@GetMapping("/{id}")` {
		t.Errorf("anotaciones de find: %q", got)
	}
	if got := byName["find"].Signature; got != "(Long id)" {
		t.Errorf("firma de find: %q", got)
	}
	// Línea y columna (base 0) del nombre.
	if s := byName["find"]; s.Line != 25 || s.Col != 16 {
		t.Errorf("find en %d:%d, quería 25:16", s.Line, s.Col)
	}
	if s := byName["UserController"]; s.Line != 15 || s.Col != 13 {
		t.Errorf("UserController en %d:%d, quería 15:13", s.Line, s.Col)
	}
	wantImports := []string{
		"com.acme.shop.model.User", "com.acme.shop.service.*",
	}
	for _, w := range wantImports {
		found := false
		for _, i := range f.Imports {
			found = found || i == w
		}
		if !found {
			t.Errorf("falta el import %q en %v", w, f.Imports)
		}
	}
}

func TestOutlineKotlin(t *testing.T) {
	f := parseTD(t, "src/main/kotlin/com/acme/shop/kt/Pedido.kt")
	wantOutline(t, f,
		"package com.acme.shop.kt",
		"class Pedido",
		"property Pedido.id",
		"property Pedido.total",
		"property Pedido.dueno",
		"object Registro",
		"property Registro.pedidos",
		"method Registro.agregar",
		"interface Repositorio",
		"method Repositorio.todos",
		"class PedidoService",
		"property PedidoService.repo",
		"method PedidoService.listar",
		"property PedidoService.plantilla",
		"object PedidoService.Companion",
		"property PedidoService.Companion.MAX",
		"enum Estado",
		"constant Estado.NUEVO",
		"constant Estado.PAGADO",
		"method resumen",
	)
	for _, s := range f.Symbols {
		if s.Name == "listar" && (len(s.Annotations) != 1 || s.Annotations[0] != `@GetMapping("/pedidos")`) {
			t.Errorf("anotaciones de listar: %q", s.Annotations)
		}
		if s.Name == "PedidoService" && (len(s.Annotations) != 1 || s.Annotations[0] != "@Service") {
			t.Errorf("anotaciones de PedidoService: %q", s.Annotations)
		}
	}
}

func TestOutlineCSharp(t *testing.T) {
	f := parseTD(t, "src/Acme/Shop/Controllers/ProductosController.cs")
	if f.Package != "Acme.Shop.Controllers" {
		t.Errorf("namespace = %q", f.Package)
	}
	wantOutline(t, f,
		"namespace Acme.Shop.Controllers",
		"class ProductosController",
		"field ProductosController._items",
		"constructor ProductosController.ProductosController",
		"method ProductosController.Listar",
		"method ProductosController.Crear",
		"property ProductosController.Total",
	)
	for _, s := range f.Symbols {
		switch s.Name {
		case "ProductosController":
			if s.Kind == KindClass && (len(s.Annotations) != 2 || s.Annotations[0] != "[ApiController]" || s.Annotations[1] != `[Route("api/[controller]")]`) {
				t.Errorf("atributos de la clase: %q", s.Annotations)
			}
		case "Crear":
			if len(s.Annotations) != 1 || s.Annotations[0] != `[HttpPost("nuevo")]` {
				t.Errorf("atributos de Crear: %q", s.Annotations)
			}
		}
	}
	g := parseTD(t, "src/Acme/Shop/Models/Producto.cs")
	wantOutline(t, g,
		"namespace Acme.Shop.Models",
		"record Producto",
		"property Producto.Id",
		"property Producto.Nombre",
		"class Categoria",
		"field Categoria.Texto",
		"property Categoria.Codigo",
	)
}

// Lo que está en comentarios, cadenas y bloques de texto no se indexa.
func TestNothingInCommentsOrStrings(t *testing.T) {
	f := parseTD(t, "src/main/java/com/acme/shop/Fantasma.java")
	wantOutline(t, f,
		"package com.acme.shop",
		"class Fantasma",
		"field Fantasma.texto",
		"method Fantasma.real",
		"method Fantasma.despues",
	)
	for _, rel := range []string{
		"src/main/java/com/acme/shop/UserController.java",
		"src/main/kotlin/com/acme/shop/kt/Pedido.kt",
		"src/Acme/Shop/Models/Producto.cs",
		"src/Acme/Shop/Controllers/ProductosController.cs",
	} {
		for _, s := range parseTD(t, rel).Symbols {
			switch s.Name {
			case "Comentado", "EnCadena", "falso", "enJavadoc", "enComentario", "ComentadoKt",
				"EnRawString", "falsa", "EnVerbatim":
				t.Errorf("%s: %s %q no debía indexarse", rel, s.Kind, s.Name)
			}
		}
	}
}

func TestMaskCases(t *testing.T) {
	cases := []struct {
		name, lang, src string
		want            []string
	}{
		{"java text block con comillas y llaves", LangJava, "class A {\n  String s = \"\"\"\n    class Falsa { void f() {} }\n    \\\"\"\" y \"x\" }\n    \"\"\";\n  void real() {}\n}\n",
			[]string{"class A", "field A.s", "method A.real"}},
		{"java cadena con comillas escapadas", LangJava, "class A { String s = \"a\\\"class B {}\\\"\"; void m() {} }",
			[]string{"class A", "field A.s", "method A.m"}},
		{"java char de llave", LangJava, "class A { char c = '{'; char d = '\\''; void m() {} }",
			[]string{"class A", "field A.c", "field A.d", "method A.m"}},
		{"java comentario sin cerrar", LangJava, "class A { void m() {} /* class B {}\n void n() {}",
			[]string{"class A", "method A.m"}},
		{"kotlin comentario anidado", LangKotlin, "class A { /* a /* class B */ class C */ fun m() {} }",
			[]string{"class A", "method A.m"}},
		{"kotlin plantilla con cadena anidada", LangKotlin, "class A { val s = \"${map[\"k\"]} class B\"\n fun m() {} }",
			[]string{"class A", "property A.s", "method A.m"}},
		{"kotlin raw string con comillas", LangKotlin, "class A { val s = \"\"\"\n  class B { fun f() {} } \"\" \n\"\"\"\n fun m() {} }",
			[]string{"class A", "property A.s", "method A.m"}},
		{"csharp verbatim con comillas dobles", LangCSharp, "class A { string s = @\"class B { \"\"x\"\" }\"; void M() {} }",
			[]string{"class A", "field A.s", "method A.M"}},
		{"csharp interpolada", LangCSharp, "class A { string s = $\"{{class B}} {Foo(\"x\")}\"; void M() {} }",
			[]string{"class A", "field A.s", "method A.M"}},
		{"csharp interpolada verbatim", LangCSharp, "class A { string s = $@\"class B {{ }}\\ {x}\"; void M() {} }",
			[]string{"class A", "field A.s", "method A.M"}},
		{"csharp cadena cruda", LangCSharp, "class A { string s = \"\"\"\n class B { \" } \"\"\";\n void M() {} }",
			[]string{"class A", "field A.s", "method A.M"}},
		{"csharp directiva con apóstrofe", LangCSharp, "#region Cosas del usuario's\nclass A { void M() {} }\n#endregion\n",
			[]string{"class A", "method A.M"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantOutline(t, Parse(c.lang, "x", []byte(c.src)), c.want...)
		})
	}
}

func TestMemberShapes(t *testing.T) {
	cases := []struct {
		name, lang, src string
		want            []string
	}{
		{"java genéricos, arrays y varios declarantes", LangJava,
			"class A { Map<String, List<Integer>> m = new HashMap<String, Integer>(); int a, b = 2, c[]; <T extends Comparable<T>> T max(List<T> xs) throws Exception { return null; } }",
			[]string{"class A", "field A.m", "field A.a", "field A.b", "field A.c", "method A.max"}},
		{"java campo con clase anónima y lambda", LangJava,
			"class A { Runnable r = new Runnable() { public void run() { class L {} } }; Supplier<X> s = () -> { return null; }; void m() {} }",
			[]string{"class A", "field A.r", "field A.s", "method A.m"}},
		{"java interface, default y @interface", LangJava,
			"interface I { int K = 1; default void d() {} void a(); } @interface Z { String value() default \"\"; }",
			[]string{"interface I", "field I.K", "method I.d", "method I.a", "annotation Z", "method Z.value"}},
		{"java record con constructor compacto", LangJava,
			"public record P(int x, @NotNull String y) { P { if (x < 0) throw new X(); } static int z() { return 1; } }",
			[]string{"record P", "field P.x", "field P.y", "constructor P.P", "method P.z"}},
		{"java enum con cuerpo y miembros", LangJava,
			"enum E { A(1) { void f() {} }, B(2); private final int n; E(int n) { this.n = n; } int n() { return n; } }",
			[]string{"enum E", "constant E.A", "constant E.B", "field E.n", "constructor E.E", "method E.n"}},
		{"java inicializador estático", LangJava,
			"class A { static { init(); } { other(); } int x; }",
			[]string{"class A", "field A.x"}},
		{"kotlin funciones, extensiones y propiedades", LangKotlin,
			"package p\nfun String.shout(): String = this.uppercase()\nfun <T> List<T>.second(): T { return this[1] }\nval x: Int get() = 1\nprivate var y = mutableMapOf<String, Int>()\nlateinit var z: String\nclass K { fun a() = 1\n fun b() {} }",
			[]string{"package p", "method shout", "method second", "property x", "property y", "property z", "class K", "method K.a", "method K.b"}},
		{"kotlin clase sin cuerpo, constructor primario y secundario", LangKotlin,
			"class A(val a: Int, private var b: String, c: Long) : Base(a)\nclass B private constructor(val q: Int) { constructor(): this(1) }\nfun f() {}",
			[]string{"class A", "property A.a", "property A.b", "class B", "property B.q", "constructor B.B", "method f"}},
		{"kotlin sealed, fun interface y typealias", LangKotlin,
			"sealed interface S\nfun interface F { fun go() }\ntypealias L = List<String>\nannotation class Ann\nabstract class Q { abstract fun x() }",
			[]string{"interface S", "interface F", "method F.go", "typealias L", "class Ann", "class Q", "method Q.x"}},
		{"kotlin objeto y companion con nombre", LangKotlin,
			"object O { const val X = 1 }\nclass C { companion object Factory { fun make() = C() } }",
			[]string{"object O", "property O.X", "class C", "object C.Factory", "method C.Factory.make"}},
		{"csharp propiedades, eventos y expresiones", LangCSharp,
			"namespace N; public class A { public int X { get; set; } = 5; public string Y => \"y\"; public event EventHandler Changed; public const int K = 1; public int this[int i] { get { return 0; } } public static A operator +(A a, A b) => a; public (int, int) Pair() => (1, 2); ~A() {} }",
			[]string{"namespace N", "class A", "property A.X", "property A.Y", "event A.Changed", "constant A.K", "method A.Pair", "method A.~A"}},
		{"csharp genéricos, restricciones, delegate y enum", LangCSharp,
			"namespace N { public delegate void D(int a); public enum E : byte { A, B = 2, C } public interface I<T> where T : class { T Get<T>(int id) where T : class; } public struct S { public int F; } }",
			[]string{"namespace N", "delegate D", "enum E", "constant E.A", "constant E.B", "constant E.C", "interface I", "method I.Get", "struct S", "field S.F"}},
		{"csharp namespaces anidados y using alias", LangCSharp,
			"using System;\nusing L = System.Collections.Generic.List<int>;\nusing static System.Math;\nnamespace A { namespace B { class C {} } class D {} }",
			[]string{"namespace A", "namespace A.B", "class C", "class D"}},
		{"csharp top level statements no se indexan", LangCSharp,
			"using System;\nConsole.WriteLine(\"hola\");\nvar x = new Foo();\nclass A {}",
			[]string{"class A"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantOutline(t, Parse(c.lang, "x", []byte(c.src)), c.want...)
		})
	}
}

func TestPackagesAndColumns(t *testing.T) {
	f := Parse(LangCSharp, "x", []byte("namespace A { namespace B { class C {} } class D {} }"))
	pk := map[string]string{}
	for _, s := range f.Symbols {
		pk[s.Name] = s.Package
	}
	if pk["C"] != "A.B" || pk["D"] != "A" {
		t.Errorf("namespaces: %v", pk)
	}
	// La columna cuenta runas, no bytes.
	g := Parse(LangJava, "x", []byte("class Ñandú { int año; void métodoÚnico() {} }"))
	for _, s := range g.Symbols {
		if s.Name == "métodoÚnico" && s.Col != 28 {
			t.Errorf("columna de métodoÚnico = %d, quería 28", s.Col)
		}
	}
}

// Código a medias o hostil: no entra en bucles ni entra en pánico.
func TestMalformedInput(t *testing.T) {
	var srcs []string
	for _, rel := range []string{
		"src/main/java/com/acme/shop/UserController.java",
		"src/main/kotlin/com/acme/shop/kt/Pedido.kt",
		"src/Acme/Shop/Controllers/ProductosController.cs",
	} {
		_, b := readTD(t, rel)
		for n := 0; n <= len(b); n += 7 {
			srcs = append(srcs, rel+"\x00"+string(b[:n]))
		}
	}
	srcs = append(srcs,
		"lang\x00"+strings.Repeat("class A {", 20000),
		"lang\x00"+strings.Repeat("}", 5000),
		"lang\x00"+strings.Repeat("(", 5000)+strings.Repeat("[", 5000),
		"lang\x00"+strings.Repeat("\"\"\"", 999),
		"lang\x00"+strings.Repeat("${", 3000)+"\"",
		"lang\x00"+strings.Repeat("namespace A {", 5000),
		"lang\x00@@@ @ @interface ; ; = => -> << >> <<<<",
		"lang\x00"+string([]byte{0xff, 0xfe, 0x00, '\'', '"', '`', '@'}),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, s := range srcs {
			name, src, _ := strings.Cut(s, "\x00")
			langs := []string{LangOf(name)}
			if langs[0] == "" {
				langs = []string{LangJava, LangKotlin, LangCSharp}
			}
			for _, l := range langs {
				Parse(l, name, []byte(src))
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("Parse no termina con entrada malformada")
	}
}

func TestUnknownLang(t *testing.T) {
	if f := Parse("rust", "x.rs", []byte("fn main() {}")); len(f.Symbols) != 0 {
		t.Errorf("símbolos de un lenguaje desconocido: %v", f.Symbols)
	}
}

// Medición: indexar el .java de 5000 líneas.
func TestParseGrandeSpeed(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "java", "Grande.java"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(string(b), "\n")
	var best time.Duration = time.Hour
	var f *File
	for range 5 {
		start := time.Now()
		f = Parse(LangJava, "Grande.java", b)
		best = min(best, time.Since(start))
	}
	t.Logf("Grande.java: %d líneas, %d bytes, %d símbolos, mejor de 5: %v", lines, len(b), len(f.Symbols), best)
	limit := 50 * time.Millisecond
	if raceEnabled {
		limit *= 10 // el detector de carreras multiplica el costo
	}
	if best > limit {
		t.Errorf("analizar Grande.java tardó %v (> %v)", best, limit)
	}
	if len(f.Symbols) < 100 {
		t.Errorf("pocos símbolos: %d", len(f.Symbols))
	}
	_ = fmt.Sprint
}
