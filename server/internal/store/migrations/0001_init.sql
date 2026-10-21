-- 0001_init: every table from the data model in ARCHITECTURE.md.
-- Timestamps are INTEGER unix milliseconds. Booleans are INTEGER 0/1.
-- AUTOINCREMENT keeps IDs from being reused after a delete: IDs appear in
-- dashboard URLs and name artifact folders (data/runs/<id>/).

CREATE TABLE apps (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL,
    slug              TEXT    NOT NULL UNIQUE,
    base_url          TEXT    NOT NULL,
    notes             TEXT    NOT NULL DEFAULT '',
    allow_destructive INTEGER NOT NULL DEFAULT 0 CHECK (allow_destructive IN (0, 1)),
    created_at        INTEGER NOT NULL
);

CREATE TABLE schedules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id     INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    cron       TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at INTEGER NOT NULL
);
CREATE INDEX schedules_app_id ON schedules(app_id);

CREATE TABLE agent_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL CHECK (kind IN ('probe', 'explore', 'triage', 'heal')),
    status       TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'budget_exceeded', 'cancelled')),
    input_json   TEXT    NOT NULL DEFAULT '{}',
    summary_json TEXT,
    steps        INTEGER NOT NULL DEFAULT 0,
    tokens_in    INTEGER NOT NULL DEFAULT 0,
    tokens_out   INTEGER NOT NULL DEFAULT 0,
    cost_usd     REAL    NOT NULL DEFAULT 0,
    started_at   INTEGER,
    finished_at  INTEGER,
    error        TEXT
);
CREATE INDEX agent_runs_app_id ON agent_runs(app_id);

CREATE TABLE agent_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_run_id INTEGER NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    type         TEXT    NOT NULL,
    payload_json TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (agent_run_id, seq)
);

CREATE TABLE test_cases (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    file_path    TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    origin       TEXT    NOT NULL CHECK (origin IN ('manual', 'generated')),
    status       TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'quarantined', 'retired')),
    agent_run_id INTEGER REFERENCES agent_runs(id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (app_id, file_path, title)
);

CREATE TABLE runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id      INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    trigger     TEXT    NOT NULL CHECK (trigger IN ('manual', 'schedule', 'eval', 'heal')),
    status      TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'passed', 'failed', 'error')),
    total       INTEGER NOT NULL DEFAULT 0,
    passed      INTEGER NOT NULL DEFAULT 0,
    failed      INTEGER NOT NULL DEFAULT 0,
    flaky       INTEGER NOT NULL DEFAULT 0,
    skipped     INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    error       TEXT,
    started_at  INTEGER,
    finished_at INTEGER
);
CREATE INDEX runs_app_id ON runs(app_id);

CREATE TABLE test_results (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id         INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    test_case_id   INTEGER REFERENCES test_cases(id) ON DELETE SET NULL,
    title          TEXT    NOT NULL,
    file_path      TEXT    NOT NULL,
    line           INTEGER NOT NULL DEFAULT 0,
    status         TEXT    NOT NULL CHECK (status IN ('passed', 'failed', 'flaky', 'skipped')),
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    error          TEXT,
    retries        INTEGER NOT NULL DEFAULT 0,
    artifacts_json TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX test_results_run_id ON test_results(run_id);
CREATE INDEX test_results_test_case_id ON test_results(test_case_id);

CREATE TABLE triage_results (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    test_result_id   INTEGER NOT NULL UNIQUE REFERENCES test_results(id) ON DELETE CASCADE,
    agent_run_id     INTEGER REFERENCES agent_runs(id) ON DELETE SET NULL,
    category         TEXT    NOT NULL CHECK (category IN ('real_bug', 'ui_change', 'flaky', 'test_bug', 'environment')),
    confidence       REAL    NOT NULL CHECK (confidence BETWEEN 0 AND 1),
    summary          TEXT    NOT NULL,
    explanation      TEXT    NOT NULL DEFAULT '',
    suggested_action TEXT    NOT NULL DEFAULT '',
    method           TEXT    NOT NULL CHECK (method IN ('rule', 'llm')),
    created_at       INTEGER NOT NULL
);

CREATE TABLE heal_proposals (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id           INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    test_case_id     INTEGER REFERENCES test_cases(id) ON DELETE SET NULL,
    test_result_id   INTEGER REFERENCES test_results(id) ON DELETE SET NULL,
    agent_run_id     INTEGER REFERENCES agent_runs(id) ON DELETE SET NULL,
    file_path        TEXT    NOT NULL,
    base_sha256      TEXT    NOT NULL,
    new_content_path TEXT    NOT NULL,
    patch            TEXT    NOT NULL,
    reasoning        TEXT    NOT NULL DEFAULT '',
    verified         INTEGER NOT NULL DEFAULT 0 CHECK (verified IN (0, 1)),
    status           TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'applied', 'failed')),
    created_at       INTEGER NOT NULL,
    decided_at       INTEGER
);
CREATE INDEX heal_proposals_app_id ON heal_proposals(app_id);
CREATE INDEX heal_proposals_status ON heal_proposals(status);
