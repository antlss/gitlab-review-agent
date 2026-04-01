package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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

	var reviewJobID *string
	if feedback.ReviewJobID != nil {
		s := feedback.ReviewJobID.String()
		reviewJobID = &s
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_feedbacks (
			id, gitlab_project_id, review_job_id, gitlab_discussion_id,
			gitlab_note_id, file_path, line_number, category,
			comment_summary, language, model_used,
			created_at, updated_at
		) VALUES (
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?,
			?, ?
		)`,
		feedback.ID.String(), feedback.GitLabProjectID, reviewJobID, feedback.GitLabDiscussionID,
		feedback.GitLabNoteID, feedback.FilePath, feedback.LineNumber, domain.PtrCategoryToStr(feedback.Category),
		feedback.CommentSummary, feedback.Language, feedback.ModelUsed,
		feedback.CreatedAt.Format(time.RFC3339), feedback.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create feedback: %w", err)
	}
	return nil
}

func (s *FeedbackStore) GetByNoteID(ctx context.Context, noteID int64) (*domain.ReviewFeedback, error) {
	var fb domain.ReviewFeedback
	err := s.db.GetContext(ctx, &fb,
		`SELECT * FROM review_feedbacks WHERE gitlab_note_id = ?`, noteID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get feedback by note id: %w", err)
	}
	return &fb, nil
}

func (s *FeedbackStore) UpdateSignal(ctx context.Context, noteID int64, signal domain.FeedbackSignal, replyContent string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE review_feedbacks SET
			signal = ?, signal_reply_content = ?, updated_at = ?
		WHERE gitlab_note_id = ?`,
		string(signal), replyContent, time.Now().Format(time.RFC3339), noteID)
	if err != nil {
		return fmt.Errorf("update feedback signal: %w", err)
	}
	return nil
}

func (s *FeedbackStore) ListForConsolidation(ctx context.Context, projectID int64, minAgeDays int) ([]*domain.ReviewFeedback, error) {
	cutoff := time.Now().AddDate(0, 0, -minAgeDays).Format(time.RFC3339)
	var results []*domain.ReviewFeedback
	err := s.db.SelectContext(ctx, &results, `
		SELECT * FROM review_feedbacks
		WHERE gitlab_project_id = ?
			AND consolidated_at IS NULL
			AND signal IS NOT NULL
			AND created_at < ?
		ORDER BY created_at ASC`,
		projectID, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list feedbacks for consolidation: %w", err)
	}
	return results, nil
}

func (s *FeedbackStore) MarkConsolidated(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	// Build placeholders manually for SQLite
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+2)
	args = append(args, now, now)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id.String())
	}
	query := fmt.Sprintf(
		`UPDATE review_feedbacks SET consolidated_at = ?, updated_at = ? WHERE id IN (%s)`,
		strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("mark consolidated: %w", err)
	}
	return nil
}

func (s *FeedbackStore) ListRecentByProject(ctx context.Context, projectID int64, limit int) ([]*domain.ReviewFeedback, error) {
	var results []*domain.ReviewFeedback
	err := s.db.SelectContext(ctx, &results, `
		SELECT * FROM review_feedbacks
		WHERE gitlab_project_id = ? AND signal IS NOT NULL
		ORDER BY created_at DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent feedbacks: %w", err)
	}
	return results, nil
}
