package lsp

// diagSub es una suscripción a los diagnósticos de un documento.
type diagSub struct{ ch chan []Diagnostic }

// SubscribeDiagnostics devuelve un canal con los diagnósticos del documento y
// la función que cancela la suscripción (cierra el canal). El canal guarda
// solo el último valor: un consumidor lento ve la publicación más reciente,
// nunca bloquea al lector. Si ya había diagnósticos publicados se entregan
// de inmediato. El canal se cierra también cuando la conexión termina.
func (c *Client) SubscribeDiagnostics(uri string) (<-chan []Diagnostic, func()) {
	s := &diagSub{ch: make(chan []Diagnostic, 1)}
	c.mu.Lock()
	select {
	case <-c.done:
		close(s.ch)
		c.mu.Unlock()
		return s.ch, func() {}
	default:
	}
	if c.diagSubs[uri] == nil {
		c.diagSubs[uri] = map[*diagSub]struct{}{}
	}
	c.diagSubs[uri][s] = struct{}{}
	if last, ok := c.diagLast[uri]; ok {
		s.ch <- last
	}
	c.mu.Unlock()

	cancel := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if subs := c.diagSubs[uri]; subs != nil {
			if _, ok := subs[s]; ok {
				delete(subs, s)
				close(s.ch)
				if len(subs) == 0 {
					delete(c.diagSubs, uri)
				}
			}
		}
	}
	return s.ch, cancel
}

// Diagnostics devuelve los últimos diagnósticos publicados del documento.
func (c *Client) Diagnostics(uri string) []Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diagLast[uri]
}

// publish guarda y reparte una publicación. Todos los envíos a los canales
// ocurren bajo c.mu, lo que permite cerrarlos sin carreras.
func (c *Client) publish(uri string, diags []Diagnostic) {
	if diags == nil {
		diags = []Diagnostic{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diagLast[uri] = diags
	for s := range c.diagSubs[uri] {
		select {
		case s.ch <- diags:
		default:
			select { // descarta el valor viejo y deja el nuevo
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- diags:
			default:
			}
		}
	}
}
