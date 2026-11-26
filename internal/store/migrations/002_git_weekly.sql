-- +goose Up
-- Recreate topics_history with 'git_weekly' in source_type CHECK constraint.
PRAGMA foreign_keys = OFF;

CREATE TABLE topics_history_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type TEXT NOT NULL CHECK(source_type IN ('telegram_text', 'telegram_voice', 'telegram_link', 'weekly_theme', 'git_weekly', 'rss', 'hackernews')),
    source_url TEXT NOT NULL DEFAULT '',
    url_hash TEXT UNIQUE,
    title TEXT NOT NULL,
    keywords TEXT NOT NULL DEFAULT '[]',
    summary TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO topics_history_new (id, source_type, source_url, url_hash, title, keywords, summary, created_at)
SELECT id, source_type, source_url, url_hash, title, keywords, summary, created_at FROM topics_history;

DROP TABLE topics_history;
ALTER TABLE topics_history_new RENAME TO topics_history;

PRAGMA foreign_keys = ON;

-- +goose Down
SELECT 1;
