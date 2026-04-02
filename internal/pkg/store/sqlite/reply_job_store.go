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

type ReplyJobStore struct {
	db *sqlx.DB
}

func NewReplyJobStore(db *sqlx.DB) *ReplyJobStore {
	return &ReplyJobStore{db: db}
}

func (s *ReplyJobStore) Create(ctx context.Context, job *domain.ReplyJob) error {
	if job.ID == (uuid.UUID{}) {
		return fmt.Errorf("reply job ID must be set before calling Create")
	}
	now := time.Now()
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.QueuedAt.IsZero() {
		job.QueuedAt = now
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO reply_jobs (
			id, gitlab_project_id, mr_iid, discussion_id,
			trigger_note_id, trigger_note_content, trigger_note_author,
			bot_comment_id, bot_comment_content, bot_comment_file_path,
			bot_comment_line, status, thread_state_before, thread_state_after,
			queued_at, created_at, updated_at
		) VALUES (
			?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?
		)`,
		job.ID.String(), job.GitLabProjectID, job.MrIID, job.DiscussionID,
		job.TriggerNoteID, job.TriggerNoteContent, job.TriggerNoteAuthor,
		job.BotCommentID, job.BotCommentContent, job.BotCommentFilePath,
		job.BotCommentLine, string(job.Status), domain.DerefThreadState(job.ThreadStateBefore), domain.DerefThreadState(job.ThreadStateAfter),
		now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create reply job: %w", err)
	}
	return nil
}

func (s *ReplyJobStore) GetByID(ctx context.Context, id uuid.UUID) (*domain.ReplyJob, error) {
	var job domain.ReplyJob
	err := s.db.GetContext(ctx, &job,
		`SELECT * FROM reply_jobs WHERE id = ?`, id.String())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get reply job: %w", err)
	}
	return &job, nil
}

func (s *ReplyJobStore) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.ReplyJobStatus, errMsg *string) error {
	now := time.Now().Format(time.RFC3339)
	query := `UPDATE reply_jobs SET status = ?, error_message = ?, updated_at = ?`
	args := []any{string(status), errMsg, now}
	if status == domain.ReplyJobStatusProcessing {
		query += `, started_at = ?`
		args = append(args, now)
	}
	if status == domain.ReplyJobStatusCompleted || status == domain.ReplyJobStatusFailed {
		query += `, completed_at = ?`
		args = append(args, now)
	}
	query += ` WHERE id = ?`
	args = append(args, id.String())
	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update reply job status: %w", err)
	}
	return nil
}

func (s *ReplyJobStore) UpdateCompleted(ctx context.Context, id uuid.UUID, reply string, intent domain.ReplyIntent, signal domain.FeedbackSignal, beforeState, afterState domain.ThreadState) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		UPDATE reply_jobs SET
			status = ?, reply_content = ?, intent_classified = ?,
			feedback_signal = ?, thread_state_before = ?, thread_state_after = ?, completed_at = ?, updated_at = ?
		WHERE id = ?`,
		string(domain.ReplyJobStatusCompleted), reply, string(intent), string(signal), string(beforeState), string(afterState), now, now, id.String())
	if err != nil {
		return fmt.Errorf("update reply completed: %w", err)
	}
	return nil
}
