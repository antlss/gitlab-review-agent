package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type ReviewJobStore struct {
	db *sqlx.DB
}

func NewReviewJobStore(db *sqlx.DB) *ReviewJobStore {
	return &ReviewJobStore{db: db}
}

func (s *ReviewJobStore) Create(ctx context.Context, job *domain.ReviewJob) error {
	if job.ID == (uuid.UUID{}) {
		return fmt.Errorf("review job ID must be set before calling Create")
	}
	now := time.Now()
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.QueuedAt.IsZero() {
		job.QueuedAt = now
	}

	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO review_jobs (
			id, gitlab_project_id, mr_iid, head_sha, base_sha,
			target_branch, source_branch, is_force_push, dry_run,
			trigger_source, status, repo_model_override, repo_language,
			repo_framework, repo_exclude_patterns,
			queued_at, created_at, updated_at
		) VALUES (
			:id, :gitlab_project_id, :mr_iid, :head_sha, :base_sha,
			:target_branch, :source_branch, :is_force_push, :dry_run,
			:trigger_source, :status, :repo_model_override, :repo_language,
			:repo_framework, :repo_exclude_patterns,
			:queued_at, :created_at, :updated_at
		)`, job)
	if err != nil {
		return fmt.Errorf("create review job: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) GetByID(ctx context.Context, id uuid.UUID) (*domain.ReviewJob, error) {
	var job domain.ReviewJob
	err := s.db.GetContext(ctx, &job,
		`SELECT * FROM review_jobs WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get review job: %w", err)
	}
	return &job, nil
}

func (s *ReviewJobStore) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ReviewJobStatus, errMsg *string) error {
	query := `UPDATE review_jobs SET status = $1, error_message = $2, updated_at = NOW()`
	if status == domain.ReviewJobStatusReviewing {
		query += `, started_at = NOW()`
	}
	if status == domain.ReviewJobStatusCompleted || status == domain.ReviewJobStatusFailed ||
		status == domain.ReviewJobStatusParseFailed || status == domain.ReviewJobStatusSkippedSize {
		query += `, completed_at = NOW()`
	}
	query += ` WHERE id = $3`
	_, err := s.db.ExecContext(ctx, query, status, errMsg, id)
	if err != nil {
		return fmt.Errorf("update review job status: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateBaseSHA(ctx context.Context, id uuid.UUID, baseSHA string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE review_jobs SET base_sha = $1, updated_at = NOW() WHERE id = $2`,
		baseSHA, id)
	if err != nil {
		return fmt.Errorf("update base sha: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateAIOutput(ctx context.Context, id uuid.UUID, raw string, parsed []domain.ParsedComment, iterations, tokens int) error {
	parsedJSON, err := json.Marshal(parsed)
	if err != nil {
		return fmt.Errorf("marshal parsed comments: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE review_jobs SET
			ai_output_raw = $1, ai_output_parsed = $2,
			iterations_used = $3, tokens_estimated = $4,
			updated_at = NOW()
		WHERE id = $5`,
		raw, parsedJSON, iterations, tokens, id)
	if err != nil {
		return fmt.Errorf("update ai output: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateCompleted(ctx context.Context, id uuid.UUID, posted, suppressed int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE review_jobs SET
			status = $1, total_comments_posted = $2, total_comments_suppressed = $3,
			completed_at = NOW(), updated_at = NOW()
		WHERE id = $4`,
		domain.ReviewJobStatusCompleted, posted, suppressed, id)
	if err != nil {
		return fmt.Errorf("update completed: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateModelUsed(ctx context.Context, id uuid.UUID, model string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE review_jobs SET model_used = $1, updated_at = NOW() WHERE id = $2`,
		model, id)
	if err != nil {
		return fmt.Errorf("update model used: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) ExistsPendingOrCompleted(ctx context.Context, projectID, mrIID int64, headSHA string, withinMinutes int) (bool, error) {
	var exists bool
	err := s.db.GetContext(ctx, &exists, `
			SELECT EXISTS(
				SELECT 1 FROM review_jobs
				WHERE gitlab_project_id = $1 AND mr_iid = $2 AND head_sha = $3
				AND status IN ('PENDING', 'REVIEWING', 'POSTING', 'COMPLETED')
				AND created_at > NOW() - make_interval(mins := $4)
			)`, projectID, mrIID, headSHA, withinMinutes)
	if err != nil {
		return false, fmt.Errorf("check existing job: %w", err)
	}
	return exists, nil
}

func (s *ReviewJobStore) ListStale(ctx context.Context, olderThanMinutes int) ([]*domain.ReviewJob, error) {
	var jobs []*domain.ReviewJob
	err := s.db.SelectContext(ctx, &jobs, `
		SELECT * FROM review_jobs
		WHERE status = $1
		AND started_at < NOW() - make_interval(mins := $2)`,
		domain.ReviewJobStatusReviewing, olderThanMinutes)
	if err != nil {
		return nil, fmt.Errorf("list stale jobs: %w", err)
	}
	return jobs, nil
}

func (s *ReviewJobStore) ListByProject(ctx context.Context, projectID int64, limit int) ([]*domain.ReviewJob, error) {
	var jobs []*domain.ReviewJob
	err := s.db.SelectContext(ctx, &jobs,
		`SELECT * FROM review_jobs WHERE gitlab_project_id = $1 ORDER BY created_at DESC LIMIT $2`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs by project: %w", err)
	}
	return jobs, nil
}
