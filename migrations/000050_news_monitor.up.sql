-- News monitor: RSS/HTML feed subscriptions + fetched items.
-- Drives the goctech team's event-driven posting (replaces the 6 fixed-slot cron jobs).

CREATE TABLE news_feed_subscriptions (
    id              uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
    url             text NOT NULL UNIQUE,
    source_name     text NOT NULL,
    source_type     varchar(16) NOT NULL DEFAULT 'rss',  -- rss | atom | html
    category        varchar(32),
    priority        int NOT NULL DEFAULT 5,               -- 1..10, bigger = more trusted
    active          bool NOT NULL DEFAULT true,
    last_polled_at  timestamptz,
    last_etag       text,
    last_modified   text,
    fetch_fail_count int NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_news_feed_subscriptions_active ON news_feed_subscriptions(active, last_polled_at NULLS FIRST);

CREATE TABLE news_feed_items (
    id                 uuid PRIMARY KEY DEFAULT uuid_generate_v7(),
    feed_id            uuid NOT NULL REFERENCES news_feed_subscriptions(id) ON DELETE CASCADE,
    url                text NOT NULL UNIQUE,
    title              text NOT NULL,
    summary            text,
    published_at       timestamptz,
    fetched_at         timestamptz NOT NULL DEFAULT now(),
    heuristic_score    int,
    llm_score          int,
    llm_reason         text,
    status             varchar(20) NOT NULL DEFAULT 'new',  -- new | scored | dispatched | skipped | duplicate | error
    dispatched_task_id uuid,
    skip_reason        text,
    title_hash         bytea
);

CREATE INDEX idx_news_feed_items_status ON news_feed_items(status, fetched_at DESC);
CREATE INDEX idx_news_feed_items_title_hash ON news_feed_items(title_hash) WHERE title_hash IS NOT NULL;
CREATE INDEX idx_news_feed_items_dispatched ON news_feed_items(dispatched_task_id) WHERE dispatched_task_id IS NOT NULL;
CREATE INDEX idx_news_feed_items_feed ON news_feed_items(feed_id, fetched_at DESC);

-- Seed 31 foreign-only tech/AI feeds. VN sources intentionally excluded.
INSERT INTO news_feed_subscriptions (url, source_name, source_type, category, priority) VALUES
    -- General tech (10)
    ('https://feeds.feedburner.com/TechCrunch',                 'TechCrunch',      'rss',  'tech-general', 8),
    ('https://www.theverge.com/rss/index.xml',                  'TheVerge',        'rss',  'tech-general', 8),
    ('https://feeds.arstechnica.com/arstechnica/index',         'ArsTechnica',     'rss',  'tech-deep',    7),
    ('https://www.wired.com/feed/rss',                          'Wired',           'rss',  'tech-general', 7),
    ('https://www.engadget.com/rss.xml',                        'Engadget',        'rss',  'tech-general', 6),
    ('https://www.cnet.com/rss/news/',                          'CNET',            'rss',  'tech-general', 5),
    ('https://www.zdnet.com/news/rss.xml',                      'ZDNET',           'rss',  'tech-general', 5),
    ('https://venturebeat.com/feed/',                           'VentureBeat',     'rss',  'tech-general', 7),
    ('https://thenextweb.com/feed',                             'TheNextWeb',      'rss',  'tech-general', 6),
    ('https://gizmodo.com/rss',                                 'Gizmodo',         'rss',  'tech-general', 5),

    -- AI official (10)
    ('https://www.anthropic.com/news',                          'Anthropic',       'html', 'ai-official',  10),
    ('https://openai.com/blog/',                                'OpenAI',          'html', 'ai-official',  10),
    ('https://blog.google/technology/ai/rss/',                  'Google AI',       'rss',  'ai-official',  9),
    ('https://ai.meta.com/blog/',                               'Meta AI',         'html', 'ai-official',  8),
    ('https://mistral.ai/news',                                 'Mistral',         'html', 'ai-oss',       7),
    ('https://huggingface.co/blog/feed.xml',                    'HuggingFace',     'rss',  'ai-oss',       8),
    ('https://qwenlm.github.io/atom.xml',                       'Qwen',            'rss',  'ai-oss',       7),
    ('https://api-docs.deepseek.com/news',                      'DeepSeek',        'html', 'ai-oss',       7),
    ('https://cohere.com/blog',                                 'Cohere',          'html', 'ai-oss',       6),
    ('https://x.ai/news',                                       'xAI',             'html', 'ai-official',  7),

    -- AI community (3)
    ('https://www.marktechpost.com/feed/',                      'MarkTechPost',    'rss',  'ai-community', 5),
    ('https://thegradient.pub/rss/',                            'The Gradient',    'rss',  'ai-community', 6),
    ('https://simonwillison.net/atom/everything/',              'Simon Willison',  'rss',  'ai-community', 7),

    -- Chip/HW (3)
    ('https://www.anandtech.com/rss/',                          'AnandTech',       'rss',  'chip-hw',      6),
    ('https://www.tomshardware.com/feeds/all',                  'TomsHardware',    'rss',  'chip-hw',      6),
    ('https://blogs.nvidia.com/feed/',                          'NVIDIA Blog',     'rss',  'chip-hw',      7),

    -- Security (3)
    ('https://krebsonsecurity.com/feed/',                       'Krebs',           'rss',  'security',     8),
    ('https://www.bleepingcomputer.com/feed/',                  'BleepingComputer','rss',  'security',     7),
    ('https://feeds.feedburner.com/TheHackersNews',             'HackerNews',      'rss',  'security',     6),

    -- News agencies (2)
    ('https://www.reuters.com/rssFeed/technologyNews',          'Reuters Tech',    'rss',  'tech-general', 7),
    ('http://feeds.bbci.co.uk/news/technology/rss.xml',         'BBC Tech',        'rss',  'tech-general', 6);
