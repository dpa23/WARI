package tile

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestJSONRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 100; i++ {
		tr := randomTree(rng, 1+rng.Intn(15))
		ids := tr.IDs()
		must(t, tr.SetFocus(ids[rng.Intn(len(ids))]))
		if i%3 == 0 {
			must(t, tr.Zoom(ids[rng.Intn(len(ids))]))
		}
		data, err := json.Marshal(tr)
		if err != nil {
			t.Fatal(err)
		}
		var back Tree
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("%v\n%s", err, data)
		}
		checkParents(t, &back)
		if !reflect.DeepEqual(back.IDs(), ids) || back.Focus() != tr.Focus() || back.Zoomed() != tr.Zoomed() {
			t.Fatalf("estado distinto: %s", data)
		}
		for _, s := range [][2]int{{80, 24}, {200, 60}, {7, 3}} {
			if !reflect.DeepEqual(noHandle(back.Layout(s[0], s[1])), noHandle(tr.Layout(s[0], s[1]))) {
				t.Fatalf("layout distinto en %v: %s", s, data)
			}
		}
	}
}

func TestJSONShape(t *testing.T) {
	tr := New(3)
	must(t, tr.Split(3, 4, Rows, true, 0))
	data, _ := json.Marshal(tr)
	want := `{"focus":3,"root":{"axis":"rows","ratio":0.5,"a":{"id":3},"b":{"id":4}}}`
	if string(data) != want {
		t.Fatalf("%s", data)
	}
	var z Tree
	if _, err := z.MarshalJSON(); err == nil {
		t.Fatal("el valor cero no se serializa")
	}
}

func TestJSONHostile(t *testing.T) {
	leaf := func(id int) string { return `{"id":` + string(rune('0'+id)) + `}` }
	pair := func(extra string) string {
		return `{"axis":"cols","ratio":0.5,"a":` + leaf(1) + `,"b":` + leaf(2) + `}` + extra
	}
	deepOK := func(n int) string { // cadena de n divisiones con IDs únicos
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(`{"axis":"rows","ratio":0.5,"b":{"id":` + itoa(1000+i) + `},"a":`)
		}
		b.WriteString(`{"id":1}`)
		b.WriteString(strings.Repeat("}", n))
		return b.String()
	}
	many := func(n int) string { // n hojas en un peine de 8 niveles de ancho
		ids := make([]string, n)
		for i := range ids {
			ids[i] = itoa(i + 1)
		}
		return comb(ids)
	}
	bad := map[string]string{
		"vacío":           `{}`,
		"no es json":      `{"root":`,
		"ratio >1":        `{"root":{"axis":"cols","ratio":1.5,"a":{"id":1},"b":{"id":2}}}`,
		"ratio 0":         `{"root":{"axis":"cols","ratio":0,"a":{"id":1},"b":{"id":2}}}`,
		"ratio negativo":  `{"root":{"axis":"cols","ratio":-0.2,"a":{"id":1},"b":{"id":2}}}`,
		"ratio NaN":       `{"root":{"axis":"cols","ratio":NaN,"a":{"id":1},"b":{"id":2}}}`,
		"ratio texto":     `{"root":{"axis":"cols","ratio":"x","a":{"id":1},"b":{"id":2}}}`,
		"id repetido":     `{"root":{"axis":"cols","ratio":0.5,"a":{"id":1},"b":{"id":1}}}`,
		"id cero":         `{"root":{"id":0}}`,
		"id negativo":     `{"root":{"id":-4}}`,
		"un solo hijo":    `{"root":{"axis":"cols","ratio":0.5,"a":{"id":1}}}`,
		"eje inválido":    `{"root":{"axis":"diag","ratio":0.5,"a":{"id":1},"b":{"id":2}}}`,
		"hoja con eje":    `{"root":{"id":1,"axis":"cols"}}`,
		"división con id": `{"root":{"id":9,"axis":"cols","ratio":0.5,"a":{"id":1},"b":{"id":2}}}`,
		"foco ausente":    `{"focus":7,"root":` + pair("") + `}`,
		"zoom ausente":    `{"zoom":7,"root":` + pair("") + `}`,
		"profundidad 40":  `{"root":` + deepOK(40) + `}`,
		"profundidad 33":  `{"root":` + deepOK(33) + `}`,
		"257 hojas":       `{"root":` + many(257) + `}`,
		"raíz nula":       `{"root":null}`,
		"hijo nulo":       `{"root":{"axis":"cols","ratio":0.5,"a":null,"b":{"id":1}}}`,
	}
	for name, s := range bad {
		tr := New(5)
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: pánico %v", name, r)
				}
			}()
			err = json.Unmarshal([]byte(s), tr)
		}()
		if err == nil {
			t.Errorf("%s: debía fallar", name)
		}
		if tr.Len() != 1 || !tr.Has(5) || tr.Focus() != 5 {
			t.Errorf("%s: el árbol quedó a medias", name)
		}
	}
	for name, s := range map[string]string{"profundidad 31": deepOK(31), "256 hojas": many(256)} {
		var tr Tree
		if err := json.Unmarshal([]byte(`{"root":`+s+`}`), &tr); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// comb arma un árbol equilibrado (profundidad log2 n) con los ids dados.
func comb(ids []string) string {
	if len(ids) == 1 {
		return `{"id":` + ids[0] + `}`
	}
	k := len(ids) / 2
	return `{"axis":"cols","ratio":0.5,"a":` + comb(ids[:k]) + `,"b":` + comb(ids[k:]) + `}`
}

func FuzzUnmarshal(f *testing.F) {
	f.Add(`{"focus":1,"root":{"axis":"cols","ratio":0.5,"a":{"id":1},"b":{"id":2}}}`)
	f.Add(`{"root":{"id":3}}`)
	f.Add(`{"root":{"axis":"rows","ratio":2,"a":{},"b":{}}}`)
	f.Fuzz(func(t *testing.T, s string) {
		var tr Tree
		if err := json.Unmarshal([]byte(s), &tr); err != nil {
			return
		}
		tr.Layout(80, 24)
		if _, err := json.Marshal(&tr); err != nil {
			t.Fatal(err)
		}
	})
}

// noHandle anula los Handle, que dependen de la identidad interna.
func noHandle(l Layout) Layout {
	ds := append([]Divider(nil), l.Dividers...)
	for i := range ds {
		ds[i].Handle = 0
	}
	l.Dividers = ds
	return l
}
