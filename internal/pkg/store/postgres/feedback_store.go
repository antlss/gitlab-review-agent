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

type FeedbackStore struct {
	db *sqlx.DB
}

func NewFeedbackStore(db *sqlx.DB) *FeedbackStore {
	return &FeedbackStore{db: db}
}

func (s *FeedbackStore) Create(ctx context.Context, feedback *domain.ReviewFeedback) error {
	feedback.ID = uuid.New()
	feedback.CreatedAt = time.Now()
	feedback.UpdatedAt = time.Now()

	_, err := s.db.NamedExecContext(ctx, `
		INSERT INTO review_feedbacks (
			id, gitlab_project_id, review_job_id, gitlab_discussion_id,
			gitlab_note_id, review_mode, prompt_version, policy_version,
			model_plan_version, file_path, line_number, category,
			comment_summary, content_hash, semantic_fingerprint, location_fingerprint,
			thread_state, language, model_used, created_at, updated_at
		) VALUES (
			:id, :gitlab_project_id, :review_job_id, :gitlab_discussion_id,
			:gitlab_note_id, :review_mode, :prompt_version, :policy_version,
			:model_plan_version, :file_path, :line_number, :category,
			:comment_summary, :content_hash, :semantic_fingerprint, :location_fingerprint,
			:thread_state, :language, :model_used, :created_at, :updated_at
		)`, feedback)
	if err != nil {
		return fmt.Errorf("create feedback: %w", err)
	}
	return nil
}

func (s *FeedbackStore) GetByNoteID(ctx context.Context, noteID int64) (*domain.ReviewFeedback, error) {
	var fb domain.ReviewFeedback
	err := s.db.GetContext(ctx, &fb,
		`SELECT * FROM review_feedbacks WHERE gitlab_note_id = $1`, noteID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get feedback by note id: %w", err)
	}
	return &fb, nil
}

func (s *FeedbackStore) UpdateSignal(ctx context.Context, noteID int64, signal domain.FeedbackSignal, replyContent string, threadState domain.ThreadState) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE review_feedbacks SET
			signal = $1, thread_state = $2, signal_reply_content = $3, updated_at = NOW()
		WHERE gitlab_note_id = $4`,
		signal, threadState, replyContent, noteID)
	if err != nil {
		return fmt.Errorf("update feedback signal: %w", err)
	}
	return nil
}

func (s *FeedbackStore) ListForConsolidation(ctx context.Context, projectID int64, minAgeDays int) ([]*domain.ReviewFeedback, error) {
	var results []*domain.ReviewFeedback
	err := s.db.SelectContext(ctx, &results, `
		SELECT * FROM review_feedbacks
		WHERE gitlab_project_id = $1
			AND consolidated_at IS NULL
			AND signal IS NOT NULL
			AND created_at < NOW() - make_interval(days := $2)
		ORDER BY created_at ASC`,
		projectID, minAgeDays)
	if err != nil {
		return nil, fmt.Errorf("list feedbacks for consolidation: %w", err)
	}
	return results, nil
}

func (s *FeedbackStore) MarkConsolidated(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	query, args, err := sqlx.In(
		`UPDATE review_feedbacks SET consolidated_at = NOW(), updated_at = NOW() WHERE id IN (?)`,
		ids)
	if err != nil {
		return fmt.Errorf("build consolidation query: %w", err)
	}
	query = s.db.Rebind(query)
	_, err = s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mark consolidated: %w", err)
	}
	return nil
}

func (s *FeedbackStore) ListRecentByProject(ctx context.Context, projectID int64, limit int) ([]*domain.ReviewFeedback, error) {
	var results []*domain.ReviewFeedback
	err := s.db.SelectContext(ctx, &results, `
		SELECT * FROM review_feedbacks
		WHERE gitlab_project_id = $1 AND signal IS NOT NULL
		ORDER BY created_at DESC LIMIT $2`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent feedbacks: %w", err)
	}
	return results, nil
}
