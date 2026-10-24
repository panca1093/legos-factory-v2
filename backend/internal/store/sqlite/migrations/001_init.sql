-- +goose Up
CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    email       TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'ENGINEER', -- OWNER|ADMIN|ENGINEER|VIEWER
    created_at  INTEGER NOT NULL
);

CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE projects (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    repo_url        TEXT NOT NULL DEFAULT '',
    language        TEXT NOT NULL DEFAULT 'go',   -- quality profile key
    quality_profile TEXT NOT NULL DEFAULT '{}',   -- JSON: check commands
    created_at      INTEGER NOT NULL
);

CREATE TABLE work_orders (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL REFERENCES projects(id),
    title           TEXT NOT NULL,
    objective       TEXT NOT NULL,
    acceptance      TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL,
    autonomy        TEXT NOT NULL DEFAULT 'assisted', -- guided|assisted|delegated
    builder_model   TEXT NOT NULL DEFAULT '',
    inspector_model TEXT NOT NULL DEFAULT '',
    clarif_question TEXT NOT NULL DEFAULT '',
    clarif_answer   TEXT NOT NULL DEFAULT '',
    revision        INTEGER NOT NULL DEFAULT 0,
    created_by      TEXT NOT NULL REFERENCES users(id),
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX idx_wo_project ON work_orders(project_id);
CREATE INDEX idx_wo_state ON work_orders(state);

CREATE TABLE plan_tasks (
    id              TEXT PRIMARY KEY,
    work_order_id   TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    title           TEXT NOT NULL,
    objective       TEXT NOT NULL DEFAULT '',
    role            TEXT NOT NULL DEFAULT 'builder',
    created_at      INTEGER NOT NULL
);
CREATE INDEX idx_tasks_wo ON plan_tasks(work_order_id);

CREATE TABLE task_deps (
    task_id     TEXT NOT NULL REFERENCES plan_tasks(id) ON DELETE CASCADE,
    depends_on  TEXT NOT NULL REFERENCES plan_tasks(id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, depends_on)
);

CREATE TABLE runs (
    id              TEXT PRIMARY KEY,
    work_order_id   TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    task_id         TEXT REFERENCES plan_tasks(id) ON DELETE SET NULL,
    kind            TEXT NOT NULL,   -- plan|implement|verify|inspect|fix|evaluate
    state           TEXT NOT NULL,   -- pending|running|completed|failed|cancelled
    harness         TEXT NOT NULL,   -- mock|opencode
    session_ref     TEXT NOT NULL DEFAULT '',
    output          TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    tokens_in       INTEGER NOT NULL DEFAULT 0,
    tokens_out      INTEGER NOT NULL DEFAULT 0,
    cost_micros     INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER,
    finished_at     INTEGER,
    created_at      INTEGER NOT NULL
);
CREATE INDEX idx_runs_wo ON runs(work_order_id);

CREATE TABLE wof_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    wo_id       TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    actor       TEXT NOT NULL,  -- foreman|architect|builder|inspector|human|system
    kind        TEXT NOT NULL,
    message     TEXT NOT NULL,
    payload     TEXT NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_events_wo ON wof_events(wo_id);

CREATE TABLE findings (
    id          TEXT PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    wo_id       TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    severity    TEXT NOT NULL,  -- blocker|major|minor|info
    title       TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    location    TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open', -- open|resolved|disputed
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_findings_wo ON findings(wo_id);

CREATE TABLE quality_checks (
    id          TEXT PRIMARY KEY,
    wo_id       TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    command     TEXT NOT NULL,
    outcome     TEXT NOT NULL,  -- PASS|FAIL|UNVERIFIED
    exit_code   INTEGER,
    output      TEXT NOT NULL DEFAULT '',
    revision    INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_qc_wo ON quality_checks(wo_id);

CREATE TABLE jobs (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    wo_id       TEXT NOT NULL REFERENCES work_orders(id) ON DELETE CASCADE,
    task_id     TEXT,
    payload     TEXT NOT NULL DEFAULT '{}',
    state       TEXT NOT NULL DEFAULT 'PENDING', -- PENDING|CLAIMED|RUNNING|COMPLETED|FAILED|CANCELLED
    attempts    INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    run_after   INTEGER NOT NULL DEFAULT 0,
    claimed_by  TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX idx_jobs_state ON jobs(state);

CREATE TABLE usage_records (
    id          TEXT PRIMARY KEY,
    wo_id       TEXT REFERENCES work_orders(id) ON DELETE SET NULL,
    user_id     TEXT,
    role        TEXT NOT NULL,       -- builder|inspector|architect|foreman
    provider    TEXT NOT NULL,
    model       TEXT NOT NULL,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    cost_micros INTEGER NOT NULL DEFAULT 0,
    source      TEXT NOT NULL DEFAULT 'measured', -- measured|estimated|unknown
    created_at  INTEGER NOT NULL
);

CREATE TABLE settings_kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE idempotency (
    key         TEXT PRIMARY KEY,
    created_at  INTEGER NOT NULL
);
