package layout

import (
	"strings"
	"unicode"
)

// fuzzyScore indica si query es una subsecuencia de target (sin distinguir
// mayúsculas) y qué tan buena es la coincidencia: más es mejor. Premia los
// caracteres consecutivos, los que empiezan una palabra y los del nombre de
// archivo (después de la última "/"), y penaliza los destinos largos.
func fuzzyScore(query, target string) (int, bool) {
	q := fold(query)
	if len(q) == 0 {
		return 0, true
	}
	t := fold(target)
	base := 0
	for i, r := range t {
		if r == '/' {
			base = i + 1
		}
	}
	score, qi, prev := 0, 0, -2
	for i, r := range t {
		if qi >= len(q) {
			break
		}
		if r != q[qi] {
			continue
		}
		s := 10
		if prev == i-1 {
			s += 15
		}
		if i == 0 || isSep(t[i-1]) {
			s += 12
		}
		if i >= base {
			s += 6
		}
		score += s
		prev = i
		qi++
	}
	if qi < len(q) {
		return 0, false
	}
	return score - len(t)/4, true
}

// fold pasa s a minúsculas y quita los acentos del español, para que "arbol"
// encuentre "árbol".
func fold(s string) []rune {
	rs := []rune(strings.ToLower(s))
	for i, r := range rs {
		switch r {
		case 'á':
			rs[i] = 'a'
		case 'é':
			rs[i] = 'e'
		case 'í':
			rs[i] = 'i'
		case 'ó':
			rs[i] = 'o'
		case 'ú', 'ü':
			rs[i] = 'u'
		}
	}
	return rs
}

func isSep(r rune) bool {
	return r == '/' || r == '.' || r == '_' || r == '-' || unicode.IsSpace(r)
}
