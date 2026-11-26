-- +goose Up

ALTER TABLE posts ADD COLUMN last_metrics_reminder_at DATETIME;

CREATE TABLE post_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id TEXT NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    impressions INTEGER NOT NULL DEFAULT 0,
    reactions INTEGER NOT NULL DEFAULT 0,
    comments INTEGER NOT NULL DEFAULT 0,
    reposts INTEGER NOT NULL DEFAULT 0,
    raw_notes TEXT NOT NULL DEFAULT '',
    captured_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_post_metrics_post_id ON post_metrics(post_id, captured_at);

-- +goose Down
DROP TABLE IF EXISTS post_metrics;
