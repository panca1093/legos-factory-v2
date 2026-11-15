-- +goose Up
CREATE TABLE chat_files (
    id         TEXT PRIMARY KEY,
    chat_id    TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL,
    message_id INTEGER,
    name       TEXT NOT NULL,
    media_type TEXT NOT NULL,
    kind       TEXT NOT NULL,
    size       INTEGER NOT NULL,
    sha256     TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_chat_files_chat ON chat_files(chat_id);

-- +goose Down
DROP TABLE chat_files;
