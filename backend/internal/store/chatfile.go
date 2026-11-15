package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"legos-factory/backend/internal/model"
)

const chatFileCols = `id, chat_id, user_id, message_id, name, media_type, kind, size, sha256, created_at`

// AddFile stores f unless the chat already has maxPending files waiting for a message
// (ErrChatTooManyFiles) or no longer exists (ErrChatNotFound).
func (r *ChatRepo) AddFile(ctx context.Context, f *model.ChatFileRec, maxPending int) error {
	res, err := r.DB.ExecContext(ctx, `INSERT INTO chat_files (`+chatFileCols+`)
		SELECT ?,?,?,?,?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM chat_files WHERE chat_id = ? AND message_id IS NULL) < ?`,
		f.ID, f.ChatID, f.UserID, f.MessageID, f.Name, f.MediaType, f.Kind, f.Size, f.SHA256, f.CreatedAt.Unix(), f.ChatID, maxPending)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return model.ErrChatNotFound
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrChatTooManyFiles
	}
	return nil
}

func scanChatFile(scan rowScanner) (*model.ChatFileRec, error) {
	var f model.ChatFileRec
	var msg sql.NullInt64
	var created int64
	if err := scan(&f.ID, &f.ChatID, &f.UserID, &msg, &f.Name, &f.MediaType, &f.Kind, &f.Size, &f.SHA256, &created); err != nil {
		return nil, err
	}
	if msg.Valid {
		f.MessageID = &msg.Int64
	}
	f.CreatedAt = time.Unix(created, 0)
	return &f, nil
}

// File loads a file of the chat; a file of another chat is not found.
func (r *ChatRepo) File(ctx context.Context, chatID, fileID string) (*model.ChatFileRec, error) {
	f, err := scanChatFile(r.DB.QueryRowContext(ctx, `SELECT `+chatFileCols+` FROM chat_files WHERE id = ? AND chat_id = ?`, fileID, chatID).Scan)
	if err == sql.ErrNoRows {
		return nil, model.ErrChatFileNotFound
	}
	return f, err
}

// DeleteFile removes a file that no message carries yet, reporting whether it did.
func (r *ChatRepo) DeleteFile(ctx context.Context, chatID, fileID string) (bool, error) {
	res, err := r.DB.ExecContext(ctx, `DELETE FROM chat_files WHERE id = ? AND chat_id = ? AND message_id IS NULL`, fileID, chatID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// StaleFiles lists files no message carries that were uploaded before cutoff.
func (r *ChatRepo) StaleFiles(ctx context.Context, cutoff time.Time) ([]model.ChatFileRec, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+chatFileCols+` FROM chat_files WHERE message_id IS NULL AND created_at < ?`, cutoff.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ChatFileRec
	for rows.Next() {
		f, err := scanChatFile(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// AddMessageFiles stores m and attaches the files to it in one transaction; a file that is gone or
// already attached fails the whole call with model.ErrChatFilesUnavailable and stores nothing.
func (r *ChatRepo) AddMessageFiles(ctx context.Context, m *model.ChatMessage, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return r.AddMessage(ctx, m)
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := addMessage(ctx, tx, m); err != nil {
		return err
	}
	args := []any{m.ID, m.ChatID}
	for _, id := range fileIDs {
		args = append(args, id)
	}
	res, err := tx.ExecContext(ctx, `UPDATE chat_files SET message_id = ? WHERE chat_id = ? AND message_id IS NULL AND id IN (?`+strings.Repeat(",?", len(fileIDs)-1)+`)`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); int(n) != len(fileIDs) {
		return model.ErrChatFilesUnavailable
	}
	if err := tx.Commit(); err != nil {
		m.ID = 0
		return err
	}
	return nil
}

func addMessage(ctx context.Context, x interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, m *model.ChatMessage) error {
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
	res, err := x.ExecContext(ctx, `INSERT INTO chat_messages (chat_id, role, content, attachments, usage, error, created_at) VALUES (?,?,?,?,?,?,?)`,
		m.ChatID, m.Role, m.Content, string(atts), string(usage), m.Error, m.CreatedAt.Unix())
	if err != nil {
		return err
	}
	m.ID, err = res.LastInsertId()
	return err
}
