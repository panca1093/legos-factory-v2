package usecase

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"legos-factory/backend/internal/model"
)

// Identity implements local sessions for dev-mode and OIDC sign-in.
type Identity struct {
	Users   UserStore
	Mode    string // dev|oidc
	DevUser model.User
	Log     *slog.Logger
}

func (s *Identity) EnsureDevUser(ctx context.Context) (*model.User, error) {
	dev := s.DevUser
	dev.Role = model.RoleOwner
	if err := s.Users.EnsureUser(ctx, dev); err != nil {
		return nil, err
	}
	return &s.DevUser, nil
}

// Login issues a session. In dev mode this signs in DevUser directly; OIDC
// code exchange produces a UserInfo that upserts a user first.
func (s *Identity) Login(ctx context.Context, u *model.User) (sessionID string, expires bool, err error) {
	sid := "sess_" + uuid.NewString()
	ttl := 7 * 24 * time.Hour
	if err := s.Users.CreateSession(ctx, sid, u.ID, time.Now().Add(ttl)); err != nil {
		return "", false, err
	}
	return sid, true, nil
}

// UserForSession resolves the session cookie value to a user.
func (s *Identity) UserForSession(ctx context.Context, sid string) (*model.User, error) {
	return s.Users.UserForSession(ctx, sid)
}

func (s *Identity) Logout(ctx context.Context, sid string) error {
	return s.Users.DeleteSession(ctx, sid)
}

// UpsertOIDCUser creates or updates a local user from verified OIDC claims.
func (s *Identity) UpsertUser(ctx context.Context, email, name string) (*model.User, error) {
	existing, err := s.Users.ByEmail(ctx, email)
	if errors.Is(err, model.ErrUserNotFound) {
		u := model.User{ID: "usr_" + uuid.NewString()[:12], Email: email, Name: name, Role: model.RoleEngineer}
		if err := s.Users.Create(ctx, u); err != nil {
			return nil, err
		}
		return &u, nil
	}
	if err != nil {
		return nil, err
	}
	return &model.User{ID: existing.ID, Email: email, Name: existing.Name, Role: existing.Role}, nil
}
