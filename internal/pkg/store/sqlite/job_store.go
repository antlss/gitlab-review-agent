package sqlite

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
	domain.EnsureReviewJobVersionDefaults(job)
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.QueuedAt.IsZero() {
		job.QueuedAt = now
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_jobs (
			id, gitlab_project_id, mr_iid, head_sha, base_sha,
			target_branch, source_branch, is_force_push, dry_run,
			trigger_source, status, review_mode, prompt_version, policy_version,
			model_plan_version, findings_budget, repo_model_override, repo_language,
			repo_framework, repo_exclude_patterns,
			queued_at, created_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?,
			?, ?, ?
		)`,
		job.ID.String(), job.GitLabProjectID, job.MrIID, job.HeadSHA, job.BaseSHA,
		job.TargetBranch, job.SourceBranch, domain.BoolToInt(job.IsForcePush), domain.BoolToInt(job.DryRun),
		string(job.TriggerSource), string(job.Status), job.ReviewMode, job.PromptVersion, job.PolicyVersion,
		job.ModelPlanVersion, job.FindingsBudget, job.RepoModelOverride, job.RepoLanguage,
		job.RepoFramework, job.RepoExcludePatterns,
		now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create review job: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) GetByID(ctx context.Context, id uuid.UUID) (*domain.ReviewJob, error) {
	var job domain.ReviewJob
	err := s.db.GetContext(ctx, &job,
		`SELECT * FROM review_jobs WHERE id = ?`, id.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get review job: %w", err)
	}
	return &job, nil
}

func (s *ReviewJobStore) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ReviewJobStatus, errMsg *string) error {
	now := time.Now().Format(time.RFC3339)
	query := `UPDATE review_jobs SET status = ?, error_message = ?, updated_at = ?`
	args := []any{string(status), errMsg, now}
	if status == domain.ReviewJobStatusReviewing {
		query += `, started_at = ?`
		args = append(args, now)
	}
	if status == domain.ReviewJobStatusCompleted || status == domain.ReviewJobStatusFailed ||
		status == domain.ReviewJobStatusParseFailed || status == domain.ReviewJobStatusSkippedSize {
		query += `, completed_at = ?`
		args = append(args, now)
	}
	query += ` WHERE id = ?`
	args = append(args, id.String())
	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update review job status: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateBaseSHA(ctx context.Context, id uuid.UUID, baseSHA string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE review_jobs SET base_sha = ?, updated_at = ? WHERE id = ?`,
		baseSHA, time.Now().Format(time.RFC3339), id.String())
	if err != nil {
		return fmt.Errorf("update base sha: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateSessionMetadata(ctx context.Context, id uuid.UUID, reviewMode, promptVersion, policyVersion, modelPlanVersion string, findingsBudget int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE review_jobs SET review_mode = ?, prompt_version = ?, policy_version = ?, model_plan_version = ?, findings_budget = ?, updated_at = ? WHERE id = ?`,
		reviewMode, promptVersion, policyVersion, modelPlanVersion, findingsBudget, time.Now().Format(time.RFC3339), id.String())
	if err != nil {
		return fmt.Errorf("update session metadata: %w", err)
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
			ai_output_raw = ?, ai_output_parsed = ?,
			iterations_used = ?, tokens_estimated = ?,
			updated_at = ?
		WHERE id = ?`,
		raw, string(parsedJSON), iterations, tokens,
		time.Now().Format(time.RFC3339), id.String())
	if err != nil {
		return fmt.Errorf("update ai output: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateCompleted(ctx context.Context, id uuid.UUID, posted, suppressed int) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		UPDATE review_jobs SET
			status = ?, total_comments_posted = ?, total_comments_suppressed = ?,
			completed_at = ?, updated_at = ?
		WHERE id = ?`,
		string(domain.ReviewJobStatusCompleted), posted, suppressed, now, now, id.String())
	if err != nil {
		return fmt.Errorf("update completed: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) UpdateModelUsed(ctx context.Context, id uuid.UUID, model string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE review_jobs SET model_used = ?, updated_at = ? WHERE id = ?`,
		model, time.Now().Format(time.RFC3339), id.String())
	if err != nil {
		return fmt.Errorf("update model used: %w", err)
	}
	return nil
}

func (s *ReviewJobStore) ExistsPendingOrCompleted(ctx context.Context, projectID, mrIID int64, headSHA string, withinMinutes int) (bool, error) {
	cutoff := time.Now().Add(-time.Duration(withinMinutes) * time.Minute).Format(time.RFC3339)
	var count int
	err := s.db.GetContext(ctx, &count, `
			SELECT COUNT(*) FROM review_jobs
			WHERE gitlab_project_id = ? AND mr_iid = ? AND head_sha = ?
			AND status IN ('PENDING', 'REVIEWING', 'POSTING', 'COMPLETED')
			AND created_at > ?`,
		projectID, mrIID, headSHA, cutoff)
	if err != nil {
		return false, fmt.Errorf("check existing job: %w", err)
	}
	return count > 0, nil
}

func (s *ReviewJobStore) ListStale(ctx context.Context, olderThanMinutes int) ([]*domain.ReviewJob, error) {
	cutoff := time.Now().Add(-time.Duration(olderThanMinutes) * time.Minute).Format(time.RFC3339)
	var jobs []*domain.ReviewJob
	err := s.db.SelectContext(ctx, &jobs, `
		SELECT * FROM review_jobs
		WHERE status = ?
		AND started_at < ?`,
		string(domain.ReviewJobStatusReviewing), cutoff)
	if err != nil {
		return nil, fmt.Errorf("list stale jobs: %w", err)
	}
	return jobs, nil
}

func (s *ReviewJobStore) ListByProject(ctx context.Context, projectID int64, limit int) ([]*domain.ReviewJob, error) {
	var jobs []*domain.ReviewJob
	err := s.db.SelectContext(ctx, &jobs,
		`SELECT * FROM review_jobs WHERE gitlab_project_id = ? ORDER BY created_at DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs by project: %w", err)
	}
	return jobs, nil
}
