package postgres

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
		WHERE gitlab_project_id = $1 AND mr_iid = $2`,
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

	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO review_records (
			id, gitlab_project_id, mr_iid, review_job_id,
			head_sha, reviewed_files, comments_posted, created_at
		) VALUES (
			:id, :gitlab_project_id, :mr_iid, :review_job_id,
			:head_sha, :reviewed_files, :comments_posted, :created_at
		)
		ON CONFLICT ON CONSTRAINT uq_review_records_mr DO UPDATE SET
			review_job_id = EXCLUDED.review_job_id,
			head_sha = EXCLUDED.head_sha,
			reviewed_files = EXCLUDED.reviewed_files,
			comments_posted = EXCLUDED.comments_posted,
			created_at = EXCLUDED.created_at
	`, record)
	if err != nil {
		return fmt.Errorf("upsert review record: %w", err)
	}
	return nil
}
