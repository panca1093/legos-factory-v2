package store

import (
	"context"
	"database/sql"
	"time"

	"legos-factory/backend/internal/model"
)

type CodeFactRepo struct{ DB *sql.DB }

// Add stores facts, replacing any earlier fact on the same path and line, and keeps only the newest max per work order.
func (r *CodeFactRepo) Add(ctx context.Context, woID string, facts []model.CodeFact, max int) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, f := range facts {
		if _, err := tx.ExecContext(ctx, `DELETE FROM code_facts WHERE wo_id = ? AND path = ? AND line = ?`, woID, f.Path, f.Line); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO code_facts (wo_id, path, line, claim, by_role, sha, created_at) VALUES (?,?,?,?,?,?,?)`,
			woID, f.Path, f.Line, f.Claim, f.By, f.SHA, time.Now().Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM code_facts WHERE wo_id = ? AND id NOT IN
		(SELECT id FROM code_facts WHERE wo_id = ? ORDER BY id DESC LIMIT ?)`, woID, woID, max); err != nil {
		return err
	}
	return tx.Commit()
}

// List returns the work order's facts, oldest first.
func (r *CodeFactRepo) List(ctx context.Context, woID string) ([]model.CodeFact, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT path, line, claim, by_role, sha FROM code_facts WHERE wo_id = ? ORDER BY id`, woID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CodeFact
	for rows.Next() {
		var f model.CodeFact
		if err := rows.Scan(&f.Path, &f.Line, &f.Claim, &f.By, &f.SHA); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
