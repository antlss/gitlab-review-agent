package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

// sqliteSchema defines all tables in a single string for auto-migration.
const sqliteSchema = `
CREATE TABLE IF NOT EXISTS repository_settings (
    id                  TEXT PRIMARY KEY,
    gitlab_project_id   INTEGER NOT NULL UNIQUE,
    project_path        TEXT NOT NULL,
    model_override      TEXT,
    language            TEXT,
    framework           TEXT,
    custom_prompt       TEXT,
    exclude_patterns    TEXT,
    feedback_count      INTEGER NOT NULL DEFAULT 0,
    is_archived         INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_repo_settings_project_id ON repository_settings(gitlab_project_id);

CREATE TABLE IF NOT EXISTS review_jobs (
    id                        TEXT PRIMARY KEY,
    gitlab_project_id         INTEGER NOT NULL,
    mr_iid                    INTEGER NOT NULL,
    head_sha                  TEXT NOT NULL,
    base_sha                  TEXT,
    target_branch             TEXT NOT NULL DEFAULT '',
    source_branch             TEXT NOT NULL DEFAULT '',
    is_force_push             INTEGER NOT NULL DEFAULT 0,
    dry_run                   INTEGER NOT NULL DEFAULT 0,
    trigger_source            TEXT NOT NULL,
    status                    TEXT NOT NULL DEFAULT 'PENDING',
    review_mode               TEXT,
    prompt_version            TEXT,
    policy_version            TEXT,
    model_plan_version        TEXT,
    findings_budget           INTEGER,
    model_used                TEXT,
    repo_model_override       TEXT,
    repo_language             TEXT,
    repo_framework            TEXT,
    repo_exclude_patterns     TEXT,
    ai_output_raw             TEXT,
    ai_output_parsed          TEXT,
    iterations_used           INTEGER,
    tokens_estimated          INTEGER,
    total_comments_posted     INTEGER,
    total_comments_suppressed INTEGER,
    queued_at                 TEXT NOT NULL,
    started_at                TEXT,
    completed_at              TEXT,
    error_message             TEXT,
    created_at                TEXT NOT NULL,
    updated_at                TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_review_jobs_project_mr_sha ON review_jobs(gitlab_project_id, mr_iid, head_sha);
CREATE INDEX IF NOT EXISTS idx_review_jobs_status ON review_jobs(status);

CREATE TABLE IF NOT EXISTS reply_jobs (
    id                      TEXT PRIMARY KEY,
    gitlab_project_id       INTEGER NOT NULL,
    mr_iid                  INTEGER NOT NULL,
    discussion_id           TEXT NOT NULL,
    trigger_note_id         INTEGER NOT NULL,
    trigger_note_content    TEXT NOT NULL,
    trigger_note_author     TEXT NOT NULL,
    bot_comment_id          INTEGER NOT NULL,
    bot_comment_content     TEXT NOT NULL,
    bot_comment_file_path   TEXT,
    bot_comment_line        INTEGER,
    status                  TEXT NOT NULL DEFAULT 'PENDING',
    reply_content           TEXT,
    intent_classified       TEXT,
    feedback_signal         TEXT,
    thread_state_before     TEXT,
    thread_state_after      TEXT,
    error_message           TEXT,
    queued_at               TEXT NOT NULL,
    started_at              TEXT,
    completed_at            TEXT,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reply_jobs_project ON reply_jobs(gitlab_project_id, mr_iid);

CREATE TABLE IF NOT EXISTS review_feedbacks (
    id                   TEXT PRIMARY KEY,
    gitlab_project_id    INTEGER NOT NULL,
    review_job_id        TEXT,
    gitlab_discussion_id TEXT NOT NULL,
    gitlab_note_id       INTEGER NOT NULL UNIQUE,
    review_mode          TEXT,
    prompt_version       TEXT,
    policy_version       TEXT,
    model_plan_version   TEXT,
    file_path            TEXT,
    line_number          INTEGER,
    category             TEXT,
    comment_summary      TEXT,
    content_hash         TEXT,
    semantic_fingerprint TEXT,
    location_fingerprint TEXT,
    thread_state         TEXT,
    language             TEXT,
    signal               TEXT,
    signal_reply_content TEXT,
    model_used           TEXT,
    consolidated_at      TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_feedbacks_semantic_fingerprint ON review_feedbacks(semantic_fingerprint);
CREATE INDEX IF NOT EXISTS idx_feedbacks_location_fingerprint ON review_feedbacks(location_fingerprint);
CREATE INDEX IF NOT EXISTS idx_feedbacks_content_hash ON review_feedbacks(content_hash);
CREATE INDEX IF NOT EXISTS idx_feedbacks_project ON review_feedbacks(gitlab_project_id);
CREATE INDEX IF NOT EXISTS idx_feedbacks_note_id ON review_feedbacks(gitlab_note_id);

CREATE TABLE IF NOT EXISTS review_records (
    id                  TEXT PRIMARY KEY,
    gitlab_project_id   INTEGER NOT NULL,
    mr_iid              INTEGER NOT NULL,
    review_job_id       TEXT NOT NULL,
    head_sha            TEXT NOT NULL,
    review_mode         TEXT,
    prompt_version      TEXT,
    policy_version      TEXT,
    model_plan_version  TEXT,
    reviewed_files      TEXT NOT NULL,
    comments_posted     INTEGER NOT NULL DEFAULT 0,
    created_at          TEXT NOT NULL,
    UNIQUE(gitlab_project_id, mr_iid)
);
CREATE INDEX IF NOT EXISTS idx_review_records_mr ON review_records(gitlab_project_id, mr_iid);
`

// Connect opens a SQLite database and auto-creates tables.
func Connect(dbPath string) (*sqlx.DB, error) {
	db, err := sqlx.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite is not designed for high concurrency; limit connections.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create sqlite schema: %w", err)
	}
	if err := applySQLiteMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate sqlite schema: %w", err)
	}
	return db, nil
}

func applySQLiteMigrations(db *sqlx.DB) error {
	stmts := []string{
		`ALTER TABLE review_jobs ADD COLUMN review_mode TEXT`,
		`ALTER TABLE review_jobs ADD COLUMN prompt_version TEXT`,
		`ALTER TABLE review_jobs ADD COLUMN policy_version TEXT`,
		`ALTER TABLE review_jobs ADD COLUMN model_plan_version TEXT`,
		`ALTER TABLE review_jobs ADD COLUMN findings_budget INTEGER`,
		`ALTER TABLE reply_jobs ADD COLUMN thread_state_before TEXT`,
		`ALTER TABLE reply_jobs ADD COLUMN thread_state_after TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN review_mode TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN prompt_version TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN policy_version TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN model_plan_version TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN content_hash TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN semantic_fingerprint TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN location_fingerprint TEXT`,
		`ALTER TABLE review_feedbacks ADD COLUMN thread_state TEXT`,
		`ALTER TABLE review_records ADD COLUMN review_mode TEXT`,
		`ALTER TABLE review_records ADD COLUMN prompt_version TEXT`,
		`ALTER TABLE review_records ADD COLUMN policy_version TEXT`,
		`ALTER TABLE review_records ADD COLUMN model_plan_version TEXT`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil && !isSQLiteDuplicateColumn(err) {
			return err
		}
	}
	indexStmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_feedbacks_semantic_fingerprint ON review_feedbacks(semantic_fingerprint)`,
		`CREATE INDEX IF NOT EXISTS idx_feedbacks_location_fingerprint ON review_feedbacks(location_fingerprint)`,
		`CREATE INDEX IF NOT EXISTS idx_feedbacks_content_hash ON review_feedbacks(content_hash)`,
	}
	for _, stmt := range indexStmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func isSQLiteDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

// NullTimeToString is a helper that converts sql.NullTime to a *string for SQLite text columns.
func NullTimeToString(t sql.NullTime) *string {
	if !t.Valid {
		return nil
	}
	s := t.Time.Format("2006-01-02T15:04:05Z07:00")
	return &s
}
