package model

type Role string

const (
	RoleOwner    Role = "OWNER"
	RoleAdmin    Role = "ADMIN"
	RoleEngineer Role = "ENGINEER"
	RoleViewer   Role = "VIEWER"
)

// CanWrite reports whether the role may mutate work.
func (r Role) CanWrite() bool { return r != RoleViewer }

// IsAdmin reports whether the role may change governance/settings.
func (r Role) IsAdmin() bool { return r == RoleOwner || r == RoleAdmin }

type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  Role   `json:"role"`
}
