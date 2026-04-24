package news

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Monitor orchestrates a single news-monitor cycle: fetch feeds, score items,
// dedup, and dispatch top items to the team leader.
type Monitor struct {
	store                 *Store
	fetcher               *Fetcher
	rateLimiter           *RateLimiter
	dispatcher            *Dispatcher
	heuristicThreshold    int
	maxDispatchPerCycle   int
	dedupWindow           time.Duration
}

type MonitorConfig struct {
	HeuristicThreshold  int           // min score to dispatch (default 6)
	MaxDispatchPerCycle int           // max items dispatched in one cycle (default 2)
	DedupWindow         time.Duration // title-hash lookback (default 24h)
}

func NewMonitor(s *Store, fetcher *Fetcher, rl *RateLimiter, disp *Dispatcher, cfg MonitorConfig) *Monitor {
	if cfg.HeuristicThreshold == 0 {
		cfg.HeuristicThreshold = 6
	}
	if cfg.MaxDispatchPerCycle == 0 {
		cfg.MaxDispatchPerCycle = 2
	}
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = 24 * time.Hour
	}
	return &Monitor{
		store:               s,
		fetcher:             fetcher,
		rateLimiter:         rl,
		dispatcher:          disp,
		heuristicThreshold:  cfg.HeuristicThreshold,
		maxDispatchPerCycle: cfg.MaxDispatchPerCycle,
		dedupWindow:         cfg.DedupWindow,
	}
}

// RunCycle is called by the ticker each tick. Must be safe to run concurrently
// with itself (caller should ensure single-flight).
func (m *Monitor) RunCycle(ctx context.Context) error {
	started := time.Now()
	slog.Info("news.cycle.start")

	// 1. Fetch all active feeds, insert new items.
	inserted := m.fetchAll(ctx)
	slog.Info("news.cycle.fetched", "new_items", inserted)

	// 2. Score new items heuristically.
	scored := m.scoreNewItems(ctx)
	slog.Info("news.cycle.scored", "items", scored)

	// 3. Mark duplicates within dedup window.
	deduped := m.dedupScored(ctx)
	slog.Info("news.cycle.deduped", "items", deduped)

	// 4. Dispatch eligible items subject to rate limit.
	dispatched := m.dispatchEligible(ctx)
	slog.Info("news.cycle.done", "dispatched", dispatched, "elapsed_ms", time.Since(started).Milliseconds())
	return nil
}

func (m *Monitor) fetchAll(ctx context.Context) int {
	feeds, err := m.store.ListActiveFeeds(ctx)
	if err != nil {
		slog.Error("news.cycle.list_feeds", "error", err)
		return 0
	}
	total := 0
	for _, f := range feeds {
		select {
		case <-ctx.Done():
			return total
		default:
		}

		res := m.fetcher.Fetch(ctx, f)
		if res.Err != nil {
			slog.Warn("news.fetch.err", "feed", f.SourceName, "url", f.URL, "error", res.Err)
			_ = m.store.UpdateFeedAfterPoll(ctx, f.ID, "", "", true)
			continue
		}
		if res.NotModified {
			slog.Debug("news.fetch.not_modified", "feed", f.SourceName)
			_ = m.store.UpdateFeedAfterPoll(ctx, f.ID, res.ETag, res.Modified, false)
			continue
		}

		// First poll of a search-type feed backfills existing URLs as 'skipped'
		// to avoid dispatching years-old indexed articles.
		initialStatus := StatusNew
		if f.SourceType == SourceSearch && f.LastPolledAt == nil {
			initialStatus = StatusSkipped
			slog.Info("news.fetch.search_backfill", "source", f.SourceName, "items", len(res.Items))
		}

		newCount := 0
		for _, it := range res.Items {
			hash := ComputeTitleHash(it.Title)
			isNew, _, err := m.store.InsertItemIfNew(ctx, f.ID, it, hash, initialStatus)
			if err != nil {
				slog.Warn("news.insert.err", "url", it.URL, "error", err)
				continue
			}
			if isNew {
				newCount++
				total++
			}
		}
		_ = m.store.UpdateFeedAfterPoll(ctx, f.ID, res.ETag, res.Modified, false)
		if newCount > 0 {
			slog.Info("news.fetch.ok", "feed", f.SourceName, "new", newCount, "total_in_feed", len(res.Items))
		}
	}
	return total
}

func (m *Monitor) scoreNewItems(ctx context.Context) int {
	items, err := m.store.ListItemsByStatus(ctx, StatusNew, 200)
	if err != nil {
		slog.Error("news.score.list_new", "error", err)
		return 0
	}
	// Prefetch active feeds indexed by ID for source metadata.
	feeds, _ := m.store.ListActiveFeeds(ctx)
	feedByID := make(map[uuid.UUID]FeedSubscription, len(feeds))
	for _, f := range feeds {
		feedByID[f.ID] = f
	}
	scored := 0
	for _, it := range items {
		src, ok := feedByID[it.FeedID]
		if !ok {
			// Feed may have been deactivated — keep item but default source
			src = FeedSubscription{SourceName: "(unknown)", Priority: 5}
		}
		score := ScoreHeuristic(it, src)
		if err := m.store.UpdateItemScore(ctx, it.ID, score); err != nil {
			slog.Warn("news.score.update", "item", it.ID, "error", err)
			continue
		}
		scored++
	}
	return scored
}

func (m *Monitor) dedupScored(ctx context.Context) int {
	items, err := m.store.ListItemsByStatus(ctx, StatusScored, 200)
	if err != nil {
		slog.Error("news.dedup.list", "error", err)
		return 0
	}
	marked := 0
	for _, it := range items {
		if len(it.TitleHash) == 0 {
			continue
		}
		dup, err := m.store.HasDuplicateTitleHash(ctx, it.ID, it.TitleHash, m.dedupWindow)
		if err != nil {
			slog.Warn("news.dedup.check", "item", it.ID, "error", err)
			continue
		}
		if dup {
			_ = m.store.MarkItemDuplicate(ctx, it.ID, "title hash match within dedup window")
			marked++
		}
	}
	return marked
}

func (m *Monitor) dispatchEligible(ctx context.Context) int {
	now := time.Now()
	ok, reason := m.rateLimiter.CanDispatchNow(ctx, now)
	if !ok {
		slog.Info("news.dispatch.skip", "reason", reason)
		return 0
	}

	items, err := m.store.ListReadyForDispatch(ctx, m.heuristicThreshold, m.maxDispatchPerCycle)
	if err != nil {
		slog.Error("news.dispatch.list", "error", err)
		return 0
	}
	if len(items) == 0 {
		return 0
	}

	// Prefetch feeds for source metadata
	feeds, _ := m.store.ListActiveFeeds(ctx)
	feedByID := make(map[uuid.UUID]FeedSubscription, len(feeds))
	for _, f := range feeds {
		feedByID[f.ID] = f
	}

	dispatched := 0
	for _, it := range items {
		src := feedByID[it.FeedID]
		taskID, err := m.dispatcher.Dispatch(ctx, it, src)
		if err != nil {
			slog.Warn("news.dispatch.err", "item", it.ID, "error", err)
			_ = m.store.MarkItemSkipped(ctx, it.ID, "dispatch error: "+err.Error())
			continue
		}
		if err := m.store.MarkItemDispatched(ctx, it.ID, taskID); err != nil {
			slog.Warn("news.dispatch.mark", "item", it.ID, "error", err)
		}
		dispatched++
		// Enforce rate limit within cycle: only 1 per tick ensures min_gap across cycles too.
		break
	}
	return dispatched
}
