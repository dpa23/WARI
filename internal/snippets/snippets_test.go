package snippets

import (
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
