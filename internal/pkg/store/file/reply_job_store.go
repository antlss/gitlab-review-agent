package file

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"

	"github.com/google/uuid"
)

type ReplyJobStore struct {
	b *base
}

func NewReplyJobStore(dataDir string) (*ReplyJobStore, error) {
	b, err := newBase(dataDir, "reply_jobs")
	if err != nil {
		return nil, err
	}
	return &ReplyJobStore{b: b}, nil
}

func replyJobFilename(id uuid.UUID) string {
	return id.String() + ".json"
}

func (s *ReplyJobStore) Create(_ context.Context, job *domain.ReplyJob) error {
	if job.ID == (uuid.UUID{}) {
		return fmt.Errorf("reply job ID must be set before calling Create")
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	now := time.Now()
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.QueuedAt.IsZero() {
		job.QueuedAt = now
	}
	return s.b.writeJSON(replyJobFilename(job.ID), job)
}

func (s *ReplyJobStore) GetByID(_ context.Context, id uuid.UUID) (*domain.ReplyJob, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	var job domain.ReplyJob
	err := s.b.readJSON(replyJobFilename(id), &job)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read reply job: %w", err)
	}
	return &job, nil
}

func (s *ReplyJobStore) updateJob(id uuid.UUID, fn func(*domain.ReplyJob)) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	fname := replyJobFilename(id)
	var job domain.ReplyJob
	if err := s.b.readJSON(fname, &job); err != nil {
		return fmt.Errorf("read reply job for update: %w", err)
	}
	fn(&job)
	job.UpdatedAt = time.Now()
	return s.b.writeJSON(fname, &job)
}

func (s *ReplyJobStore) UpdateStatus(_ context.Context, id uuid.UUID, status domain.ReplyJobStatus, errMsg *string) error {
	return s.updateJob(id, func(job *domain.ReplyJob) {
		job.Status = status
		job.ErrorMessage = errMsg
		now := time.Now()
		if status == domain.ReplyJobStatusProcessing {
			job.StartedAt = &now
		}
		if status == domain.ReplyJobStatusCompleted || status == domain.ReplyJobStatusFailed {
			job.CompletedAt = &now
		}
	})
}

func (s *ReplyJobStore) UpdateCompleted(_ context.Context, id uuid.UUID, reply string, intent domain.ReplyIntent, signal domain.FeedbackSignal, beforeState, afterState domain.ThreadState) error {
	return s.updateJob(id, func(job *domain.ReplyJob) {
		job.Status = domain.ReplyJobStatusCompleted
		job.ReplyContent = &reply
		job.IntentClassified = &intent
		job.FeedbackSignal = &signal
		job.ThreadStateBefore = domain.Ptr(beforeState)
		job.ThreadStateAfter = domain.Ptr(afterState)
		now := time.Now()
		job.CompletedAt = &now
	})
}
