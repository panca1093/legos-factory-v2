-- +goose Up
CREATE TABLE chats (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
    title      TEXT NOT NULL,
    model      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_chats_user ON chats(user_id, updated_at);

CREATE TABLE chat_messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id     TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    role        TEXT NOT NULL,        -- user|assistant
    content     TEXT NOT NULL,
    attachments TEXT NOT NULL DEFAULT '[]',
    usage       TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_chat_messages_chat ON chat_messages(chat_id, id);

-- +goose Down
DROP TABLE chat_messages;
DROP TABLE chats;
