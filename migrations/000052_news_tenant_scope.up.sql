-- Tenant-scope news feeds + items so the Web UI can manage them per tenant.
-- All existing rows belong to the master tenant (the only one currently
-- using the news monitor). Default keeps the column NOT NULL without
-- forcing a backfill query.

ALTER TABLE news_feed_subscriptions
  ADD COLUMN tenant_id uuid NOT NULL
  DEFAULT '0193a5b0-7000-7000-8000-000000000001'::uuid
  REFERENCES tenants(id) ON DELETE CASCADE;

ALTER TABLE news_feed_items
  ADD COLUMN tenant_id uuid NOT NULL
  DEFAULT '0193a5b0-7000-7000-8000-000000000001'::uuid
  REFERENCES tenants(id) ON DELETE CASCADE;

CREATE INDEX idx_news_feed_subs_tenant
  ON news_feed_subscriptions(tenant_id, active, last_polled_at NULLS FIRST);

CREATE INDEX idx_news_feed_items_tenant_status
  ON news_feed_items(tenant_id, status, fetched_at DESC);

-- url uniqueness was global; make it per-tenant so two tenants can subscribe
-- to the same feed independently.
ALTER TABLE news_feed_subscriptions DROP CONSTRAINT news_feed_subscriptions_url_key;
ALTER TABLE news_feed_subscriptions ADD CONSTRAINT news_feed_subscriptions_tenant_url_key UNIQUE (tenant_id, url);

ALTER TABLE news_feed_items DROP CONSTRAINT news_feed_items_url_key;
ALTER TABLE news_feed_items ADD CONSTRAINT news_feed_items_tenant_url_key UNIQUE (tenant_id, url);
