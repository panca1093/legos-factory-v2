// Package profile holds credentials, model profiles, role bindings and run-time env for agents; config state, not facts.
package profile

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"legos-factory/factory/internal/harness"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrSingleOwnerOnly  = errors.New("subscription credentials require single-owner mode")
	ErrUnverified       = errors.New("profile has not passed the smoke test")
	ErrNoBinding        = errors.New("no profile bound to role")
	ErrAuthFileRequired = errors.New("this credential needs a login file in the sandbox, which is not supported yet")
)

type Config struct {
	DB          string // SQLite file; may be the facts DB
	Key         []byte // 32 bytes
	SingleOwner bool
	PlansFile   string // optional; written with defaults when missing
	HTTP        *http.Client
}

// Credential deliberately has no secret field; read it with Store.Secret.
type Credential struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"` // "api_key" | "subscription"
	Provider    string `json:"provider"`
	BaseURL     string `json:"base_url,omitempty"`
	Concurrency int    `json:"concurrency"`
}

type Price struct {
	InputPerMTok      float64 `json:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok"`
}

type Profile struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Agent          string     `json:"agent"`
	CredentialID   string     `json:"credential_id"`
	Model          string     `json:"model"`
	Price          Price      `json:"price"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	ModelUnchecked bool       `json:"model_unchecked,omitempty"` // custom provider whose catalog could not be fetched
}

type Store struct {
	db          *sql.DB
	aead        cipher.AEAD
	singleOwner bool
	plansFile   string
	http        *http.Client
	now         func() time.Time

	mu   sync.Mutex
	sems map[string]chan struct{}
}

const schema = `
CREATE TABLE IF NOT EXISTS credentials (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL, provider TEXT NOT NULL,
  base_url TEXT NOT NULL DEFAULT '', concurrency INTEGER NOT NULL DEFAULT 1, secret BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS profiles (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, agent TEXT NOT NULL, credential_id TEXT NOT NULL, model TEXT NOT NULL,
  price_in REAL NOT NULL DEFAULT 0, price_out REAL NOT NULL DEFAULT 0, price_cache_read REAL NOT NULL DEFAULT 0,
  price_cache_write REAL NOT NULL DEFAULT 0, verified_at TEXT, model_unchecked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS role_bindings (
  project TEXT NOT NULL, role TEXT NOT NULL, profile_id TEXT NOT NULL, PRIMARY KEY (project, role));`

func Open(cfg Config) (*Store, error) {
	block, err := aes.NewCipher(cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("secret key must be 32 bytes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", cfg.DB+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	c := cfg.HTTP
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	return &Store{db: db, aead: aead, singleOwner: cfg.SingleOwner, plansFile: cfg.PlansFile, http: c,
		now: time.Now, sems: map[string]chan struct{}{}}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func newID(prefix string) string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (s *Store) seal(plain []byte) []byte {
	nonce := make([]byte, s.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return s.aead.Seal(nonce, nonce, plain, nil)
}

func (s *Store) open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("sealed credential is truncated")
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, errors.New("credential cannot be decrypted with the configured key")
	}
	return plain, nil
}

func (s *Store) CreateCredential(ctx context.Context, c Credential, secret string) (Credential, error) {
	switch c.Kind {
	case "api_key":
	case "subscription":
		if !s.singleOwner {
			return Credential{}, ErrSingleOwnerOnly
		}
	default:
		return Credential{}, fmt.Errorf("unknown credential kind %q", c.Kind)
	}
	if c.Name == "" || c.Provider == "" || secret == "" {
		return Credential{}, errors.New("name, provider and secret are required")
	}
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	c.ID = newID("cred-")
	_, err := s.db.ExecContext(ctx, `INSERT INTO credentials(id,name,kind,provider,base_url,concurrency,secret) VALUES(?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Kind, c.Provider, c.BaseURL, c.Concurrency, s.seal([]byte(secret)))
	return c, err
}

const credCols = `id,name,kind,provider,base_url,concurrency`

func scanCred(r interface{ Scan(...any) error }) (Credential, error) {
	var c Credential
	err := r.Scan(&c.ID, &c.Name, &c.Kind, &c.Provider, &c.BaseURL, &c.Concurrency)
	return c, err
}

func (s *Store) GetCredential(ctx context.Context, id string) (Credential, error) {
	c, err := scanCred(s.db.QueryRowContext(ctx, `SELECT `+credCols+` FROM credentials WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("credential %s: %w", id, ErrNotFound)
	}
	return c, err
}

func (s *Store) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+credCols+` FROM credentials ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE credential_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("credential %s is used by %d profile(s)", id, n)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id=?`, id)
	return err
}

// Secret is the only way to read a credential's secret.
func (s *Store) Secret(ctx context.Context, id string) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT secret FROM credentials WHERE id=?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("credential %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	plain, err := s.open(sealed)
	return string(plain), err
}

const profCols = `id,name,agent,credential_id,model,price_in,price_out,price_cache_read,price_cache_write,verified_at,model_unchecked`

func scanProfile(r interface{ Scan(...any) error }) (Profile, error) {
	var p Profile
	var verified sql.NullString
	err := r.Scan(&p.ID, &p.Name, &p.Agent, &p.CredentialID, &p.Model, &p.Price.InputPerMTok, &p.Price.OutputPerMTok,
		&p.Price.CacheReadPerMTok, &p.Price.CacheWritePerMTok, &verified, &p.ModelUnchecked)
	if err == nil && verified.Valid {
		t, perr := time.Parse(time.RFC3339Nano, verified.String)
		if perr != nil {
			return p, perr
		}
		p.VerifiedAt = &t
	}
	return p, err
}

// CreateProfile validates agent and model against the credential's compatibility matrix and catalog.
func (s *Store) CreateProfile(ctx context.Context, p Profile) (Profile, error) {
	if p.Name == "" || p.Model == "" {
		return Profile{}, errors.New("name and model are required")
	}
	cred, err := s.GetCredential(ctx, p.CredentialID)
	if err != nil {
		return Profile{}, err
	}
	if !contains(Agents(cred), p.Agent) {
		return Profile{}, fmt.Errorf("agent %q cannot use credential %s (%s); allowed: %v", p.Agent, cred.Name, cred.Provider, Agents(cred))
	}
	models, err := s.Models(ctx, cred)
	switch {
	case err == nil:
		if !hasModel(models, p.Model) {
			return Profile{}, fmt.Errorf("model %q is not offered by credential %s", p.Model, cred.Name)
		}
	case isCustom(cred):
		p.ModelUnchecked = true
	default:
		return Profile{}, fmt.Errorf("list models: %w", err)
	}
	p.ID, p.VerifiedAt = newID("prof-"), nil
	_, err = s.db.ExecContext(ctx, `INSERT INTO profiles(`+profCols+`) VALUES(?,?,?,?,?,?,?,?,?,NULL,?)`,
		p.ID, p.Name, p.Agent, p.CredentialID, p.Model, p.Price.InputPerMTok, p.Price.OutputPerMTok,
		p.Price.CacheReadPerMTok, p.Price.CacheWritePerMTok, p.ModelUnchecked)
	return p, err
}

func (s *Store) GetProfile(ctx context.Context, id string) (Profile, error) {
	p, err := scanProfile(s.db.QueryRowContext(ctx, `SELECT `+profCols+` FROM profiles WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("profile %s: %w", id, ErrNotFound)
	}
	return p, err
}

func (s *Store) ListProfiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+profCols+` FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM role_bindings WHERE profile_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM profiles WHERE id=?`, id)
	return err
}

func (s *Store) markVerified(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE profiles SET verified_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), id)
	return err
}

// Bind sets the profile for a role; projectID "" is the global default. Builder needs a verified profile.
func (s *Store) Bind(ctx context.Context, projectID string, role harness.Role, profileID string) error {
	p, err := s.GetProfile(ctx, profileID)
	if err != nil {
		return err
	}
	if role == harness.RoleBuilder && p.VerifiedAt == nil {
		return fmt.Errorf("bind builder to %s: %w", p.Name, ErrUnverified)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO role_bindings(project,role,profile_id) VALUES(?,?,?)
		ON CONFLICT(project,role) DO UPDATE SET profile_id=excluded.profile_id`, projectID, string(role), profileID)
	return err
}

// Resolve prefers the project binding and falls back to the global one.
func (s *Store) Resolve(ctx context.Context, projectID string, role harness.Role) (Profile, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT profile_id FROM role_bindings WHERE role=? AND project IN (?, '')
		ORDER BY project='' LIMIT 1`, string(role), projectID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, fmt.Errorf("%s: %w", role, ErrNoBinding)
	}
	if err != nil {
		return Profile{}, err
	}
	return s.GetProfile(ctx, id)
}

// Acquire blocks until the credential has a free slot (Credential.Concurrency, default 1).
func (s *Store) Acquire(ctx context.Context, credID string) (func(), error) {
	s.mu.Lock()
	sem, ok := s.sems[credID]
	s.mu.Unlock()
	if !ok {
		c, err := s.GetCredential(ctx, credID)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		if sem, ok = s.sems[credID]; !ok {
			sem = make(chan struct{}, max(c.Concurrency, 1))
			s.sems[credID] = sem
		}
		s.mu.Unlock()
	}
	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-sem }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Cost prices usage with the profile's own rates; the harness's cost estimate is never used.
func Cost(p Price, u harness.Usage) float64 {
	return (float64(u.InputTokens)*p.InputPerMTok + float64(u.OutputTokens)*p.OutputPerMTok +
		float64(u.CacheReadTokens)*p.CacheReadPerMTok + float64(u.CacheCreateTokens)*p.CacheWritePerMTok) / 1e6
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
