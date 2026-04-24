ALTER TABLE news_feed_items DROP CONSTRAINT news_feed_items_tenant_url_key;
ALTER TABLE news_feed_items ADD CONSTRAINT news_feed_items_url_key UNIQUE (url);

ALTER TABLE news_feed_subscriptions DROP CONSTRAINT news_feed_subscriptions_tenant_url_key;
ALTER TABLE news_feed_subscriptions ADD CONSTRAINT news_feed_subscriptions_url_key UNIQUE (url);

DROP INDEX IF EXISTS idx_news_feed_items_tenant_status;
DROP INDEX IF EXISTS idx_news_feed_subs_tenant;

ALTER TABLE news_feed_items DROP COLUMN tenant_id;
ALTER TABLE news_feed_subscriptions DROP COLUMN tenant_id;
