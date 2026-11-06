package store

import (
	"context"
	"database/sql"
	"time"

	"legos-factory/backend/internal/model"
)

type UsageRepo struct{ DB *sql.DB }

func (r *UsageRepo) Insert(ctx context.Context, u model.UsageRecord) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO usage_records (id, wo_id, user_id, role, provider, model, tokens_in, tokens_out, cache_read_tokens, cache_write_tokens, cost_micros, source, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, nullStr(u.WOID), nullStr(u.UserID), u.Role, u.Provider, u.Model,
		u.TokensIn, u.TokensOut, u.CacheRead, u.CacheWrite, u.CostMicros, u.Source, u.CreatedAt.Unix())
	return err
}

func (r *UsageRepo) Totals(ctx context.Context) (model.UsageTotals, error) {
	var t model.UsageTotals
	err := r.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros),0), COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0)
		FROM usage_records`).Scan(&t.CostMicros, &t.TokensIn, &t.TokensOut)
	return t, err
}

func (r *UsageRepo) SpentMicros(ctx context.Context) int64 {
	var spent int64
	_ = r.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros),0) FROM usage_records`).Scan(&spent)
	return spent
}

// Since returns a user's usage records created at or after since, oldest first.
func (r *UsageRepo) Since(ctx context.Context, userID string, since time.Time) ([]model.UsageRecord, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, COALESCE(wo_id,''), role, provider, model, tokens_in, tokens_out, cost_micros, source, created_at
		FROM usage_records WHERE user_id = ? AND created_at >= ? ORDER BY created_at`, userID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UsageRecord
	for rows.Next() {
		u := model.UsageRecord{UserID: userID}
		var at int64
		if err := rows.Scan(&u.ID, &u.WOID, &u.Role, &u.Provider, &u.Model, &u.TokensIn, &u.TokensOut, &u.CostMicros, &u.Source, &at); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(at, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}
