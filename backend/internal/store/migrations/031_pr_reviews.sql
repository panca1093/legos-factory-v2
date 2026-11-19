-- +goose Up
CREATE TABLE review_identities (           -- pin: which account posts for this user on this host
    user_id TEXT NOT NULL, provider TEXT NOT NULL, host TEXT NOT NULL,
    source TEXT NOT NULL,                  -- connection | gh | glab
    connection_id TEXT NOT NULL DEFAULT '',
    login TEXT NOT NULL, host_user_id TEXT NOT NULL DEFAULT '',
    pinned_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, provider, host)
);
CREATE TABLE pr_reviews (
    id TEXT PRIMARY KEY, user_id TEXT NOT NULL, project_id TEXT NOT NULL,
    provider TEXT NOT NULL, host TEXT NOT NULL, repo TEXT NOT NULL, number INTEGER NOT NULL,
    head_sha TEXT NOT NULL DEFAULT '',     -- head the current drafts were made on
    summary TEXT NOT NULL DEFAULT '', verdict TEXT NOT NULL DEFAULT 'comment',
    posted_at INTEGER, posted_head_sha TEXT NOT NULL DEFAULT '', posted_verdict TEXT NOT NULL DEFAULT '',
    posted_ref TEXT NOT NULL DEFAULT '', posted_as TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
    UNIQUE (user_id, provider, host, repo, number)
);
CREATE TABLE pr_review_runs (
    id TEXT PRIMARY KEY, review_id TEXT NOT NULL REFERENCES pr_reviews(id) ON DELETE CASCADE,
    head_sha TEXT NOT NULL, state TEXT NOT NULL,      -- running|completed|failed
    agent TEXT NOT NULL, harness TEXT NOT NULL, model TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
    tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_micros INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL, finished_at INTEGER
);
CREATE TABLE pr_review_drafts (
    id TEXT PRIMARY KEY, review_id TEXT NOT NULL REFERENCES pr_reviews(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL, old_path TEXT NOT NULL DEFAULT '',
    side TEXT NOT NULL DEFAULT 'new', line INTEGER NOT NULL DEFAULT 0, old_line INTEGER NOT NULL DEFAULT 0,
    inline INTEGER NOT NULL DEFAULT 1,     -- 0 = General (not anchorable)
    head_sha TEXT NOT NULL, severity TEXT NOT NULL, title TEXT NOT NULL,
    body TEXT NOT NULL, ai_body TEXT NOT NULL,       -- ai_body kept to show "edited"
    status TEXT NOT NULL,                  -- pending|accepted|discarded|superseded|outdated|posted
    posted_ref TEXT NOT NULL DEFAULT '', post_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX idx_pr_review_drafts_review ON pr_review_drafts(review_id, status);
CREATE TABLE pr_review_viewed (
    review_id TEXT NOT NULL REFERENCES pr_reviews(id) ON DELETE CASCADE,
    path TEXT NOT NULL, patch_hash TEXT NOT NULL, viewed_at INTEGER NOT NULL,
    PRIMARY KEY (review_id, path)
);

-- +goose Down
DROP TABLE pr_review_viewed;
DROP TABLE pr_review_drafts;
DROP TABLE pr_review_runs;
DROP TABLE pr_reviews;
DROP TABLE review_identities;
