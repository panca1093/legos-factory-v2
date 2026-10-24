package store

import (
	"context"
	"database/sql"

	"legos-factory/backend/internal/model"
)

type UsageRepo struct{ DB *sql.DB }

func (r *UsageRepo) Insert(ctx context.Context, u model.UsageRecord) error {
	_, err := r.DB.ExecContext(ctx, `INSERT INTO usage_records (id, wo_id, user_id, role, provider, model, tokens_in, tokens_out, cost_micros, source, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, nullStr(u.WOID), nullStr(u.UserID), u.Role, u.Provider, u.Model,
		u.TokensIn, u.TokensOut, u.CostMicros, u.Source, u.CreatedAt.Unix())
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
