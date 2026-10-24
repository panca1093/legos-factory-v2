package usecase

import (
	"context"
	"errors"

	"legos-factory/backend/internal/model"
)

// Projects owns project creation and the access rule applied everywhere:
// OWNER/ADMIN reach every project in the workspace; everyone else only the
// projects they are a member of. Inaccessible projects surface as not found
// so their existence is not disclosed.
type Projects struct {
	Store   ProjectStore
	Members MemberStore
	Users   UserDirectory
}

// Scope returns the project ids u may read; nil means unrestricted.
func (p *Projects) Scope(ctx context.Context, u *model.User) ([]string, error) {
	if u.Role.IsAdmin() {
		return nil, nil
	}
	return p.Members.ProjectIDsFor(ctx, u.ID)
}

func (p *Projects) CanAccess(ctx context.Context, u *model.User, projectID string) (bool, error) {
	if u.Role.IsAdmin() {
		return true, nil
	}
	return p.Members.IsMember(ctx, projectID, u.ID)
}

// Authorize returns model.ErrProjectNotFound unless u may access projectID.
func (p *Projects) Authorize(ctx context.Context, u *model.User, projectID string) error {
	ok, err := p.CanAccess(ctx, u, projectID)
	if err != nil {
		return err
	}
	if !ok {
		return model.ErrProjectNotFound
	}
	return nil
}

func (p *Projects) Get(ctx context.Context, u *model.User, id string) (*model.Project, error) {
	if err := p.Authorize(ctx, u, id); err != nil {
		return nil, err
	}
	return p.Store.Get(ctx, id)
}

func (p *Projects) List(ctx context.Context, u *model.User) ([]model.Project, error) {
	all, err := p.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	scope, err := p.Scope(ctx, u)
	if err != nil || scope == nil {
		return all, err
	}
	allowed := toSet(scope)
	out := make([]model.Project, 0, len(all))
	for _, pr := range all {
		if allowed[pr.ID] {
			out = append(out, pr)
		}
	}
	return out, nil
}

// Create stores the project and makes its creator the first member.
func (p *Projects) Create(ctx context.Context, u *model.User, pr *model.Project) error {
	if err := p.Store.Create(ctx, pr); err != nil {
		return err
	}
	return p.Members.Add(ctx, pr.ID, u.ID)
}

func (p *Projects) ListMembers(ctx context.Context, projectID string) ([]model.ProjectMember, error) {
	if _, err := p.Store.Get(ctx, projectID); err != nil {
		return nil, err
	}
	return p.Members.List(ctx, projectID)
}

func (p *Projects) AddMember(ctx context.Context, projectID, email string) error {
	if _, err := p.Store.Get(ctx, projectID); err != nil {
		return err
	}
	u, err := p.Users.ByEmail(ctx, email)
	if err != nil {
		return err
	}
	return p.Members.Add(ctx, projectID, u.ID)
}

func (p *Projects) RemoveMember(ctx context.Context, projectID, userID string) error {
	if _, err := p.Store.Get(ctx, projectID); err != nil {
		return err
	}
	return p.Members.Remove(ctx, projectID, userID)
}

// IsNotFound reports access-denied-as-not-found and genuine absence alike.
func IsNotFound(err error) bool {
	return errors.Is(err, model.ErrProjectNotFound) || errors.Is(err, model.ErrWorkOrderNotFound) ||
		errors.Is(err, model.ErrKnowledgeNotFound) || errors.Is(err, model.ErrUserNotFound) ||
		errors.Is(err, model.ErrConnectionNotFound)
}

func toSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
