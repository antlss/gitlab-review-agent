package postgres

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

// postgresSchema contains all DDL statements with IF NOT EXISTS / DO $$ guards,
// so it is safe to run on every startup.
const postgresSchema = `
-- ENUMs (idempotent via DO $$ ... EXCEPTION block)
DO $$ BEGIN
    CREATE TYPE review_job_status AS ENUM (
        'PENDING', 'REVIEWING', 'POSTING', 'COMPLETED',
        'FAILED', 'PARSE_FAILED', 'SKIPPED_SIZE'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE trigger_source AS ENUM ('webhook', 'cli', 'queue_retry');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE reply_job_status AS ENUM ('PENDING', 'PROCESSING', 'COMPLETED', 'FAILED');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE feedback_signal AS ENUM ('ACCEPTED', 'REJECTED', 'NEUTRAL');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Tables
CREATE TABLE IF NOT EXISTS repository_settings (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    gitlab_project_id   BIGINT NOT NULL UNIQUE,
    project_path        VARCHAR(500) NOT NULL,
    model_override      VARCHAR(50),
    language            VARCHAR(50),
    framework           VARCHAR(100),
    custom_prompt       TEXT,
    exclude_patterns    TEXT,
    feedback_count      INTEGER NOT NULL DEFAULT 0,
    is_archived         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_repo_settings_project_id ON repository_settings(gitlab_project_id);

CREATE TABLE IF NOT EXISTS review_jobs (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    gitlab_project_id         BIGINT NOT NULL,
    mr_iid                    BIGINT NOT NULL,
    head_sha                  VARCHAR(40) NOT NULL,
    base_sha                  VARCHAR(40),
    target_branch             VARCHAR(255) NOT NULL,
    source_branch             VARCHAR(255) NOT NULL,
    is_force_push             BOOLEAN NOT NULL DEFAULT FALSE,
    dry_run                   BOOLEAN NOT NULL DEFAULT FALSE,
    trigger_source            trigger_source NOT NULL,
    status                    review_job_status NOT NULL DEFAULT 'PENDING',
    review_mode               VARCHAR(20),
    prompt_version            VARCHAR(50),
    policy_version            VARCHAR(50),
    model_plan_version        VARCHAR(50),
    findings_budget           INTEGER,
    model_used                VARCHAR(50),
    repo_model_override       VARCHAR(50),
    repo_language             VARCHAR(50),
    repo_framework            VARCHAR(100),
    repo_exclude_patterns     TEXT,
    ai_output_raw             TEXT,
    ai_output_parsed          JSONB,
    iterations_used           INTEGER,
    tokens_estimated          INTEGER,
    total_comments_posted     INTEGER,
    total_comments_suppressed INTEGER,
    queued_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at                TIMESTAMPTZ,
    completed_at              TIMESTAMPTZ,
    error_message             TEXT,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS review_mode VARCHAR(20);
ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS prompt_version VARCHAR(50);
ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS policy_version VARCHAR(50);
ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS model_plan_version VARCHAR(50);
ALTER TABLE review_jobs ADD COLUMN IF NOT EXISTS findings_budget INTEGER;
CREATE INDEX IF NOT EXISTS idx_review_jobs_project_mr_sha ON review_jobs(gitlab_project_id, mr_iid, head_sha);
CREATE INDEX IF NOT EXISTS idx_review_jobs_status ON review_jobs(status);
CREATE INDEX IF NOT EXISTS idx_review_jobs_project_status ON review_jobs(gitlab_project_id, status);

CREATE TABLE IF NOT EXISTS reply_jobs (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    gitlab_project_id       BIGINT NOT NULL,
    mr_iid                  BIGINT NOT NULL,
    discussion_id           VARCHAR(255) NOT NULL,
    trigger_note_id         BIGINT NOT NULL,
    trigger_note_content    TEXT NOT NULL,
    trigger_note_author     VARCHAR(255) NOT NULL,
    bot_comment_id          BIGINT NOT NULL,
    bot_comment_content     TEXT NOT NULL,
    bot_comment_file_path   VARCHAR(1000),
    bot_comment_line        INTEGER,
    status                  reply_job_status NOT NULL DEFAULT 'PENDING',
    reply_content           TEXT,
    intent_classified       VARCHAR(50),
    feedback_signal         VARCHAR(20),
    thread_state_before     VARCHAR(32),
    thread_state_after      VARCHAR(32),
    error_message           TEXT,
    queued_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at              TIMESTAMPTZ,
    completed_at            TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE reply_jobs ADD COLUMN IF NOT EXISTS thread_state_before VARCHAR(32);
ALTER TABLE reply_jobs ADD COLUMN IF NOT EXISTS thread_state_after VARCHAR(32);
CREATE INDEX IF NOT EXISTS idx_reply_jobs_project ON reply_jobs(gitlab_project_id, mr_iid);
CREATE INDEX IF NOT EXISTS idx_reply_jobs_status ON reply_jobs(status);
CREATE INDEX IF NOT EXISTS idx_reply_jobs_discussion ON reply_jobs(discussion_id);

CREATE TABLE IF NOT EXISTS review_feedbacks (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    gitlab_project_id    BIGINT NOT NULL,
    review_job_id        UUID REFERENCES review_jobs(id),
    gitlab_discussion_id VARCHAR(255) NOT NULL,
    gitlab_note_id       BIGINT NOT NULL UNIQUE,
    review_mode          VARCHAR(20),
    prompt_version       VARCHAR(50),
    policy_version       VARCHAR(50),
    model_plan_version   VARCHAR(50),
    file_path            VARCHAR(1000),
    line_number          INTEGER,
    category             VARCHAR(50),
    comment_summary      TEXT,
    content_hash         VARCHAR(64),
    semantic_fingerprint VARCHAR(64),
    location_fingerprint VARCHAR(64),
    language             VARCHAR(50),
    signal               VARCHAR(20),
    signal_reply_content TEXT,
    model_used           VARCHAR(50),
    consolidated_at      TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS review_mode VARCHAR(20);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS prompt_version VARCHAR(50);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS policy_version VARCHAR(50);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS model_plan_version VARCHAR(50);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS content_hash VARCHAR(64);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS semantic_fingerprint VARCHAR(64);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS location_fingerprint VARCHAR(64);
ALTER TABLE review_feedbacks ADD COLUMN IF NOT EXISTS thread_state VARCHAR(32);
CREATE INDEX IF NOT EXISTS idx_feedbacks_project ON review_feedbacks(gitlab_project_id);
CREATE INDEX IF NOT EXISTS idx_feedbacks_semantic_fingerprint ON review_feedbacks(semantic_fingerprint);
CREATE INDEX IF NOT EXISTS idx_feedbacks_location_fingerprint ON review_feedbacks(location_fingerprint);
CREATE INDEX IF NOT EXISTS idx_feedbacks_content_hash ON review_feedbacks(content_hash);
CREATE INDEX IF NOT EXISTS idx_feedbacks_project_signal ON review_feedbacks(gitlab_project_id, signal);
CREATE INDEX IF NOT EXISTS idx_feedbacks_not_consolidated ON review_feedbacks(gitlab_project_id, consolidated_at)
    WHERE consolidated_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_feedbacks_note_id ON review_feedbacks(gitlab_note_id);

CREATE TABLE IF NOT EXISTS review_records (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    gitlab_project_id   BIGINT NOT NULL,
    mr_iid              BIGINT NOT NULL,
    review_job_id       UUID NOT NULL REFERENCES review_jobs(id),
    head_sha            VARCHAR(40) NOT NULL,
    review_mode         VARCHAR(20),
    prompt_version      VARCHAR(50),
    policy_version      VARCHAR(50),
    model_plan_version  VARCHAR(50),
    reviewed_files      JSONB NOT NULL,
    comments_posted     INTEGER NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_review_records_mr UNIQUE (gitlab_project_id, mr_iid)
);
ALTER TABLE review_records ADD COLUMN IF NOT EXISTS review_mode VARCHAR(20);
ALTER TABLE review_records ADD COLUMN IF NOT EXISTS prompt_version VARCHAR(50);
ALTER TABLE review_records ADD COLUMN IF NOT EXISTS policy_version VARCHAR(50);
ALTER TABLE review_records ADD COLUMN IF NOT EXISTS model_plan_version VARCHAR(50);
CREATE INDEX IF NOT EXISTS idx_review_records_mr ON review_records(gitlab_project_id, mr_iid);
`

// Connect opens a PostgreSQL connection and auto-creates schema.
func Connect(databaseURL string, maxOpen, maxIdle int) (*sqlx.DB, error) {
	db, err := sqlx.Connect("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)

	if _, err := db.Exec(postgresSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create postgres schema: %w", err)
	}

	return db, nil
}
