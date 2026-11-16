package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
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
		case "budget_limit_micros": // retired setting; rows may still exist
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
		case "role_agents":
			_ = json.Unmarshal([]byte(v), &s.RoleAgents)
		case "role_permissions":
			_ = json.Unmarshal([]byte(v), &s.RolePermissions)
		case "max_concurrent_runs":
			fmt.Sscanf(v, "%d", &s.MaxConcurrentRuns)
		case "jev_enabled":
			s.JevEnabled, s.JevEnabledSet = v == "1", true
		case "jev_key":
			if b, err := base64.StdEncoding.DecodeString(v); err == nil && len(b) > 0 {
				s.JevKeySealed = b
				s.JevKeySet = true
			}
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
		"foreman_model":   s.ForemanModel,
		"architect_model": s.ArchitectModel,
		"builder_model":   s.BuilderModel,
		"inspector_model": s.InspectorModel,
		"chat_model":      s.ChatModel,
	}
	if s.AllowedModels != nil {
		writes["allowed_models"] = strings.Trim(strings.Join(s.AllowedModels, ","), ",")
	}
	if s.RoleAgents != nil {
		b, _ := json.Marshal(s.RoleAgents)
		writes["role_agents"] = string(b)
	}
	if s.RolePermissions != nil {
		b, _ := json.Marshal(s.RolePermissions)
		writes["role_permissions"] = string(b)
	}
	if s.MaxConcurrentRuns > 0 {
		writes["max_concurrent_runs"] = fmt.Sprintf("%d", s.MaxConcurrentRuns)
	}
	return r.upsert(ctx, writes)
}

// Patch writes only the keys p sets; a role map replaces the stored one, so callers merge first.
func (r *SettingsRepo) Patch(ctx context.Context, p model.SettingsPatch) error {
	return r.PatchSealed(ctx, p, nil)
}

// PatchSealed is Patch plus the already sealed TypeSafe key: nil leaves it, an empty slice clears it.
// p.JevKey is ignored; sealing belongs to the caller.
func (r *SettingsRepo) PatchSealed(ctx context.Context, p model.SettingsPatch, sealedJevKey []byte) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	writes := map[string]string{}
	for k, v := range map[string]*string{
		"foreman_model": p.ForemanModel, "architect_model": p.ArchitectModel,
		"builder_model": p.BuilderModel, "inspector_model": p.InspectorModel, "chat_model": p.ChatModel,
	} {
		if v != nil {
			writes[k] = *v
		}
	}
	if p.MaxConcurrentRuns != nil {
		writes["max_concurrent_runs"] = fmt.Sprintf("%d", *p.MaxConcurrentRuns)
	}
	if p.JevEnabled != nil {
		writes["jev_enabled"] = map[bool]string{true: "1", false: "0"}[*p.JevEnabled]
	}
	if sealedJevKey != nil {
		writes["jev_key"] = base64.StdEncoding.EncodeToString(sealedJevKey)
	}
	for key, m := range map[string]map[string]string{"role_agents": p.RoleAgents, "role_permissions": p.RolePermissions} {
		if m != nil {
			b, _ := json.Marshal(m)
			writes[key] = string(b)
		}
	}
	for k, v := range writes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings_kv (key, value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SettingsRepo) upsert(ctx context.Context, writes map[string]string) error {
	for k, v := range writes {
		if _, err := r.DB.ExecContext(ctx, `INSERT INTO settings_kv (key, value) VALUES (?,?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return nil
}
