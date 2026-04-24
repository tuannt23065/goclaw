DROP INDEX IF EXISTS idx_news_feed_items_dispatched_at;
ALTER TABLE news_feed_items DROP COLUMN IF EXISTS dispatched_at;
