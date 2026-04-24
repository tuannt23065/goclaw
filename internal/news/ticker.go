package news

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Ticker runs RunCycle at a fixed interval. Single-flight: if a cycle is still
// running when the next tick fires, that tick is skipped.
type Ticker struct {
	monitor  *Monitor
	interval time.Duration
	mu       sync.Mutex
	running  bool
}

func NewTicker(m *Monitor, interval time.Duration) *Ticker {
	return &Ticker{monitor: m, interval: interval}
}

func (t *Ticker) Start(ctx context.Context) {
	go t.loop(ctx)
}

func (t *Ticker) loop(ctx context.Context) {
	// Run once immediately after a short startup delay so cold-start sees recent items.
	initial := time.NewTimer(30 * time.Second)
	defer initial.Stop()

	select {
	case <-ctx.Done():
		return
	case <-initial.C:
		t.runOnce(ctx)
	}

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.runOnce(ctx)
		}
	}
}

func (t *Ticker) runOnce(ctx context.Context) {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		slog.Warn("news.ticker.skip_overlap")
		return
	}
	t.running = true
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
	}()

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := t.monitor.RunCycle(runCtx); err != nil {
		slog.Error("news.ticker.cycle_err", "error", err)
	}
}
