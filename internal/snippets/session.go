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
// reajusta todos los rangos. Si la edición cae dentro de la parada actual
// (start >= Start y end <= End, bordes incluidos), esta crece o se encoge con
// ella: insertar justo en su borde final, o en una parada vacía, también la
// hace crecer. Los demás rangos que contienen el tramo crecen o se encogen,
// los posteriores se desplazan y ninguno queda con End < Start. Los espejos de
// la parada actual los sincroniza el editor con sus propias ediciones,
// informando cada una con EditMirror.
func (s *Session) Edit(start, end, newLen int) {
	cur := s.stops[s.cur]
	s.remap(start, end, newLen, start >= cur.Start && end <= cur.End)
}

// EditMirror informa de que el editor reemplazó el texto del espejo i de la
// parada actual por newLen runas. El espejo pasa a cubrir el texto nuevo,
// también si estaba vacío, y los demás rangos se reajustan como en Edit.
func (s *Session) EditMirror(i, newLen int) {
	mirrors := s.stops[s.cur].Mirrors
	if i < 0 || i >= len(mirrors) {
		return
	}
	r := mirrors[i]
	s.remap(r.Start, r.End, newLen, false)
	s.stops[s.cur].Mirrors[i] = Range{r.Start, r.Start + newLen}
}

// remap reajusta todos los rangos tras reemplazar [start, end) por newLen
// runas. Con inCur, la parada actual conserva su inicio y su final absorbe la
// diferencia.
func (s *Session) remap(start, end, newLen int, inCur bool) {
	delta := newLen - (end - start)

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
		if i == s.cur && inCur {
			st.End += delta
		} else {
			st.Start, st.End = mapStart(st.Start), mapEnd(st.End)
			st.End = max(st.End, st.Start)
		}
		for j, m := range st.Mirrors {
			ns, ne := mapStart(m.Start), mapEnd(m.End)
			st.Mirrors[j] = Range{ns, max(ne, ns)}
		}
	}
}
