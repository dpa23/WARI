package lsp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// DefaultInitTimeout acota el arranque de un servidor (jdtls indexa el
// proyecto en el primer inicio).
const DefaultInitTimeout = 90 * time.Second

// Manager arranca servidores bajo demanda y los reutiliza por (servidor,
// raíz). Nada se lanza hasta el primer Get.
type Manager struct {
	ctx         context.Context
	launch      Launcher
	opts        Options
	InitTimeout time.Duration // 0 usa DefaultInitTimeout

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	def    ServerDef
	root   string
	ready  chan struct{} // se cierra al terminar el arranque
	client *Client
	err    error
}

// Status describe un servidor activo.
type Status struct {
	Name string
	Root string
	// RSS son los bytes residentes; 0 si no se pudo leer (ver RSSErr).
	RSS    int64
	RSSErr error
}

// NewManager crea el gestor. ctx es el de larga vida de los servidores: al
// cancelarse, proc termina sus grupos.
func NewManager(ctx context.Context, launch Launcher, opts Options) *Manager {
	return &Manager{ctx: ctx, launch: launch, opts: opts, entries: map[string]*entry{}}
}

// Get devuelve el cliente listo para (def, root), arrancándolo si hace falta.
// Un servidor caído se reinicia. Llamadas concurrentes comparten un único
// arranque. ctx acota la espera, no la vida del servidor.
func (m *Manager) Get(ctx context.Context, def ServerDef, root string) (*Client, error) {
	key := def.Name + "\x00" + root
	for {
		m.mu.Lock()
		e := m.entries[key]
		if e != nil {
			select {
			case <-e.ready:
				if e.err != nil || e.client.Err() != nil {
					if m.entries[key] == e {
						delete(m.entries, key)
					}
					if e.err == nil {
						go e.client.Close() // limpia el proceso de un servidor caído
					}
					m.mu.Unlock()
					continue
				}
				m.mu.Unlock()
				return e.client, nil
			default:
			}
			m.mu.Unlock()
			select {
			case <-e.ready:
				if e.err != nil {
					return nil, e.err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		e = &entry{def: def, root: root, ready: make(chan struct{})}
		m.entries[key] = e
		m.mu.Unlock()

		e.client, e.err = m.start(ctx, def, root)
		if e.err != nil {
			m.mu.Lock()
			if m.entries[key] == e {
				delete(m.entries, key)
			}
			m.mu.Unlock()
		}
		close(e.ready)
		return e.client, e.err
	}
}

func (m *Manager) start(ctx context.Context, def ServerDef, root string) (*Client, error) {
	path, err := def.Detect()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", def.Name, err)
	}
	p, err := m.launch(m.ctx, path, def.CommandArgs(root), root)
	if err != nil {
		return nil, fmt.Errorf("lsp: lanzar %s: %w", def.Name, err)
	}
	c := NewProcessClient(p, m.opts)
	timeout := m.InitTimeout
	if timeout <= 0 {
		timeout = DefaultInitTimeout
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.Initialize(ictx, FileURI(root)); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("lsp: iniciar %s: %w", def.Name, err)
	}
	return c, nil
}

// Status lista los servidores activos con su consumo de memoria, ordenados
// por nombre y raíz.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	var live []*entry
	for _, e := range m.entries {
		select {
		case <-e.ready:
			if e.err == nil && e.client.Err() == nil {
				live = append(live, e)
			}
		default:
		}
	}
	m.mu.Unlock()
	out := make([]Status, 0, len(live))
	for _, e := range live {
		rss, err := e.client.MemoryRSS()
		out = append(out, Status{Name: e.def.Name, Root: e.root, RSS: rss, RSSErr: err})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Root < out[j].Root
	})
	return out
}

// Shutdown cierra todos los servidores (en paralelo) y devuelve el primer
// error.
func (m *Manager) Shutdown() error {
	m.mu.Lock()
	entries := m.entries
	m.entries = map[string]*entry{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for _, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-e.ready
			if e.err != nil {
				return
			}
			if err := e.client.Close(); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}
