package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"legos-factory/backend/internal/model"
)

type SettingsRepo struct{ DB *sql.DB }

func (r *SettingsRepo) Get(ctx context.Context) (*model.Settings, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT key, value FROM settings_kv`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s := &model.Settings{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		switch k {
		case "budget_limit_micros":
			fmt.Sscanf(v, "%d", &s.BudgetLimitMicros)
		case "foreman_model":
			s.ForemanModel = v
		case "architect_model":
			s.ArchitectModel = v
		case "builder_model":
			s.BuilderModel = v
		case "inspector_model":
			s.InspectorModel = v
		case "allowed_models":
			s.AllowedModels = strings.Split(strings.Trim(v, ","), ",")
		case "chat_model":
			s.ChatModel = v
		default:
			if s.Extra == nil {
				s.Extra = map[string]string{}
			}
			s.Extra[k] = v
		}
	}
	return s, rows.Err()
}

// Put upserts the governed keys; AllowedModels is left untouched when nil
// and Extra is never written.
func (r *SettingsRepo) Put(ctx context.Context, s model.Settings) error {
	writes := map[string]string{
		"budget_limit_micros": fmt.Sprintf("%d", s.BudgetLimitMicros),
		"foreman_model":       s.ForemanModel,
		"architect_model":     s.ArchitectModel,
		"builder_model":       s.BuilderModel,
		"inspector_model":     s.InspectorModel,
		"chat_model":          s.ChatModel,
	}
	if s.AllowedModels != nil {
		writes["allowed_models"] = strings.Trim(strings.Join(s.AllowedModels, ","), ",")
	}
	for k, v := range writes {
		if _, err := r.DB.ExecContext(ctx, `INSERT INTO settings_kv (key, value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return nil
}

func (r *SettingsRepo) Patch(ctx context.Context, p model.SettingsPatch) error {
	writes := map[string]string{}
	if p.BudgetLimitMicros != nil {
		writes["budget_limit_micros"] = fmt.Sprintf("%d", *p.BudgetLimitMicros)
	}
	for k, v := range map[string]*string{
		"foreman_model": p.ForemanModel, "architect_model": p.ArchitectModel,
		"builder_model": p.BuilderModel, "inspector_model": p.InspectorModel, "chat_model": p.ChatModel,
	} {
		if v != nil {
			writes[k] = *v
		}
	}
	if len(writes) == 0 {
		return nil
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range writes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings_kv (key, value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}
