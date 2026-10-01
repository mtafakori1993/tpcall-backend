package signaling

import "sync"

// Presence is a concurrency-safe in-memory registry mapping extension -> Client.
// Only one active session per extension is kept.
type Presence struct {
	mu      sync.RWMutex
	clients map[string]*Client
}

// NewPresence returns an empty presence registry.
func NewPresence() *Presence {
	return &Presence{clients: make(map[string]*Client)}
}

// Register associates c with its extension, replacing any existing session.
// The replaced client (if any) is returned so the caller can close it.
func (p *Presence) Register(c *Client) *Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.clients[c.extension]
	p.clients[c.extension] = c
	return old
}

// Unregister removes c from the registry only if it is still the current
// session for the extension. This prevents a stale, already-replaced
// connection from removing a newer one (pointer identity comparison).
func (p *Presence) Unregister(c *Client) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients[c.extension] == c {
		delete(p.clients, c.extension)
		return true
	}
	return false
}

// Get returns the current client for an extension.
func (p *Presence) Get(extension string) (*Client, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	c, ok := p.clients[extension]
	return c, ok
}

// IsOnline reports whether the extension has a live session.
func (p *Presence) IsOnline(extension string) bool {
	_, ok := p.Get(extension)
	return ok
}

// Clients returns a snapshot of all current clients.
func (p *Presence) Clients() []*Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Client, 0, len(p.clients))
	for _, c := range p.clients {
		out = append(out, c)
	}
	return out
}
