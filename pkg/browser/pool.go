package browser

import (
	"context"
	"log/slog"
	"sync"
)

// ManagerPool manages multiple browser.Manager instances keyed by remote CDP URL.
// It lazily creates Managers on demand and caches them for reuse.
// The fallback manager (empty URL key) is the global default.
type ManagerPool struct {
	mu       sync.Mutex
	managers map[string]*Manager // remoteURL → Manager
	defaults []Option            // shared default options for new Managers
}

// NewManagerPool creates a pool with a fallback (global) manager.
// defaultOpts are applied to any lazily created Manager.
func NewManagerPool(fallback *Manager, defaultOpts ...Option) *ManagerPool {
	p := &ManagerPool{
		managers: make(map[string]*Manager),
		defaults: defaultOpts,
	}
	if fallback != nil {
		p.managers[""] = fallback
	}
	return p
}

// Get returns a Manager for the given remote URL.
// Empty string returns the fallback (global) manager.
// Unknown URLs create a new Manager with WithRemoteURL and cached defaults.
func (p *ManagerPool) Get(remoteURL string) *Manager {
	p.mu.Lock()
	defer p.mu.Unlock()

	if m, ok := p.managers[remoteURL]; ok {
		return m
	}

	// Create new Manager for this remote URL
	opts := make([]Option, len(p.defaults))
	copy(opts, p.defaults)
	opts = append(opts, WithRemoteURL(remoteURL))

	m := New(opts...)
	p.managers[remoteURL] = m
	slog.Info("browser pool: created manager for remote URL", "url", remoteURL)
	return m
}

// Fallback returns the global default Manager (empty URL key).
func (p *ManagerPool) Fallback() *Manager {
	return p.Get("")
}

// Close stops all managed Managers.
func (p *ManagerPool) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for url, m := range p.managers {
		if err := m.Stop(ctx); err != nil {
			slog.Warn("browser pool: failed to stop manager", "url", url, "error", err)
		}
	}
	return nil
}
