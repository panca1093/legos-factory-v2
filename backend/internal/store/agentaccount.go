package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"legos-factory/backend/internal/model"
)

// AgentAccountRepo owns agent_accounts and agent_settings.
type AgentAccountRepo struct{ DB *sql.DB }

const acctCols = `id, user_id, agent, label, home_dir, active, created_at`

func scanAccount(scan rowScanner) (*model.AgentAccount, error) {
	var a model.AgentAccount
	var active int
	var created int64
	if err := scan(&a.ID, &a.UserID, &a.Agent, &a.Label, &a.HomeDir, &active, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, model.ErrAgentAccountNotFound
		}
		return nil, err
	}
	a.Active, a.CreatedAt = active != 0, time.Unix(created, 0)
	return &a, nil
}

func (r *AgentAccountRepo) Create(ctx context.Context, a *model.AgentAccount) error {
	a.CreatedAt = time.Now()
	_, err := r.DB.ExecContext(ctx, `INSERT INTO agent_accounts (`+acctCols+`) VALUES (?,?,?,?,?,0,?)`,
		a.ID, a.UserID, a.Agent, a.Label, a.HomeDir, a.CreatedAt.Unix())
	return err
}

// Get returns the user's own account; another user's is "not found".
func (r *AgentAccountRepo) Get(ctx context.Context, userID, agent, id string) (*model.AgentAccount, error) {
	return scanAccount(r.DB.QueryRowContext(ctx, `SELECT `+acctCols+` FROM agent_accounts WHERE id = ? AND user_id = ? AND agent = ?`, id, userID, agent).Scan)
}

func (r *AgentAccountRepo) List(ctx context.Context, userID, agent string) ([]model.AgentAccount, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT `+acctCols+` FROM agent_accounts WHERE user_id = ? AND agent = ? ORDER BY created_at, id`, userID, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AgentAccount
	for rows.Next() {
		a, err := scanAccount(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// Active returns the user's active account for the agent, or nil for the system default.
func (r *AgentAccountRepo) Active(ctx context.Context, userID, agent string) (*model.AgentAccount, error) {
	a, err := scanAccount(r.DB.QueryRowContext(ctx, `SELECT `+acctCols+` FROM agent_accounts WHERE user_id = ? AND agent = ? AND active = 1`, userID, agent).Scan)
	if errors.Is(err, model.ErrAgentAccountNotFound) {
		return nil, nil
	}
	return a, err
}

// Activate makes id the user's only active account for the agent; an empty id selects the system default.
func (r *AgentAccountRepo) Activate(ctx context.Context, userID, agent, id string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if id != "" {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_accounts WHERE id = ? AND user_id = ? AND agent = ?`, id, userID, agent).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return model.ErrAgentAccountNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_accounts SET active = 0 WHERE user_id = ? AND agent = ?`, userID, agent); err != nil {
		return err
	}
	if id != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_accounts SET active = 1 WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete removes the account; deleting the active one leaves the system default active.
func (r *AgentAccountRepo) Delete(ctx context.Context, userID, agent, id string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM agent_accounts WHERE id = ? AND user_id = ? AND agent = ?`, id, userID, agent)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrAgentAccountNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_role_accounts WHERE user_id = ? AND agent = ? AND account_id = ?`, userID, agent, id); err != nil {
		return err
	}
	return tx.Commit()
}

// RoleAccounts maps each role the user has assigned for the agent to its account id ("system" is the virtual default).
func (r *AgentAccountRepo) RoleAccounts(ctx context.Context, userID, agent string) (map[string]string, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT role, account_id FROM agent_role_accounts WHERE user_id = ? AND agent = ?`, userID, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var role, id string
		if err := rows.Scan(&role, &id); err != nil {
			return nil, err
		}
		out[role] = id
	}
	return out, rows.Err()
}

// SetRoleAccounts makes accountID serve exactly roles: they leave the user's other accounts of the agent, and the account's other roles are cleared.
func (r *AgentAccountRepo) SetRoleAccounts(ctx context.Context, userID, agent, accountID string, roles []string) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_role_accounts WHERE user_id = ? AND agent = ? AND account_id = ?`, userID, agent, accountID); err != nil {
		return err
	}
	for _, role := range roles {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_role_accounts (user_id, agent, role, account_id) VALUES (?,?,?,?)
			ON CONFLICT(user_id, agent, role) DO UPDATE SET account_id = excluded.account_id`, userID, agent, role, accountID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Enabled reports the agent's switch; an agent never switched is enabled.
func (r *AgentAccountRepo) Enabled(ctx context.Context, agent string) (bool, error) {
	var v int
	err := r.DB.QueryRowContext(ctx, `SELECT enabled FROM agent_settings WHERE agent = ?`, agent).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return v != 0, err
}

func (r *AgentAccountRepo) SetEnabled(ctx context.Context, agent string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := r.DB.ExecContext(ctx, `INSERT INTO agent_settings (agent, enabled) VALUES (?,?)
		ON CONFLICT(agent) DO UPDATE SET enabled = excluded.enabled`, agent, v)
	return err
}
