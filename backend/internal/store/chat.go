package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"legos-factory/backend/internal/model"
)

type ChatRepo struct{ DB *sql.DB }

const chatCols = `id, user_id, COALESCE(project_id,''), title, model, created_at, updated_at`

func (r *ChatRepo) Create(ctx context.Context, c *model.Chat) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO chats (id, user_id, project_id, title, model, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		c.ID, c.UserID, nullStr(c.ProjectID), c.Title, c.Model, c.CreatedAt.Unix(), c.UpdatedAt.Unix())
	return err
}

func scanChat(scan rowScanner) (*model.Chat, error) {
	var c model.Chat
	var created, updated int64
	if err := scan(&c.ID, &c.UserID, &c.ProjectID, &c.Title, &c.Model, &created, &updated); err != nil {
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return &c, nil
}

func (r *ChatRepo) Get(ctx context.Context, id string) (*model.Chat, error) {
	c, err := scanChat(r.DB.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE id = ?`, id).Scan)
	if err == sql.ErrNoRows {
		return nil, model.ErrChatNotFound
	}
	return c, err
}

func (r *ChatRepo) List(ctx context.Context, userID string) ([]model.Chat, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+chatCols+` FROM chats WHERE user_id = ? ORDER BY updated_at DESC, rowid DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Chat{}
	for rows.Next() {
		c, err := scanChat(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *ChatRepo) Update(ctx context.Context, id, title, modelRef string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE chats SET title = ?, model = ?, updated_at = ? WHERE id = ?`, title, modelRef, time.Now().Unix(), id)
	return err
}

func (r *ChatRepo) Rename(ctx context.Context, id, title string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE chats SET title = ? WHERE id = ?`, title, id)
	return err
}

func (r *ChatRepo) Touch(ctx context.Context, id string) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE chats SET updated_at = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

func (r *ChatRepo) Delete(ctx context.Context, id string) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM chats WHERE id = ?`, id)
	return err
}

func (r *ChatRepo) AddMessage(ctx context.Context, m *model.ChatMessage) error {
	if m.Attachments == nil {
		m.Attachments = []model.ChatAttachment{}
	}
	atts, err := json.Marshal(m.Attachments)
	if err != nil {
		return err
	}
	usage := []byte{}
	if m.Usage != nil {
		if usage, err = json.Marshal(m.Usage); err != nil {
			return err
		}
	}
	res, err := r.DB.ExecContext(ctx, `INSERT INTO chat_messages (chat_id, role, content, attachments, usage, error, created_at) VALUES (?,?,?,?,?,?,?)`,
		m.ChatID, m.Role, m.Content, string(atts), string(usage), m.Error, m.CreatedAt.Unix())
	if err != nil {
		return err
	}
	m.ID, err = res.LastInsertId()
	return err
}

func (r *ChatRepo) Messages(ctx context.Context, chatID string) ([]model.ChatMessage, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, chat_id, role, content, attachments, usage, error, created_at FROM chat_messages WHERE chat_id = ? ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ChatMessage{}
	for rows.Next() {
		var m model.ChatMessage
		var atts, usage string
		var created int64
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &atts, &usage, &m.Error, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(atts), &m.Attachments); err != nil {
			return nil, err
		}
		if usage != "" {
			m.Usage = &model.ChatUsage{}
			if err := json.Unmarshal([]byte(usage), m.Usage); err != nil {
				return nil, err
			}
		}
		m.CreatedAt = time.Unix(created, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}
