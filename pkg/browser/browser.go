package browser

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// Manager handles the Chrome browser lifecycle and page management.
type Manager struct {
	mu          sync.Mutex
	browser     *rod.Browser
	launcher    *launcher.Launcher // retained for PID-based cleanup on crash
	refs        *RefStore
	pages       map[string]*rod.Page        // targetID → page
	console     map[string][]ConsoleMessage // targetID → console messages
	tenantCtxs  map[string]*rod.Browser     // tenantID → incognito browser context
	pageTenants map[string]string           // targetID → tenantID (for filtering)
	pageLastUsed map[string]time.Time       // targetID → last access time
	headless      bool
	remoteURL     string        // CDP endpoint for remote Chrome (sidecar); skips local launcher
	actionTimeout time.Duration // per-action context timeout (default 30s)
	idleTimeout   time.Duration // auto-close pages idle longer than this (default 10m, 0=disabled)
	maxPages      int           // max open pages per tenant (default 5)
	stopReaper    chan struct{} // signal to stop the reaper goroutine
	logger        *slog.Logger
}

// Option configures a Manager.
type Option func(*Manager)

// WithHeadless sets headless mode (default false).
func WithHeadless(h bool) Option {
	return func(m *Manager) { m.headless = h }
}

// WithRemoteURL sets a remote CDP endpoint (e.g. "ws://chrome:9222").
// When set, Start() connects to the remote Chrome instead of launching locally.
func WithRemoteURL(url string) Option {
	return func(m *Manager) { m.remoteURL = url }
}

// WithLogger sets a custom logger.
func WithLogger(l *slog.Logger) Option {
	return func(m *Manager) { m.logger = l }
}

// WithActionTimeout sets the per-action context timeout.
func WithActionTimeout(d time.Duration) Option {
	return func(m *Manager) { m.actionTimeout = d }
}

// WithIdleTimeout sets the idle page auto-close timeout. 0 disables the reaper.
func WithIdleTimeout(d time.Duration) Option {
	return func(m *Manager) { m.idleTimeout = d }
}

// WithMaxPages sets the max open pages per tenant.
func WithMaxPages(n int) Option {
	return func(m *Manager) { m.maxPages = n }
}

// New creates a Manager with options.
func New(opts ...Option) *Manager {
	m := &Manager{
		refs:          NewRefStore(),
		pages:         make(map[string]*rod.Page),
		console:       make(map[string][]ConsoleMessage),
		tenantCtxs:    make(map[string]*rod.Browser),
		pageTenants:   make(map[string]string),
		pageLastUsed:  make(map[string]time.Time),
		actionTimeout: 30 * time.Second,
		idleTimeout:   10 * time.Minute,
		maxPages:      5,
		logger:        slog.Default(),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// ActionTimeout returns the configured per-action timeout.
func (m *Manager) ActionTimeout() time.Duration {
	return m.actionTimeout
}

// touchPageLocked updates the last-used timestamp for a page. Must be called with mu held.
func (m *Manager) touchPageLocked(targetID string) {
	m.pageLastUsed[targetID] = time.Now()
}

// healthCheckTimeout is the maximum time to wait for a browser health check
// (Pages() call) before considering the connection dead. This prevents a stale
// WebSocket from holding the mutex indefinitely and blocking all browser operations.
const healthCheckTimeout = 5 * time.Second

// Start launches a local Chrome browser or connects to a remote one.
// If already connected but the connection is dead, it reconnects automatically.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// If browser exists, check if connection is still alive (with timeout to
	// prevent hanging on stale WebSocket connections that block the mutex).
	if m.browser != nil {
		if m.healthCheckLocked() {
			return nil // already connected and healthy
		}
		// Connection dead or health check timed out — clean up and reconnect
		m.logger.Info("browser connection lost, reconnecting")
		m.resetStateLocked()
	}

	var controlURL string

	if m.remoteURL != "" {
		// Remote Chrome sidecar — query /json/version and fix host for Docker networking
		u, err := resolveRemoteCDP(m.remoteURL)
		if err != nil {
			return fmt.Errorf("resolve remote Chrome at %s: %w", m.remoteURL, err)
		}
		controlURL = u
		m.logger.Info("connecting to remote Chrome", "cdp", controlURL, "remote", m.remoteURL)
	} else {
		// Local Chrome — launch via rod launcher with stability flags
		launchCtx, launchCancel := context.WithTimeout(ctx, 30*time.Second)
		defer launchCancel()

		l := launcher.New().
			Context(launchCtx).
			Leakless(true).
			Headless(m.headless).
			Set("disable-gpu").
			Set("no-first-run").
			Set("no-default-browser-check").
			Set("disable-dev-shm-usage").
			Set("disable-software-rasterizer").
			Set("disable-extensions").
			Set("disable-background-networking").
			Set("disable-renderer-backgrounding").
			Set("disable-background-timer-throttling").
			Set("disable-backgrounding-occluded-windows")

		u, err := l.Launch()
		if err != nil {
			return fmt.Errorf("launch Chrome: %w", err)
		}
		controlURL = u
		m.launcher = l
		m.logger.Info("Chrome launched", "cdp", controlURL, "headless", m.headless, "pid", l.PID())
	}

	connectCtx, connectCancel := context.WithTimeout(ctx, 15*time.Second)
	defer connectCancel()

	b := rod.New().Context(connectCtx).ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		// If local launch succeeded but connect failed, kill the orphan process
		if m.launcher != nil {
			m.launcher.Kill()
			m.launcher.Cleanup()
			m.launcher = nil
		}
		return fmt.Errorf("connect to Chrome: %w", err)
	}

	m.browser = b

	// Start idle-page reaper if configured
	if m.idleTimeout > 0 && m.stopReaper == nil {
		m.stopReaper = make(chan struct{})
		go m.runReaper()
	}

	return nil
}

// healthCheckLocked checks if the browser connection is still alive.
// Returns true if healthy, false if dead or timed out.
// Must be called with mu held.
func (m *Manager) healthCheckLocked() bool {
	done := make(chan error, 1)
	go func() {
		_, err := m.browser.Pages()
		done <- err
	}()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(healthCheckTimeout):
		m.logger.Warn("browser health check timed out, treating as dead")
		return false
	}
}

// pagesWithTimeout calls m.browser.Pages() with a timeout guard.
// Returns an error if the call hangs longer than healthCheckTimeout.
// Must be called with mu held (caller already holds lock).
func (m *Manager) pagesWithTimeout() (rod.Pages, error) {
	type result struct {
		pages rod.Pages
		err   error
	}
	done := make(chan result, 1)
	go func() {
		p, err := m.browser.Pages()
		done <- result{p, err}
	}()
	select {
	case r := <-done:
		return r.pages, r.err
	case <-time.After(healthCheckTimeout):
		return nil, fmt.Errorf("browser.Pages() timed out after %s", healthCheckTimeout)
	}
}

// resetStateLocked cleans up all browser state for reconnection.
// Must be called with mu held.
func (m *Manager) resetStateLocked() {
	m.closeTenantContextsLocked()
	m.browser = nil
	m.pages = make(map[string]*rod.Page)
	m.console = make(map[string][]ConsoleMessage)
	m.pageTenants = make(map[string]string)
	m.refs = NewRefStore()
}

// Stop closes the Chrome browser (local) or disconnects (remote sidecar).
func (m *Manager) Stop(ctx context.Context) error {
	// Grab and nil-out stopReaper under the lock, then close outside to avoid
	// deadlock (reaper goroutine also acquires mu).
	m.mu.Lock()
	ch := m.stopReaper
	m.stopReaper = nil
	m.mu.Unlock()
	if ch != nil {
		close(ch)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.browser == nil {
		return nil
	}

	m.closeTenantContextsLocked()

	var err error
	if m.remoteURL == "" {
		// Local Chrome — close the browser process
		err = m.browser.Close()
		// Force-kill via launcher if retained
		if m.launcher != nil {
			m.launcher.Kill()
			m.launcher.Cleanup()
			m.launcher = nil
		}
	}
	// Remote Chrome — just drop the connection; sidecar stays alive

	m.browser = nil
	m.pages = make(map[string]*rod.Page)
	m.console = make(map[string][]ConsoleMessage)
	m.pageTenants = make(map[string]string)
	m.pageLastUsed = make(map[string]time.Time)
	return err
}

// closeTenantContextsLocked closes all incognito browser contexts. Must be called with mu held.
func (m *Manager) closeTenantContextsLocked() {
	for tid, ctx := range m.tenantCtxs {
		if err := ctx.Close(); err != nil {
			m.logger.Warn("failed to close tenant browser context", "tenant", tid, "error", err)
		}
	}
	m.tenantCtxs = make(map[string]*rod.Browser)
}

// cleanupDeadBrowserLocked resets all state and kills any orphan Chrome process.
// Must be called with mu held.
func (m *Manager) cleanupDeadBrowserLocked() {
	m.closeTenantContextsLocked()
	if m.launcher != nil {
		m.launcher.Kill()
		m.launcher.Cleanup()
		m.launcher = nil
	}
	m.browser = nil
	m.pages = make(map[string]*rod.Page)
	m.console = make(map[string][]ConsoleMessage)
	m.pageTenants = make(map[string]string)
	m.pageLastUsed = make(map[string]time.Time)
	m.refs = NewRefStore()
}

// MasterTenantID is the well-known master tenant UUID string.
// Pages opened without a tenant context or by the master tenant use the main browser directly.
const MasterTenantID = "0193a5b0-7000-7000-8000-000000000001"

// tenantBrowserLocked returns an isolated incognito browser context for the given tenant.
// Master tenant and empty string use the main browser (no isolation needed).
// Must be called with mu held.
func (m *Manager) tenantBrowserLocked(tenantID string) (*rod.Browser, error) {
	if m.browser == nil {
		return nil, fmt.Errorf("browser not running")
	}
	// Master tenant or no tenant: use main browser
	if tenantID == "" || tenantID == MasterTenantID {
		return m.browser, nil
	}
	// Return existing incognito context
	if ctx, ok := m.tenantCtxs[tenantID]; ok {
		return ctx, nil
	}
	// Create new incognito context for this tenant
	incognito, err := m.browser.Incognito()
	if err != nil {
		return nil, fmt.Errorf("create incognito context for tenant %s: %w", tenantID, err)
	}
	m.tenantCtxs[tenantID] = incognito
	m.logger.Info("created incognito browser context", "tenant", tenantID)
	return incognito, nil
}

// Status returns current browser status.
func (m *Manager) Status() *StatusInfo {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.browser == nil {
		return &StatusInfo{Running: false}
	}

	if !m.healthCheckLocked() {
		return &StatusInfo{Running: false}
	}

	pages, _ := m.browser.Pages()
	info := &StatusInfo{
		Running: true,
		Tabs:    len(pages),
	}
	if len(pages) > 0 {
		if pageInfo, err := pages[0].Info(); err == nil {
			info.URL = pageInfo.URL
		}
	}
	return info
}
