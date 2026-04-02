package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type ReviewRecordStore struct {
	db *sqlx.DB
}

func NewReviewRecordStore(db *sqlx.DB) *ReviewRecordStore {
	return &ReviewRecordStore{db: db}
}

func (s *ReviewRecordStore) GetLastCompleted(ctx context.Context, projectID, mrIID int64) (*domain.ReviewRecord, error) {
	var record domain.ReviewRecord
	err := s.db.GetContext(ctx, &record, `
		SELECT * FROM review_records
		WHERE gitlab_project_id = ? AND mr_iid = ?`,
		projectID, mrIID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get last review record: %w", err)
	}
	return &record, nil
}

func (s *ReviewRecordStore) Upsert(ctx context.Context, record *domain.ReviewRecord) error {
	record.ID = uuid.New()
	record.CreatedAt = time.Now()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_records (
			id, gitlab_project_id, mr_iid, review_job_id,
			head_sha, review_mode, prompt_version, policy_version,
			model_plan_version, reviewed_files, comments_posted, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gitlab_project_id, mr_iid) DO UPDATE SET
			review_job_id = excluded.review_job_id,
			head_sha = excluded.head_sha,
			review_mode = excluded.review_mode,
			prompt_version = excluded.prompt_version,
			policy_version = excluded.policy_version,
			model_plan_version = excluded.model_plan_version,
			reviewed_files = excluded.reviewed_files,
			comments_posted = excluded.comments_posted,
			created_at = excluded.created_at`,
		record.ID.String(), record.GitLabProjectID, record.MrIID,
		record.ReviewJobID.String(), record.HeadSHA,
		record.ReviewMode, record.PromptVersion, record.PolicyVersion,
		record.ModelPlanVersion, string(record.ReviewedFiles), record.CommentsPosted,
		record.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upsert review record: %w", err)
	}
	return nil
}
