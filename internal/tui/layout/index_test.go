package layout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dpa23/wari/assets"
	"github.com/dpa23/wari/internal/imports"
	"github.com/dpa23/wari/internal/index"
	"github.com/dpa23/wari/internal/tui/filetree"
)

const (
	ctrlRel   = "src/main/java/com/acme/shop/UserController.java"
	modelRel  = "src/main/java/com/acme/shop/model/User.java"
	legacyRel = "src/main/java/com/acme/legacy/User.java"
)

// copyProyecto copia testdata/proyecto a un directorio temporal.
func copyProyecto(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "..", "testdata", "proyecto")
	dst := t.TempDir()
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

// indexedLayout abre file (relativo a la raíz) con el índice del proyecto ya
// lleno y los mapas de imports embebidos.
func indexedLayout(t *testing.T, root, file string) Model {
	t.Helper()
	maps, err := imports.LoadDir(assets.FS, "imports")
	if err != nil {
		t.Fatal(err)
	}
	ix := index.New(index.Options{Root: root, Ignore: filetree.IgnoreFunc(root)})
	if st := ix.Refresh(context.Background()); st.Files == 0 {
		t.Fatalf("índice vacío: %+v", st)
	}
	cfg := Config{Root: root, Imports: maps, Index: ix}
	if file != "" {
		cfg.File = filepath.Join(root, filepath.FromSlash(file))
	}
	m := newLayout(t, cfg, 140, 30)
	// Lo que haría Init: la pasada de fondo trae los tipos propios.
	return feed(m, m.refreshIndex())
}

func cursorAt(m Model) (line, col int) {
	c := m.ed.Buffer().Cursor()
	return c.Line, c.Col
}

func openPath(m Model) string { return filepath.ToSlash(m.ed.Path()) }

func paletteTitles(m Model) []string {
	var out []string
	for _, idx := range m.pal.results {
		out = append(out, m.pal.items[idx].title)
	}
	return out
}

func TestOutlineListsClassesMethodsAndAnnotations(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), ctrlRel)
	m, _ = send(m, press('o', tea.ModAlt))
	if !m.pal.open || m.pal.mode != palOutline {
		t.Fatalf("alt+o debía abrir el outline: open=%v mode=%v", m.pal.open, m.pal.mode)
	}
	titles := paletteTitles(m)
	want := []string{
		`class UserController  @RestController  @RequestMapping("/users")`,
		`  field service`,
		`  ctor UserController(UserService service)`,
		`  fun find(Long id)  @GetMapping("/{id}")`,
		`  fun create(User nuevo)  @PostMapping`,
		`  enum Estado`,
		`    const ACTIVO`,
		`  class Interna`,
		`    fun incrementar()`,
	}
	for _, w := range want {
		found := false
		for _, ti := range titles {
			found = found || ti == w
		}
		if !found {
			t.Errorf("falta %q en el outline:\n  %s", w, strings.Join(titles, "\n  "))
		}
	}
	for _, ti := range titles {
		if strings.Contains(ti, "Comentado") || strings.Contains(ti, "EnCadena") || strings.Contains(ti, "enComentario") {
			t.Errorf("el outline lista algo de un comentario o cadena: %q", ti)
		}
	}
	scr := screen(m)
	if !strings.Contains(scr, "Outline: ") || !strings.Contains(scr, "@GetMapping") {
		t.Errorf("el outline no se ve en pantalla:\n%s", scr)
	}
	checkDims(t, m, 140, 30, "outline")

	// Filtrar por una anotación y saltar.
	m = typeText(m, "PostMapping")
	if got := paletteTitles(m); len(got) != 1 || !strings.Contains(got[0], "create") {
		t.Fatalf("filtrar por @PostMapping: %v", got)
	}
	m, _ = send(m, enter())
	if m.pal.open {
		t.Fatal("la paleta debía cerrarse")
	}
	if line, col := cursorAt(m); line != 30 || col != 16 {
		t.Errorf("cursor en %d:%d, quería 30:16 (create)", line, col)
	}
	if m.focus != focusEditor {
		t.Error("el foco debía quedar en el editor")
	}
}

func TestOutlineReflectsUnsavedText(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), modelRel)
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	m = typeText(m, " void sinGuardar() {}")
	m, _ = send(m, press('o', tea.ModAlt))
	found := false
	for _, ti := range paletteTitles(m) {
		found = found || strings.Contains(ti, "sinGuardar")
	}
	if !found {
		t.Errorf("el outline debía reflejar el texto sin guardar: %v", paletteTitles(m))
	}
}

func TestOutlineStartsOnSymbolAtCursor(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), ctrlRel)
	m.ed.GoTo(30, 0) // dentro de create
	m, _ = send(m, press('o', tea.ModAlt))
	sel := m.pal.items[m.pal.results[m.pal.sel]].title
	if !strings.Contains(sel, "create") {
		t.Errorf("el outline debía empezar en el símbolo del cursor, empieza en %q", sel)
	}
}

func TestGotoDefinitionAcrossFiles(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), ctrlRel)
	m.ed.GoTo(25, 11) // `User` en "public User find(Long id)"
	if w, _, _ := m.ed.WordAtCursor(); w != "User" {
		t.Fatalf("palabra bajo el cursor: %q", w)
	}
	m, _ = send(m, press(tea.KeyF12, 0))
	if m.pal.open {
		t.Fatalf("UserController importa model.User: no debía haber selector (%v)", paletteTitles(m))
	}
	if !strings.HasSuffix(openPath(m), modelRel) {
		t.Fatalf("saltó a %s, quería %s", openPath(m), modelRel)
	}
	if line, col := cursorAt(m); line != 2 || col != 13 {
		t.Errorf("cursor en %d:%d, quería la clase en 2:13", line, col)
	}
	if !strings.Contains(screen(m), "model/User.java") {
		t.Errorf("el título debía mostrar el archivo nuevo")
	}
	checkDims(t, m, 140, 30, "goto")

	// Desde el package legacy gana la clase de su propio package.
	m = indexedLayout(t, copyProyecto(t), "src/main/java/com/acme/legacy/Migracion.java")
	m.ed.GoTo(3, 4)
	m, _ = send(m, press('d', tea.ModAlt))
	if !strings.HasSuffix(openPath(m), legacyRel) {
		t.Errorf("saltó a %s, quería %s", openPath(m), legacyRel)
	}

	// Entre lenguajes: UserService (import con comodín) y Pedido (Kotlin).
	m = indexedLayout(t, copyProyecto(t), ctrlRel)
	m.ed.GoTo(17, 20) // UserService en el campo
	m, _ = send(m, press(tea.KeyF12, 0))
	if !strings.HasSuffix(openPath(m), "service/UserService.java") {
		t.Errorf("UserService: %s", openPath(m))
	}
	m = indexedLayout(t, copyProyecto(t), "src/Acme/Shop/Controllers/ProductosController.cs")
	// El campo "List<Producto>" está en la línea 11 (base 0).
	line := m.ed.Buffer().Line(11)
	col := strings.Index(string(line), "Producto>")
	m.ed.GoTo(11, col+2)
	m, _ = send(m, press(tea.KeyF12, 0))
	if !strings.HasSuffix(openPath(m), "Models/Producto.cs") {
		t.Errorf("Producto: %s", openPath(m))
	}
}

func TestGotoDefinitionAmbiguousUsesSelector(t *testing.T) {
	root := copyProyecto(t)
	write(t, root, "src/main/java/com/acme/otro/Uso.java", "package com.acme.otro;\n\nclass Uso {\n    User usuario;\n}\n")
	m := indexedLayout(t, root, "src/main/java/com/acme/otro/Uso.java")
	m.ed.GoTo(3, 5)
	m, _ = send(m, press(tea.KeyF12, 0))
	if !m.pal.open || m.pal.mode != palDefs {
		t.Fatalf("con dos candidatos debía abrir el selector (open=%v mode=%v)", m.pal.open, m.pal.mode)
	}
	titles := paletteTitles(m)
	if len(titles) != 2 {
		t.Fatalf("candidatos: %v", titles)
	}
	scr := screen(m)
	if !strings.Contains(scr, "legacy/User.java:4") || !strings.Contains(scr, "model/User.java:3") {
		t.Errorf("el selector debía mostrar los dos archivos:\n%s", scr)
	}
	m, _ = send(m, press(tea.KeyDown, 0), enter())
	if m.pal.open || !strings.HasSuffix(openPath(m), modelRel) {
		t.Errorf("elegir el segundo debía abrir model/User.java: %s", openPath(m))
	}
}

func TestGotoDefinitionWithoutIndexUsesTheFile(t *testing.T) {
	root := copyProyecto(t)
	m := newLayout(t, Config{Root: root, File: filepath.Join(root, ctrlRel)}, 140, 30)
	m.ed.GoTo(26, 17) // `service` en "return service.buscar(id)"
	m, _ = send(m, press(tea.KeyF12, 0))
	if !strings.HasSuffix(openPath(m), ctrlRel) {
		t.Fatalf("debía quedarse en el mismo archivo: %s", openPath(m))
	}
	if line, col := cursorAt(m); line != 17 || col != 30 {
		t.Errorf("cursor en %d:%d, quería el campo en 17:30", line, col)
	}
	// Un identificador que no está en ningún sitio.
	m.ed.GoTo(26, 24) // `buscar`: está en otro archivo que nadie indexó
	m, _ = send(m, press(tea.KeyF12, 0))
	if !strings.Contains(m.msg, "no se encontró la definición de buscar") {
		t.Errorf("msg = %q", m.msg)
	}
}

func TestGotoDefinitionRespectsUnsavedChanges(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), ctrlRel)
	m.ed.GoTo(25, 11)
	m, _ = send(m, press(tea.KeyEnd, 0))
	m = typeText(m, " ")
	if !m.ed.Dirty() {
		t.Fatal("el buffer debía estar sucio")
	}
	m.ed.GoTo(25, 11)
	m, _ = send(m, press(tea.KeyF12, 0))
	if !strings.HasSuffix(openPath(m), ctrlRel) || !strings.Contains(m.msg, "cambios sin guardar") {
		t.Fatalf("con cambios sin guardar debía avisar y no abrir: %s / %q", openPath(m), m.msg)
	}
	m, _ = send(m, press(tea.KeyF12, 0))
	if !strings.HasSuffix(openPath(m), modelRel) {
		t.Errorf("repetir debía abrir el destino: %s", openPath(m))
	}
}

func TestCompleteProjectNames(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), "src/Acme/Shop/Controllers/ProductosController.cs")
	// Una línea nueva dentro de la clase.
	m.ed.GoTo(11, 0)
	m, _ = send(m, press(tea.KeyHome, 0))
	m = typeText(m, "Produ")
	m, _ = send(m, press('/', tea.ModAlt))
	if !m.pal.open || m.pal.mode != palComplete {
		t.Fatalf("alt+/ debía abrir la lista (open=%v mode=%v msg=%q)", m.pal.open, m.pal.mode, m.msg)
	}
	titles := paletteTitles(m)
	if len(titles) < 2 || titles[0] != "ProductosController" || titles[1] != "Producto" {
		t.Fatalf("sugerencias: %v", titles)
	}
	if !strings.Contains(screen(m), "Completar Produ: ") {
		t.Errorf("la lista no se ve:\n%s", screen(m))
	}
	checkDims(t, m, 140, 30, "completar")
	m, _ = send(m, press(tea.KeyDown, 0), enter())
	if m.pal.open {
		t.Fatal("la lista debía cerrarse")
	}
	if got := string(m.ed.Buffer().Line(11)); !strings.HasPrefix(got, "Producto        private readonly") {
		t.Errorf("línea tras completar: %q", got)
	}
	// Deshacer lo revierte de una vez.
	m, _ = send(m, press('z', tea.ModCtrl))
	if got := string(m.ed.Buffer().Line(11)); !strings.HasPrefix(got, "Produ") || strings.HasPrefix(got, "Producto") {
		t.Errorf("tras deshacer: %q", got)
	}

	// Un solo candidato se inserta sin lista.
	m, _ = send(m, press(tea.KeyHome, 0))
	m = typeText(m, "Categ")
	m, _ = send(m, press(tea.KeySpace, tea.ModCtrl))
	if m.pal.open {
		t.Fatalf("con un solo candidato no debía abrir la lista: %v", paletteTitles(m))
	}
	if got := string(m.ed.Buffer().Line(11)); !strings.HasPrefix(got, "Categoria") {
		t.Errorf("línea tras completar: %q", got)
	}
	// Sin prefijo o sin sugerencias solo avisa.
	m, _ = send(m, press(tea.KeyHome, 0), press('/', tea.ModAlt))
	if m.pal.open || !strings.Contains(m.msg, "al menos una letra") {
		t.Errorf("sin prefijo: msg=%q open=%v", m.msg, m.pal.open)
	}
}

func TestProjectTypesFeedImports(t *testing.T) {
	root := copyProyecto(t)
	write(t, root, "src/main/java/com/acme/otro/Uso.java", "package com.acme.otro;\n\nclass Uso {\n    Pedido p;\n    UserService s;\n}\n")
	m := indexedLayout(t, root, "src/main/java/com/acme/otro/Uso.java")

	// Pedido es una clase de Kotlin del proyecto: la sugerencia y alt+i la
	// conocen.
	m.ed.GoTo(3, 5)
	m, _ = send(m, press(tea.KeyEnd, 0)) // actualiza la sugerencia
	m.ed.GoTo(3, 5)
	m, _ = send(m, press(tea.KeyRight, 0), press(tea.KeyLeft, 0))
	if !strings.Contains(screen(m), "importar Pedido") {
		t.Fatalf("la línea de estado debía sugerir importar Pedido:\n%s", screen(m))
	}
	m, _ = send(m, press('i', tea.ModAlt))
	if m.pal.open {
		t.Fatalf("Pedido tiene un solo candidato: no debía haber selector")
	}
	got := m.ed.Buffer().String()
	if !strings.Contains(got, "import com.acme.shop.kt.Pedido;") {
		t.Fatalf("falta el import de la clase propia:\n%s", got)
	}

	// UserService (Java del proyecto) también.
	for i := 0; i < m.ed.Buffer().LineCount(); i++ {
		if strings.Contains(string(m.ed.Buffer().Line(i)), "UserService") {
			m.ed.GoTo(i, 6)
		}
	}
	m, _ = send(m, press(tea.KeyRight, 0), press(tea.KeyLeft, 0), press('i', tea.ModAlt))
	if got := m.ed.Buffer().String(); !strings.Contains(got, "import com.acme.shop.service.UserService;") {
		t.Fatalf("falta el import de UserService:\n%s", got)
	}
	// Los imports ordenados quedan juntos y sin duplicar.
	m, _ = send(m, press('i', tea.ModAlt))
	if !strings.Contains(m.msg, "ya está importado") {
		t.Errorf("msg = %q", m.msg)
	}
}

func TestProjectTypeWithTwoCandidatesAsksWhich(t *testing.T) {
	root := copyProyecto(t)
	write(t, root, "src/main/java/com/acme/otro/Uso.java", "package com.acme.otro;\n\nclass Uso {\n    User u;\n}\n")
	m := indexedLayout(t, root, "src/main/java/com/acme/otro/Uso.java")
	m.ed.GoTo(3, 5)
	m, _ = send(m, press(tea.KeyRight, 0), press(tea.KeyLeft, 0), press('i', tea.ModAlt))
	if !m.pal.open || m.pal.mode != palImports {
		t.Fatalf("User existe en dos packages: debía abrir el selector (open=%v)", m.pal.open)
	}
	titles := paletteTitles(m)
	if len(titles) != 2 || titles[0] != "com.acme.legacy.User" || titles[1] != "com.acme.shop.model.User" {
		t.Errorf("candidatos: %v", titles)
	}
}

func TestIndexKeysAndPalette(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), ctrlRel)
	m, _ = send(m, press('p', tea.ModCtrl))
	m = typeText(m, "outline")
	if got := paletteTitles(m); len(got) == 0 || !strings.Contains(got[0], "Outline") {
		t.Fatalf("la paleta debía ofrecer el outline: %v", got)
	}
	m, _ = send(m, enter())
	if !m.pal.open || m.pal.mode != palOutline {
		t.Fatalf("elegir Outline en la paleta debía abrirlo (open=%v mode=%v)", m.pal.open, m.pal.mode)
	}
	m, _ = send(m, press(tea.KeyEscape, 0))

	// Las demás acciones también están en la paleta y en la ayuda.
	m, _ = send(m, press('p', tea.ModCtrl))
	m = typeText(m, "definici")
	if got := paletteTitles(m); len(got) == 0 || !strings.Contains(got[0], "definición") {
		t.Errorf("paleta: %v", got)
	}
	m, _ = send(m, press(tea.KeyEscape, 0))
	m, _ = send(m, press(tea.KeyF1, 0))
	help := screen(m)
	if !strings.Contains(help, "alt+o") || !strings.Contains(help, "f12, alt+d") {
		t.Errorf("la ayuda debía listar las teclas nuevas:\n%s", help)
	}
}

func TestIndexKeysBelongToChatWhenItHasFocus(t *testing.T) {
	m := indexedLayout(t, copyProyecto(t), ctrlRel)
	m = m.focusPanel(focusChat)
	m, _ = send(m, press('o', tea.ModAlt), press('d', tea.ModAlt))
	if m.pal.open || m.focus != focusChat {
		t.Error("con el foco en el chat, alt+o y alt+d son del chat (alt+d borra una palabra)")
	}
}

func TestIndexActionsWithoutFile(t *testing.T) {
	m := newLayout(t, Config{}, 100, 24)
	for _, k := range []tea.KeyPressMsg{press('o', tea.ModAlt), press(tea.KeyF12, 0), press('/', tea.ModAlt)} {
		m, _ = send(m, k)
		if m.pal.open || !strings.Contains(m.msg, "no hay archivo abierto") {
			t.Errorf("%v sin archivo: msg=%q", k, m.msg)
		}
	}
	// Un archivo de otro tipo.
	root := t.TempDir()
	m = newLayout(t, Config{Root: root, File: write(t, root, "notas.txt", "hola\n")}, 100, 24)
	m, _ = send(m, press('o', tea.ModAlt))
	if m.pal.open || !strings.Contains(m.msg, "Java, Kotlin y C#") {
		t.Errorf("txt: msg=%q", m.msg)
	}
}

func TestSaveRefreshesIndexInBackground(t *testing.T) {
	root := copyProyecto(t)
	m := indexedLayout(t, root, modelRel)
	if len(m.ix.Resolve("Renombrado", nil, -1, 0)) != 0 {
		t.Fatal("precondición")
	}
	m, _ = send(m, press(tea.KeyDown, 0), press(tea.KeyEnd, 0))
	m = typeText(m, " class Renombrado {}")
	m, cmd := send(m, press('s', tea.ModCtrl))
	// El guardado devuelve su Cmd; el resultado dispara la pasada de fondo.
	msg := cmd()
	var next tea.Cmd
	m, next = send(m, msg)
	if next == nil {
		t.Fatal("tras guardar debía lanzarse una pasada del índice")
	}
	m = feed(m, next)
	if got := m.ix.Resolve("Renombrado", nil, -1, 0); len(got) != 1 {
		t.Errorf("tras guardar el índice debía conocer la clase nueva: %v", got)
	}
}
