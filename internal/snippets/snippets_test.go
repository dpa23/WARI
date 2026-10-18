package snippets

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const javaJSON = `{
  "ctor": {"prefix": "ctor", "body": ["public ${1:Class}(${2}) {", "\t$0", "}"], "description": "Constructor"},
  "for": {"prefix": ["for", "fori"], "body": "for (int i = 0; i < n; i++) {}"},
  "foreach": {"prefix": "foreach", "body": ["for (${1:T} x : ${2:xs}) {", "\t$0", "}"]}
}`

func mustLoad(t *testing.T, js string) Set {
	t.Helper()
	set, err := Load(strings.NewReader(js))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func names(sns []Snippet) []string {
	var out []string
	for _, s := range sns {
		out = append(out, s.Name)
	}
	return out
}

func TestLoadAndMatch(t *testing.T) {
	set := mustLoad(t, javaJSON)
	if got := names(set.Snippets); !reflect.DeepEqual(got, []string{"ctor", "for", "foreach"}) {
		t.Fatalf("orden: %v", got)
	}
	if got := set.Snippets[1].Prefixes; !reflect.DeepEqual(got, []string{"for", "fori"}) {
		t.Fatalf("prefixes: %v", got)
	}
	if got := set.Snippets[1].Body; !reflect.DeepEqual(got, []string{"for (int i = 0; i < n; i++) {}"}) {
		t.Fatalf("body: %v", got)
	}
	cases := []struct {
		prefix string
		want   []string
	}{
		{"ctor", []string{"ctor"}},
		{"c", []string{"ctor"}},
		{"for", []string{"for", "foreach"}}, // exacto primero
		{"fo", []string{"for", "foreach"}},
		{"fori", []string{"for"}},
		{"x", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := names(set.Match(c.prefix)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Match(%q) = %v, quiero %v", c.prefix, got, c.want)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	for _, js := range []string{
		`{`,
		`{"a": {"body": "x"}}`,
		`{"a": {"prefix": "", "body": "x"}}`,
		`{"a": {"prefix": "a"}}`,
		`{"a": {"prefix": 3, "body": "x"}}`,
	} {
		if _, err := Load(strings.NewReader(js)); err == nil {
			t.Errorf("Load(%s) debería fallar", js)
		}
	}
}

func TestLoadDir(t *testing.T) {
	fsys := fstest.MapFS{
		"snippets/java.json":   {Data: []byte(javaJSON)},
		"snippets/kotlin.json": {Data: []byte(`{"fun": {"prefix": "fun", "body": "fun $1() {}"}}`)},
		"snippets/notas.txt":   {Data: []byte("ignorado")},
	}
	sets, err := LoadDir(fsys, "snippets")
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 2 || len(sets["java"].Snippets) != 3 || len(sets["kotlin"].Snippets) != 1 {
		t.Fatalf("sets: %+v", sets)
	}
	fsys["snippets/malo.json"] = &fstest.MapFile{Data: []byte(`[]`)}
	if _, err := LoadDir(fsys, "snippets"); err == nil || !strings.Contains(err.Error(), "malo.json") {
		t.Fatalf("error esperado con el archivo: %v", err)
	}
}

func TestLoadDirIgnoresSymlinkAndRejectsBigFile(t *testing.T) {
	fsys := fstest.MapFS{
		"snippets/symlink.json": &fstest.MapFile{
			Mode: fs.ModeSymlink,
			Data: []byte(`{"link": {"prefix": "link", "body": "link"}}`),
		},
	}
	sets, err := LoadDir(fsys, "snippets")
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 0 {
		t.Fatalf("debía ignorar symlinks, pero cargó: %+v", sets)
	}

	big := make([]byte, 1<<20+10)
	copy(big, `{"big": {"prefix": "big", "body": ["`)
	for i := 36; i < len(big)-5; i++ {
		big[i] = 'a'
	}
	copy(big[len(big)-5:], `"]}}`)
	fsys["snippets/big.json"] = &fstest.MapFile{Data: big}

	if _, err := LoadDir(fsys, "snippets"); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("debía fallar por superar el límite de 1 MiB, obtuve: %v", err)
	}
}

func TestExpandCtorAcceptance(t *testing.T) {
	set := mustLoad(t, javaJSON)
	sn := set.Match("ctor")[0]
	exp, err := Expand(sn, Vars{Filename: "Persona.java"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "public Class() {\n\t\n}"; exp.Text != want {
		t.Fatalf("texto %q, quiero %q", exp.Text, want)
	}
	want := []Tabstop{
		{Index: 1, Start: 7, End: 12},
		{Index: 2, Start: 13, End: 13},
		{Index: 0, Start: 18, End: 18},
	}
	if !reflect.DeepEqual(exp.Stops, want) {
		t.Fatalf("stops %+v, quiero %+v", exp.Stops, want)
	}

	s := NewSession(exp)
	if s.Current().Index != 1 || s.Finished() {
		t.Fatalf("inicio: %+v", s.Current())
	}
	var order []int
	for {
		st, ok := s.Next()
		if !ok {
			break
		}
		order = append(order, st.Index)
	}
	if !reflect.DeepEqual(order, []int{2, 0}) || !s.Finished() {
		t.Fatalf("Next: %v finished=%v", order, s.Finished())
	}
	if _, ok := s.Next(); ok {
		t.Fatal("Next en $0 no debe avanzar")
	}
	order = nil
	for {
		st, ok := s.Prev()
		if !ok {
			break
		}
		order = append(order, st.Index)
	}
	if !reflect.DeepEqual(order, []int{2, 1}) {
		t.Fatalf("Prev: %v", order)
	}
	if s.Current().Index != 1 {
		t.Fatalf("Prev en $1 no debe retroceder: %+v", s.Current())
	}
}

func TestExpandIndentAndTabs(t *testing.T) {
	sn := Snippet{Name: "x", Body: []string{"a {", "\tb;", "\t$0", "}"}}
	exp, err := Expand(sn, Vars{Tab: Spaces(4)}, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := "a {\n      b;\n      \n  }"; exp.Text != want {
		t.Fatalf("texto %q, quiero %q", exp.Text, want)
	}
	last := exp.Stops[len(exp.Stops)-1]
	if got := []rune(exp.Text)[:last.Start]; string(got) != "a {\n      b;\n      " {
		t.Fatalf("$0 en %d: %q", last.Start, string(got))
	}
}

func TestExpandImplicitFinalStop(t *testing.T) {
	exp, err := Expand(Snippet{Name: "x", Body: []string{"héllo ${1:ñu}"}}, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Tabstop{{Index: 1, Start: 6, End: 8}, {Index: 0, Start: 8, End: 8}}
	if exp.Text != "héllo ñu" || !reflect.DeepEqual(exp.Stops, want) {
		t.Fatalf("%q %+v", exp.Text, exp.Stops)
	}

	exp, _ = Expand(Snippet{Name: "x", Body: []string{"solo texto"}}, Vars{}, "")
	s := NewSession(exp)
	if !s.Finished() || s.Current().Start != 10 {
		t.Fatalf("sin stops: %+v", s.Current())
	}
}

func TestExpandMirrorsAndNesting(t *testing.T) {
	sn := Snippet{Name: "x", Body: []string{"${1:foo} = $1; ${2:a ${3:b}}$0"}}
	exp, err := Expand(sn, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "foo = foo; a b"; exp.Text != want {
		t.Fatalf("texto %q", exp.Text)
	}
	want := []Tabstop{
		{Index: 1, Start: 0, End: 3, Mirrors: []Range{{6, 9}}},
		{Index: 2, Start: 11, End: 14},
		{Index: 3, Start: 13, End: 14},
		{Index: 0, Start: 14, End: 14},
	}
	if !reflect.DeepEqual(exp.Stops, want) {
		t.Fatalf("stops %+v", exp.Stops)
	}
}

func TestExpandVariables(t *testing.T) {
	vars := Vars{Filename: "src/main/Cuenta.test.java", Extra: map[string]string{"AUTOR": "dpa"}}
	sn := Snippet{Name: "x", Body: []string{
		"$TM_FILENAME|$TM_FILENAME_BASE|${CLASS_NAME}|$AUTOR|${NOPE:def}|${AUTOR:no}|$NOPE|${NOPE2:${1:anidado}}",
	}}
	exp, err := Expand(sn, vars, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "Cuenta.test.java|Cuenta.test|Cuenta.test|dpa|def|dpa||anidado"
	if exp.Text != want {
		t.Fatalf("texto %q, quiero %q", exp.Text, want)
	}
	if exp.Stops[0].Index != 1 || exp.Stops[0].Start != len([]rune(want))-len("anidado") {
		t.Fatalf("stop anidado: %+v", exp.Stops)
	}

	// ${VAR:default} descartado no debe registrar tabstops del default.
	exp, _ = Expand(Snippet{Name: "x", Body: []string{"${AUTOR:${1:z}}"}}, vars, "")
	if exp.Text != "dpa" || len(exp.Stops) != 1 {
		t.Fatalf("%q %+v", exp.Text, exp.Stops)
	}
}

func TestExpandEscapes(t *testing.T) {
	sn := Snippet{Name: "x", Body: []string{`cost \$5 \\ \} ${1:a\}b} $ ${ \n`}}
	exp, err := Expand(sn, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := `cost $5 \ } a}b $ ${ \n`; exp.Text != want {
		t.Fatalf("texto %q, quiero %q", exp.Text, want)
	}
	if exp.Stops[0].Index != 1 || exp.Stops[0].End-exp.Stops[0].Start != 3 {
		t.Fatalf("%+v", exp.Stops)
	}
}

func TestExpandErrors(t *testing.T) {
	for _, body := range []string{"${1:sin cierre", "${1|a,b|}", "${1", "${A"} {
		if _, err := Expand(Snippet{Name: "x", Body: []string{body}}, Vars{}, ""); err == nil {
			t.Errorf("Expand(%q) debería fallar", body)
		}
	}
}

func TestSessionEdit(t *testing.T) {
	exp, err := Expand(Snippet{Name: "x", Body: []string{"${1:foo}(${2:bar}) $1 $0"}}, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// "foo(bar) foo "
	s := NewSession(exp)
	cur := s.Current()
	s.Edit(cur.Start, cur.End, 6) // foo -> 6 runas
	got := s.Current()
	if got.Start != 0 || got.End != 6 {
		t.Fatalf("actual: %+v", got)
	}
	st, _ := s.Next()
	if st.Start != 7 || st.End != 10 {
		t.Fatalf("$2 desplazado: %+v", st)
	}
	if m := s.stops[0].Mirrors[0]; m != (Range{12, 15}) {
		t.Fatalf("espejo: %+v", m)
	}
	// Insertar en un rango vacío ($0 final) y teclear en $2 vaciándolo.
	s.Edit(st.Start, st.End, 0)
	if c := s.Current(); c.Start != 7 || c.End != 7 {
		t.Fatalf("vaciado: %+v", c)
	}
	s.Edit(7, 7, 2)
	if c := s.Current(); c.Start != 7 || c.End != 9 {
		t.Fatalf("tecleado: %+v", c)
	}
	last, _ := s.Next()
	if last.Index != 0 || last.Start != 15 {
		t.Fatalf("$0: %+v", last)
	}
}

func TestSessionEditGrowsAtActiveStopBorder(t *testing.T) {
	exp, err := Expand(Snippet{Name: "x", Body: []string{"a ${1:i} b ${2:n} c $0"}}, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// "a i b n c "
	s := NewSession(exp)
	cur := s.Current()
	s.Edit(cur.Start, cur.End, 1) // se sustituye "i" por una runa: $1 = [2,3)
	s.Edit(3, 3, 1)               // se teclea al final de $1
	s.Edit(4, 4, 1)               // y otra vez
	if c := s.Current(); c.Start != 2 || c.End != 5 {
		t.Fatalf("$1 debía cubrir lo tecleado: %+v", c)
	}
	st, _ := s.Next()
	if st.Start != 8 || st.End != 9 {
		t.Fatalf("$2 desplazado: %+v", st)
	}
	// Teclear en el borde inicial también es "dentro" de la parada actual.
	s.Edit(8, 8, 2)
	if c := s.Current(); c.Start != 8 || c.End != 11 {
		t.Fatalf("borde inicial: %+v", c)
	}
	// Una edición fuera de la parada actual no la hace crecer.
	s.Prev()
	s.Edit(6, 6, 3) // texto entre $1 y $2
	if c := s.Current(); c.Start != 2 || c.End != 5 {
		t.Fatalf("$1 no debía cambiar: %+v", c)
	}
	if n := s.stops[1]; n.Start != 11 || n.End != 14 {
		t.Fatalf("$2 tras insertar antes: %+v", n)
	}
}

func TestSessionEditEmptyStopGrows(t *testing.T) {
	exp, err := Expand(Snippet{Name: "x", Body: []string{"f($1)$2$0"}}, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(exp) // $1 = [2,2), $2 = [3,3)
	s.Edit(2, 2, 1)
	if c := s.Current(); c.Start != 2 || c.End != 3 {
		t.Fatalf("$1: %+v", c)
	}
	if n := s.stops[1]; n.Start != 4 || n.End != 4 {
		t.Fatalf("$2 vacío debía desplazarse entero: %+v", n)
	}
	for _, st := range s.stops {
		if st.End < st.Start {
			t.Errorf("rango invertido: %+v", st)
		}
	}
}

func TestSessionEditMirrorEmptyMirrorNotInverted(t *testing.T) {
	exp, err := Expand(Snippet{Name: "x", Body: []string{"${1:ab}-$1-$0"}}, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// "ab-ab-": $1 = [0,2), espejo = [3,5)
	s := NewSession(exp)
	cur := s.Current()
	s.Edit(cur.Start, cur.End, 0) // se borra el texto: $1 = [0,0)
	if c := s.Current(); c.Start != 0 || c.End != 0 {
		t.Fatalf("$1 vacío: %+v", c)
	}
	s.EditMirror(0, 0) // el editor vació también el espejo
	if m := s.Current().Mirrors[0]; m != (Range{1, 1}) {
		t.Fatalf("espejo vacío: %+v", m)
	}
	// Se teclean 3 runas en $1 y el editor las copia al espejo vacío.
	s.Edit(0, 0, 3)
	s.EditMirror(0, 3)
	c, m := s.Current(), s.Current().Mirrors[0]
	if c.Start != 0 || c.End != 3 {
		t.Fatalf("$1: %+v", c)
	}
	if m != (Range{4, 7}) {
		t.Fatalf("espejo: %+v, quiero {4 7}", m)
	}
	if last, _ := s.Next(); last.Start != 8 {
		t.Fatalf("$0: %+v", last)
	}
}

func TestSessionEditMirrorShiftsOthers(t *testing.T) {
	exp, err := Expand(Snippet{Name: "x", Body: []string{"${1:i} ${2:n} $1 ${3:z} $0"}}, Vars{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// "i n i z ": $1 [0,1), $2 [2,3), espejo [4,5), $3 [6,7)
	s := NewSession(exp)
	s.Edit(0, 1, 3)
	s.EditMirror(0, 3) // tras Edit el espejo estaba en [6,7)
	if m := s.Current().Mirrors[0]; m != (Range{6, 9}) {
		t.Fatalf("espejo: %+v", m)
	}
	s.Next()
	if c := s.Current(); c.Start != 4 || c.End != 5 {
		t.Fatalf("$2: %+v", c)
	}
	s.Next()
	if c := s.Current(); c.Start != 10 || c.End != 11 {
		t.Fatalf("$3: %+v", c)
	}
}
