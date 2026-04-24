package news

import (
	"context"
	"log/slog"
	"time"
)

type RateLimiter struct {
	store             *Store
	minGap            time.Duration
	quietStart        int
	quietEnd          int
	vnLoc             *time.Location
}

func NewRateLimiter(store *Store, minGapMin, quietStartH, quietEndH int) *RateLimiter {
	loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		slog.Warn("news.rate_limiter: cannot load Asia/Ho_Chi_Minh, using UTC+7", "error", err)
		loc = time.FixedZone("VN", 7*3600)
	}
	return &RateLimiter{
		store:      store,
		minGap:     time.Duration(minGapMin) * time.Minute,
		quietStart: quietStartH,
		quietEnd:   quietEndH,
		vnLoc:      loc,
	}
}

// CanDispatchNow checks quiet hours and min-gap since last dispatch.
// Returns (allowed, reason-if-denied).
func (r *RateLimiter) CanDispatchNow(ctx context.Context, now time.Time) (bool, string) {
	if r.isQuietHour(now) {
		return false, "quiet hours (00-06 VN)"
	}
	last, err := r.store.LastDispatchedAt(ctx)
	if err != nil {
		slog.Warn("news.rate_limiter: last dispatched lookup failed", "error", err)
		return true, "" // fail-open
	}
	if last == nil {
		return true, ""
	}
	elapsed := now.Sub(*last)
	if elapsed < r.minGap {
		return false, "min_gap " + r.minGap.String() + " not elapsed (last " + elapsed.Round(time.Minute).String() + " ago)"
	}
	return true, ""
}

func (r *RateLimiter) isQuietHour(now time.Time) bool {
	h := now.In(r.vnLoc).Hour()
	if r.quietStart < r.quietEnd {
		return h >= r.quietStart && h < r.quietEnd
	}
	// wrap-around (e.g. 22..6)
	return h >= r.quietStart || h < r.quietEnd
}
