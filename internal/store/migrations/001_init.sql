-- +goose Up

-- 1. Dynamic runtime configuration (replaces .env, no restart required)
CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    is_secret INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 2. Odradek topic patterns: tracks trusted themes and user-defined guidelines
CREATE TABLE topic_patterns (
    slug TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    auto_publish INTEGER NOT NULL DEFAULT 0,
    approved_count INTEGER NOT NULL DEFAULT 0,
    guidelines TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 3. Weekly content planner (linked to LinkedIn and/or sidequestlab.dev)
CREATE TABLE weekly_themes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    week_start TEXT NOT NULL,               -- YYYY-MM-DD (Monday of target week)
    scheduled_day TEXT NOT NULL DEFAULT '', -- e.g. 'tuesday', 'thursday', or ''
    pattern_slug TEXT REFERENCES topic_patterns(slug),
    title TEXT NOT NULL,
    angle TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    target_linkedin INTEGER NOT NULL DEFAULT 1,
    target_sidequest INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'PLANNED' CHECK(status IN ('PLANNED', 'IN_PROGRESS', 'COMPLETED', 'SKIPPED')),
    post_id TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 4. Curated topics history for deduplication
CREATE TABLE topics_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type TEXT NOT NULL CHECK(source_type IN ('telegram_text', 'telegram_voice', 'telegram_link', 'weekly_theme', 'rss', 'hackernews')),
    source_url TEXT NOT NULL DEFAULT '',
    url_hash TEXT UNIQUE,
    title TEXT NOT NULL,
    keywords TEXT NOT NULL DEFAULT '[]',
    summary TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 5. Full lifecycle of each cargo/post (prepared by Odradek, delivered by Porter)
CREATE TABLE posts (
    id TEXT PRIMARY KEY,
    topic_id INTEGER REFERENCES topics_history(id),
    weekly_theme_id INTEGER REFERENCES weekly_themes(id),
    pattern_slug TEXT REFERENCES topic_patterns(slug),
    status TEXT NOT NULL CHECK(status IN (
        'CURATED',
        'DRAFTING',
        'RENDERING',
        'QA_REVIEW',
        'AWAITING_APPROVAL',
        'EDITING',
        'PUBLISHING',
        'PUBLISHED',
        'FAILED',
        'DISCARDED'
    )),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    requires_human_review INTEGER NOT NULL DEFAULT 1,
    uncertainty_reason TEXT NOT NULL DEFAULT '',
    last_reminder_at DATETIME,
    content_text TEXT NOT NULL DEFAULT '',
    media_type TEXT NOT NULL DEFAULT 'none' CHECK(media_type IN ('none', 'code_snippet', 'mermaid')),
    media_payload TEXT NOT NULL DEFAULT '',
    media_language TEXT NOT NULL DEFAULT '',
    media_path TEXT NOT NULL DEFAULT '',
    publish_to_linkedin INTEGER NOT NULL DEFAULT 1,
    publish_to_sidequest INTEGER NOT NULL DEFAULT 0,
    sidequest_slug TEXT NOT NULL DEFAULT '',
    sidequest_mdx_path TEXT NOT NULL DEFAULT '',
    telegram_preview_msg_id INTEGER,
    linkedin_post_urn TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at DATETIME
);

CREATE INDEX idx_posts_status_reminder ON posts(status, last_reminder_at);
CREATE INDEX idx_weekly_themes_week_status ON weekly_themes(week_start, status);

-- 6. Detailed QA loop logs (Writer <-> Odradek QA)
CREATE TABLE qa_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL,
    draft_snapshot TEXT NOT NULL,
    qa_approved INTEGER NOT NULL,
    qa_response TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 7. LinkedIn OAuth2 token tracking (60-day expiry)
CREATE TABLE oauth_tokens (
    provider TEXT PRIMARY KEY,
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    person_urn TEXT NOT NULL,
    scopes TEXT NOT NULL,
    expires_at DATETIME NOT NULL,
    last_alert_sent_at DATETIME,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS oauth_tokens;
DROP TABLE IF EXISTS qa_logs;
DROP TABLE IF EXISTS posts;
DROP TABLE IF EXISTS topics_history;
DROP TABLE IF EXISTS weekly_themes;
DROP TABLE IF EXISTS topic_patterns;
DROP TABLE IF EXISTS settings;
