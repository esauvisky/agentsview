package live

import "sync"

// Registry tracks live controllers by key.
type Registry struct {
	mu sync.Mutex
	m  map[Key]*Controller
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{m: map[Key]*Controller{}}
}

// GetOrCreate returns the existing controller for key, or stores and
// returns a newly created one when absent. The second return value is
// true if an existing controller was returned.
func (r *Registry) GetOrCreate(key Key, mk func() *Controller) (*Controller, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.m[key]; ok {
		return c, true
	}
	c := mk()
	r.m[key] = c
	return c, false
}

// Get returns the existing controller for key, if any.
func (r *Registry) Get(key Key) (*Controller, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.m[key]
	return c, ok
}
