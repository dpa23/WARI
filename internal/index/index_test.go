package index

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// copyProyecto copia testdata/proyecto a un directorio temporal, para poder
// modificarlo.
func copyProyecto(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := proyecto(t)
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func newIndexed(t *testing.T, opts Options) (*Index, Stats) {
	t.Helper()
	if opts.Root == "" {
		opts.Root = proyecto(t)
	}
	ix := New(opts)
	st := ix.Refresh(context.Background())
	return ix, st
}

func TestRefreshIndexesProject(t *testing.T) {
	ix, st := newIndexed(t, Options{})
	if !ix.Ready() {
		t.Fatal("el índice debía estar listo")
	}
	// 6 java, 1 kotlin y 2 c#.
	if st.Files != 9 || st.Parsed != 9 || st.Removed != 0 || st.Truncated {
		t.Errorf("stats = %+v", st)
	}
	if st.Source != "go" {
		t.Errorf("source = %q", st.Source)
	}
	if st.Symbols < 60 {
		t.Errorf("pocos símbolos: %d", st.Symbols)
	}
	if got := ix.Symbols("src/main/java/com/acme/shop/model/User.java"); len(got) != 6 {
		t.Errorf("símbolos de User.java: %v", got)
	}
	t.Logf("indexado: %+v", st)
}

func TestRefreshIsIncremental(t *testing.T) {
	root := copyProyecto(t)
	ix, st := newIndexed(t, Options{Root: root})
	if st.Parsed != 9 {
		t.Fatalf("primera pasada: %+v", st)
	}
	if st = ix.Refresh(context.Background()); st.Parsed != 0 || st.Removed != 0 || st.Files != 9 {
		t.Errorf("sin cambios no se relee nada: %+v", st)
	}

	// Un archivo cambiado (mtime nuevo) se relee, y solo ese.
	p := filepath.Join(root, "src/main/java/com/acme/shop/model/User.java")
	if err := os.WriteFile(p, []byte("package com.acme.shop.model;\npublic class User {\n  void nuevoMetodo() {}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	st = ix.Refresh(context.Background())
	if st.Parsed != 1 || st.Files != 9 {
		t.Errorf("cambio de un archivo: %+v", st)
	}
	got := ix.Symbols("src/main/java/com/acme/shop/model/User.java")
	if len(got) != 3 || got[2].Name != "nuevoMetodo" {
		t.Errorf("símbolos tras el cambio: %v", got)
	}

	// Un archivo nuevo entra; uno borrado sale.
	if err := os.WriteFile(filepath.Join(root, "src/main/java/com/acme/shop/Nuevo.java"), []byte("package com.acme.shop;\nclass Nuevo {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "src/main/java/com/acme/legacy/Migracion.java")); err != nil {
		t.Fatal(err)
	}
	st = ix.Refresh(context.Background())
	if st.Parsed != 1 || st.Removed != 1 || st.Files != 9 {
		t.Errorf("alta y baja: %+v", st)
	}
	if len(ix.Resolve("Migracion", nil, -1, 0)) != 0 || len(ix.Resolve("Nuevo", nil, -1, 0)) != 1 {
		t.Error("el índice no refleja el alta y la baja")
	}
}

func TestRefreshHonorsIgnoreAndKinds(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/A.java", "package a;\nclass A {}\n")
	write("target/classes/Compilado.java", "package a;\nclass Compilado {}\n")
	write("src/notas.txt", "class Texto {}")
	write("src/Grande.java", "package a;\nclass Grande {}\n"+strings.Repeat("// relleno relleno relleno\n", 100))
	if err := os.Symlink(filepath.Join(root, "src/A.java"), filepath.Join(root, "src/Enlace.java")); err != nil {
		t.Log("sin symlinks:", err)
	}
	ignore := func(rel string, isDir bool) bool { return isDir && rel == "target" }
	ix, st := newIndexed(t, Options{Root: root, Ignore: ignore, MaxFileSize: 1000})
	if len(ix.Resolve("Compilado", nil, -1, 0)) != 0 {
		t.Error("lo ignorado por el árbol no se indexa")
	}
	if len(ix.Resolve("Texto", nil, -1, 0)) != 0 {
		t.Error("solo se indexan .java, .kt y .cs")
	}
	if len(ix.Resolve("Grande", nil, -1, 0)) != 0 || st.Skipped != 1 {
		t.Errorf("un archivo mayor que MaxFileSize se omite (skipped=%d)", st.Skipped)
	}
	if len(ix.Resolve("A", nil, -1, 0)) != 1 {
		t.Errorf("A debía estar indexada una sola vez (los enlaces no se siguen): %v", ix.Resolve("A", nil, -1, 0))
	}
}

func TestRefreshBudget(t *testing.T) {
	ix, st := newIndexed(t, Options{MaxSymbols: 30})
	if !st.Truncated || st.Symbols > 30 || st.Skipped == 0 {
		t.Errorf("el presupuesto de símbolos no se aplicó: %+v", st)
	}
	ix2, st2 := newIndexed(t, Options{MaxFiles: 3})
	if !st2.Truncated || st2.Files != 3 {
		t.Errorf("el presupuesto de archivos no se aplicó: %+v", st2)
	}
	_, _ = ix, ix2
}

func TestRefreshCanceled(t *testing.T) {
	ix := New(Options{Root: proyecto(t)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ix.Refresh(ctx)
	if ix.Ready() {
		t.Error("una pasada cancelada no deja el índice listo")
	}
}

func TestRefreshBusy(t *testing.T) {
	ix := New(Options{Root: proyecto(t)})
	ix.refreshMu.Lock()
	st := ix.Refresh(context.Background())
	ix.refreshMu.Unlock()
	if !st.Busy {
		t.Error("con otra pasada en curso debía devolver Busy")
	}
}

// Consultas y pasadas a la vez: el detector de carreras decide.
func TestConcurrentQueries(t *testing.T) {
	root := copyProyecto(t)
	ix, _ := newIndexed(t, Options{Root: root})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ix.Resolve("User", nil, -1, 0)
				ix.Complete("Use", nil, 10)
				ix.ImportMap()
				ix.Stats()
			}
		}()
	}
	for i := range 5 {
		p := filepath.Join(root, "src/main/java/com/acme/shop/Nuevo.java")
		os.WriteFile(p, []byte("package com.acme.shop;\nclass Nuevo {}\n"), 0o644)
		future := time.Now().Add(time.Duration(i+1) * time.Hour)
		os.Chtimes(p, future, future)
		ix.Refresh(context.Background())
	}
	close(stop)
	wg.Wait()
}

// curFile analiza un archivo del proyecto como lo haría el editor con su
// buffer.
func curFile(t *testing.T, rel string) *File {
	t.Helper()
	return parseTD(t, rel)
}

func paths(syms []Symbol) []string {
	var out []string
	for _, s := range syms {
		out = append(out, s.Path)
	}
	return out
}

func TestResolveGotoDefinition(t *testing.T) {
	ix, _ := newIndexed(t, Options{})
	const (
		modelUser  = "src/main/java/com/acme/shop/model/User.java"
		legacyUser = "src/main/java/com/acme/legacy/User.java"
	)
	ctrl := curFile(t, "src/main/java/com/acme/shop/UserController.java")

	// User: dos clases con ese nombre; UserController importa la del modelo.
	if got := paths(ix.Resolve("User", ctrl, -1, 0)); !slices.Equal(got, []string{modelUser}) {
		t.Errorf("User desde UserController (import explícito): %v", got)
	}
	// Desde el paquete legacy gana la de su propio paquete.
	mig := curFile(t, "src/main/java/com/acme/legacy/Migracion.java")
	if got := paths(ix.Resolve("User", mig, -1, 0)); !slices.Equal(got, []string{legacyUser}) {
		t.Errorf("User desde Migracion (mismo package): %v", got)
	}
	// Sin relación con ninguna, es ambiguo: las dos, para el selector.
	if got := paths(ix.Resolve("User", nil, -1, 0)); !slices.Equal(got, []string{legacyUser, modelUser}) {
		t.Errorf("User sin contexto: %v", got)
	}
	// Un import con comodín (service.*) trae UserService de otro archivo.
	got := ix.Resolve("UserService", ctrl, -1, 0)
	if len(got) != 1 || got[0].Path != "src/main/java/com/acme/shop/service/UserService.java" || got[0].Kind != KindClass {
		t.Errorf("UserService: %v", got)
	}
	// Un método del mismo archivo.
	if got := ix.Resolve("find", ctrl, -1, 0); len(got) != 1 || got[0].Path != ctrl.Path {
		t.Errorf("find: %v", got)
	}
	// Un método de otra clase.
	if got := ix.Resolve("buscar", ctrl, -1, 0); len(got) != 1 || !strings.HasSuffix(got[0].Path, "UserService.java") {
		t.Errorf("buscar: %v", got)
	}
	// Entre lenguajes: una clase de Kotlin y un tipo de C#.
	if got := ix.Resolve("Pedido", ctrl, -1, 0); len(got) != 1 || got[0].Kind != KindClass || !strings.HasSuffix(got[0].Path, "Pedido.kt") {
		t.Errorf("Pedido: %v", got)
	}
	prod := curFile(t, "src/Acme/Shop/Controllers/ProductosController.cs")
	if got := ix.Resolve("Producto", prod, -1, 0); len(got) != 1 || got[0].Kind != KindRecord || !strings.HasSuffix(got[0].Path, "Producto.cs") {
		t.Errorf("Producto: %v", got)
	}
	// La clase gana al constructor del mismo nombre.
	if got := ix.Resolve("UserController", mig, -1, 0); len(got) != 1 || got[0].Kind != KindClass {
		t.Errorf("UserController: %v", got)
	}
	// Con el cursor sobre la declaración, esa no cuenta.
	var decl Symbol
	for _, s := range ctrl.Symbols {
		if s.Name == "find" {
			decl = s
		}
	}
	if got := ix.Resolve("find", ctrl, decl.Line, decl.Col+1); len(got) != 0 {
		t.Errorf("sobre la propia declaración: %v", got)
	}
	if got := ix.Resolve("NoExiste", ctrl, -1, 0); len(got) != 0 {
		t.Errorf("NoExiste: %v", got)
	}
	// Con el índice nil solo se mira el archivo.
	var nilIx *Index
	if got := nilIx.Resolve("find", ctrl, -1, 0); len(got) != 1 {
		t.Errorf("índice nil: %v", got)
	}
}

func TestComplete(t *testing.T) {
	ix, _ := newIndexed(t, Options{})
	prod := curFile(t, "src/Acme/Shop/Controllers/ProductosController.cs")

	names := func(cs []Completion) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Name)
		}
		return out
	}
	got := names(ix.Complete("Prod", prod, 20))
	if !slices.Contains(got, "Producto") || !slices.Contains(got, "ProductosController") {
		t.Errorf("Prod: %v", got)
	}
	if slices.Contains(got, "Categoria") {
		t.Errorf("Prod no debía sugerir Categoria: %v", got)
	}
	// Sin distinguir mayúsculas; los homónimos se agrupan.
	cs := ix.Complete("user", nil, 20)
	got = names(cs)
	for _, want := range []string{"User", "UserController", "UserService"} {
		if !slices.Contains(got, want) {
			t.Errorf("user: falta %s en %v", want, got)
		}
	}
	for _, c := range cs {
		if c.Name == "User" && c.Count != 2 {
			t.Errorf("User tiene 2 declaraciones: %+v", c)
		}
	}
	// Métodos y propiedades del proyecto.
	if got := names(ix.Complete("agreg", nil, 5)); !slices.Equal(got, []string{"agregar"}) {
		t.Errorf("agreg: %v", got)
	}
	// Lo del mismo archivo y package va primero.
	mig := curFile(t, "src/main/java/com/acme/legacy/Migracion.java")
	cs = ix.Complete("U", mig, 20)
	if len(cs) == 0 || cs[0].Name != "User" || cs[0].Package != "com.acme.legacy" {
		t.Errorf("U desde legacy: %+v", cs)
	}
	// Los nombres no repiten el prefijo exacto, no incluyen packages y el
	// límite se respeta.
	if got := names(ix.Complete("User", nil, 20)); slices.Contains(got, "User") {
		t.Errorf("User no debía sugerirse a sí mismo: %v", got)
	}
	if got := ix.Complete("U", nil, 2); len(got) != 2 {
		t.Errorf("límite: %v", got)
	}
	if ix.Complete("", nil, 5) != nil || ix.Complete("zzzz", nil, 5) != nil {
		t.Error("sin prefijo o sin coincidencias no hay nada")
	}
	// El texto actual del archivo (aún sin guardar) cuenta aunque el índice no
	// lo tenga.
	buf := Parse(LangJava, "Nueva.java", []byte("package n;\nclass Zapato { void zurcir() {} }\n"))
	if got := names(ix.Complete("z", buf, 5)); !slices.Equal(got, []string{"Zapato", "zurcir"}) {
		t.Errorf("z sobre un buffer nuevo: %v", got)
	}
}

func TestImportMap(t *testing.T) {
	ix, _ := newIndexed(t, Options{})
	m := ix.ImportMap()
	if got := m["java"]["User"]; !slices.Equal(got, []string{"com.acme.legacy.User", "com.acme.shop.model.User"}) {
		t.Errorf("User: %v", got)
	}
	if got := m["java"]["UserService"]; !slices.Equal(got, []string{"com.acme.shop.service.UserService"}) {
		t.Errorf("UserService: %v", got)
	}
	// Java y Kotlin comparten los tipos.
	if got := m["java"]["Pedido"]; !slices.Equal(got, []string{"com.acme.shop.kt.Pedido"}) {
		t.Errorf("Pedido desde Java: %v", got)
	}
	if got := m["kotlin"]["User"]; len(got) != 2 {
		t.Errorf("User desde Kotlin: %v", got)
	}
	// En C# el candidato es el namespace.
	if got := m["csharp"]["Producto"]; !slices.Equal(got, []string{"Acme.Shop.Models"}) {
		t.Errorf("Producto: %v", got)
	}
	if _, ok := m["java"]["Producto"]; ok {
		t.Error("los tipos de C# no son importables desde Java")
	}
	// Ni los anidados ni los miembros.
	for _, n := range []string{"Interna", "Estado", "find", "Companion"} {
		if got, ok := m["java"][n]; ok && n != "Estado" {
			t.Errorf("%s no debía estar: %v", n, got)
		}
	}
	if got := m["java"]["Estado"]; !slices.Equal(got, []string{"com.acme.shop.kt.Estado"}) {
		t.Errorf("Estado (solo el de nivel superior de Kotlin): %v", got)
	}
}

// Medición: indexar un archivo de 5000 líneas con el índice completo.
func TestRefreshGrandeSpeed(t *testing.T) {
	ix := New(Options{Root: filepath.Join("..", "..", "testdata", "java")})
	st := ix.Refresh(context.Background())
	t.Logf("Refresh sobre testdata/java: %+v", st)
	limit := 50 * time.Millisecond
	if raceEnabled {
		limit *= 10
	}
	if st.Files != 1 || st.Took > limit {
		t.Errorf("indexar Grande.java: %+v (límite %v)", st, limit)
	}
}
