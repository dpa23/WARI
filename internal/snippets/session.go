package snippets

// Session recorre los tabstops de una Expansion. Los offsets son relativos al
// inicio del texto expandido; el editor suma la posición de inserción.
// No es segura para uso concurrente.
type Session struct {
	stops []Tabstop
	cur   int
}

// NewSession empieza en la primera parada ($1, o $0 si no hay otras).
func NewSession(exp Expansion) *Session {
	stops := make([]Tabstop, len(exp.Stops))
	for i, st := range exp.Stops {
		st.Mirrors = append([]Range(nil), st.Mirrors...)
		stops[i] = st
	}
	if len(stops) == 0 {
		stops = []Tabstop{{}}
	}
	return &Session{stops: stops}
}

// Current devuelve la parada actual.
func (s *Session) Current() Tabstop { return s.stops[s.cur] }

// Finished indica que la sesión llegó a $0; el editor debe cerrarla.
func (s *Session) Finished() bool { return s.cur == len(s.stops)-1 }

// Next avanza a la siguiente parada. En la última devuelve (actual, false).
func (s *Session) Next() (Tabstop, bool) {
	if s.Finished() {
		return s.stops[s.cur], false
	}
	s.cur++
	return s.stops[s.cur], true
}

// Prev retrocede a la parada anterior. En la primera devuelve (actual, false).
func (s *Session) Prev() (Tabstop, bool) {
	if s.cur == 0 {
		return s.stops[0], false
	}
	s.cur--
	return s.stops[s.cur], true
}

// Edit informa de que el editor reemplazó [start, end) por newLen runas y
// reajusta todos los rangos. Si [start, end) es el rango de la parada actual,
// este pasa a cubrir el texto nuevo; los demás rangos que lo contienen crecen
// o se encogen y los posteriores se desplazan. Los espejos de la parada actual
// los sincroniza el editor con sus propias ediciones, informando cada una.
func (s *Session) Edit(start, end, newLen int) {
	delta := newLen - (end - start)
	cur := &s.stops[s.cur]
	curMatch := cur.Start == start && cur.End == end

	mapStart := func(p int) int {
		switch {
		case p >= end:
			return p + delta
		case p > start:
			return start
		}
		return p
	}
	mapEnd := func(p int) int {
		switch {
		case p > end:
			return p + delta
		case p == end && end > start:
			return end + delta
		case p > start:
			return start + newLen
		}
		return p
	}

	for i := range s.stops {
		st := &s.stops[i]
		if i == s.cur && curMatch {
			st.End = start + newLen
		} else {
			st.Start, st.End = mapStart(st.Start), mapEnd(st.End)
		}
		for j, m := range st.Mirrors {
			st.Mirrors[j] = Range{mapStart(m.Start), mapEnd(m.End)}
		}
	}
}
