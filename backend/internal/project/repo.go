package project

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("project not found")

type Project struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	RepoURL        string    `json:"repo_url"`
	Language       string    `json:"language"`
	QualityProfile string    `json:"quality_profile"` // JSON: [{"name","command"}]
	CreatedAt      time.Time `json:"created_at"`
}

type Repository struct{ DB *sql.DB }

const pCols = `id, name, description, repo_url, language, quality_profile, created_at`

func (r *Repository) Create(ctx context.Context, p *Project) error {
	if p.ID == "" {
		p.ID = "prj_" + uuid.NewString()[:12]
	}
	p.CreatedAt = time.Now()
	_, err := r.DB.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_url, language, quality_profile, created_at)
		VALUES (?,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.RepoURL, p.Language, p.QualityProfile, p.CreatedAt.Unix())
	return err
}

func (r *Repository) Get(ctx context.Context, id string) (*Project, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT `+pCols+` FROM projects WHERE id = ?`, id)
	return scanP(row.Scan)
}

func (r *Repository) List(ctx context.Context) ([]Project, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+pCols+` FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanP(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanP(scan func(dest ...any) error) (*Project, error) {
	var p Project
	var createdAt int64
	err := scan(&p.ID, &p.Name, &p.Description, &p.RepoURL, &p.Language, &p.QualityProfile, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = time.Unix(createdAt, 0)
	return &p, nil
}
