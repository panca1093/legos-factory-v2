package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"legos-factory/backend/internal/model"
)

// UserRepo owns the users and sessions tables.
type UserRepo struct{ DB *sql.DB }

// EnsureUser inserts u unless a user with the same id already exists.
func (r *UserRepo) EnsureUser(ctx context.Context, u model.User) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO users (id, email, name, role, created_at) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO NOTHING`, u.ID, u.Email, u.Name, string(u.Role), time.Now().Unix())
	return err
}

func (r *UserRepo) Create(ctx context.Context, u model.User) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO users (id, email, name, role, created_at) VALUES (?,?,?,?,?)`,
		u.ID, u.Email, u.Name, string(u.Role), time.Now().Unix())
	return err
}

func (r *UserRepo) ByEmail(ctx context.Context, email string) (*model.User, error) {
	var u model.User
	var role string
	err := r.DB.QueryRowContext(ctx, `SELECT id, email, name, role FROM users WHERE email = ?`, email).
		Scan(&u.ID, &u.Email, &u.Name, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Role = model.Role(role)
	return &u, nil
}

func (r *UserRepo) CreateSession(ctx context.Context, sid, userID string, expiresAt time.Time) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO sessions (id, user_id, expires_at, created_at) VALUES (?,?,?,?)`,
		sid, userID, expiresAt.Unix(), time.Now().Unix())
	return err
}

func (r *UserRepo) UserForSession(ctx context.Context, sid string) (*model.User, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT u.id, u.email, u.name, u.role FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.id = ? AND s.expires_at > ?`, sid, time.Now().Unix())
	var u model.User
	var role string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrSessionNotFound
		}
		return nil, err
	}
	u.Role = model.Role(role)
	return &u, nil
}

func (r *UserRepo) DeleteSession(ctx context.Context, sid string) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sid)
	return err
}
