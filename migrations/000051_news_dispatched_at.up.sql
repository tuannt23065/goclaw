-- Track actual dispatch timestamp separately from ingest timestamp.
-- LastDispatchedAt previously used MAX(fetched_at) which is the FETCH time
-- (insert into DB), not when we sent the InboundMessage to leader. When a
-- cycle dispatched an item that was fetched in an earlier cycle, the rate
-- limiter saw an artificially-old "last dispatch" and let a new item through
-- every 15-min cycle instead of honoring min_dispatch_gap_min=30.
-- Result: ~15-min posting cadence vs the configured 30 min.

ALTER TABLE news_feed_items ADD COLUMN dispatched_at timestamptz;

-- Backfill existing dispatched rows so the rate limiter has a sane baseline
-- (treat fetched_at as the dispatch time for legacy rows).
UPDATE news_feed_items SET dispatched_at = fetched_at WHERE status = 'dispatched';

CREATE INDEX idx_news_feed_items_dispatched_at
  ON news_feed_items(dispatched_at DESC NULLS LAST)
  WHERE status = 'dispatched';
