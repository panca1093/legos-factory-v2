-- +goose Up
ALTER TABLE work_orders ADD COLUMN source_chat_id TEXT REFERENCES chats(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE work_orders DROP COLUMN source_chat_id;
