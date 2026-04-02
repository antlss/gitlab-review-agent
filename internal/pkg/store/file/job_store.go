package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"

	"github.com/google/uuid"
)

type ReviewJobStore struct {
	b *base
}

func NewReviewJobStore(dataDir string) (*ReviewJobStore, error) {
	b, err := newBase(dataDir, "review_jobs")
	if err != nil {
		return nil, err
	}
	return &ReviewJobStore{b: b}, nil
}

func jobFilename(id uuid.UUID) string {
	return id.String() + ".json"
}

func (s *ReviewJobStore) Create(_ context.Context, job *domain.ReviewJob) error {
	if job.ID == (uuid.UUID{}) {
		return fmt.Errorf("review job ID must be set before calling Create")
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	now := time.Now()
	domain.EnsureReviewJobVersionDefaults(job)
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.QueuedAt.IsZero() {
		job.QueuedAt = now
	}
	return s.b.writeJSON(jobFilename(job.ID), job)
}

func (s *ReviewJobStore) GetByID(_ context.Context, id uuid.UUID) (*domain.ReviewJob, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	var job domain.ReviewJob
	err := s.b.readJSON(jobFilename(id), &job)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read review job: %w", err)
	}
	return &job, nil
}

func (s *ReviewJobStore) updateJob(id uuid.UUID, fn func(*domain.ReviewJob)) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	fname := jobFilename(id)
	var job domain.ReviewJob
	if err := s.b.readJSON(fname, &job); err != nil {
		return fmt.Errorf("read review job for update: %w (path: %s)", err, filepath.Join(s.b.dir, fname))
	}
	fn(&job)
	job.UpdatedAt = time.Now()
	return s.b.writeJSON(fname, &job)
}

func (s *ReviewJobStore) UpdateStatus(_ context.Context, id uuid.UUID, status domain.ReviewJobStatus, errMsg *string) error {
	return s.updateJob(id, func(job *domain.ReviewJob) {
		job.Status = status
		job.ErrorMessage = errMsg
		now := time.Now()
		if status == domain.ReviewJobStatusReviewing {
			job.StartedAt = &now
		}
		if status == domain.ReviewJobStatusCompleted || status == domain.ReviewJobStatusFailed ||
			status == domain.ReviewJobStatusParseFailed || status == domain.ReviewJobStatusSkippedSize {
			job.CompletedAt = &now
		}
	})
}

func (s *ReviewJobStore) UpdateBaseSHA(_ context.Context, id uuid.UUID, baseSHA string) error {
	return s.updateJob(id, func(job *domain.ReviewJob) {
		job.BaseSHA = &baseSHA
	})
}

func (s *ReviewJobStore) UpdateSessionMetadata(_ context.Context, id uuid.UUID, reviewMode, promptVersion, policyVersion, modelPlanVersion string, findingsBudget int) error {
	return s.updateJob(id, func(job *domain.ReviewJob) {
		job.ReviewMode = domain.Ptr(reviewMode)
		job.PromptVersion = domain.Ptr(promptVersion)
		job.PolicyVersion = domain.Ptr(policyVersion)
		job.ModelPlanVersion = domain.Ptr(modelPlanVersion)
		job.FindingsBudget = domain.Ptr(findingsBudget)
	})
}

func (s *ReviewJobStore) UpdateAIOutput(_ context.Context, id uuid.UUID, raw string, parsed []domain.ParsedComment, iterations, tokens int) error {
	return s.updateJob(id, func(job *domain.ReviewJob) {
		job.AIOutputRaw = &raw
		parsedJSON, _ := json.Marshal(parsed)
		job.AIOutputParsed = parsedJSON
		job.IterationsUsed = &iterations
		job.TokensEstimated = &tokens
	})
}

func (s *ReviewJobStore) UpdateCompleted(_ context.Context, id uuid.UUID, posted, suppressed int) error {
	return s.updateJob(id, func(job *domain.ReviewJob) {
		job.Status = domain.ReviewJobStatusCompleted
		job.TotalCommentsPosted = &posted
		job.TotalCommentsSuppressed = &suppressed
		now := time.Now()
		job.CompletedAt = &now
	})
}

func (s *ReviewJobStore) UpdateModelUsed(_ context.Context, id uuid.UUID, model string) error {
	return s.updateJob(id, func(job *domain.ReviewJob) {
		job.ModelUsed = &model
	})
}

func (s *ReviewJobStore) ExistsPendingOrCompleted(_ context.Context, projectID, mrIID int64, headSHA string, withinMinutes int) (bool, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(withinMinutes) * time.Minute)
	files, err := s.b.listFiles()
	if err != nil {
		return false, fmt.Errorf("list review jobs: %w", err)
	}

	for _, f := range files {
		var job domain.ReviewJob
		if err := s.b.readJSON(f, &job); err != nil {
			continue
		}
		if job.GitLabProjectID == projectID && job.MrIID == mrIID && job.HeadSHA == headSHA &&
			(job.Status == domain.ReviewJobStatusPending ||
				job.Status == domain.ReviewJobStatusReviewing ||
				job.Status == domain.ReviewJobStatusPosting ||
				job.Status == domain.ReviewJobStatusCompleted) &&
			job.CreatedAt.After(cutoff) {
			return true, nil
		}
	}
	return false, nil
}

func (s *ReviewJobStore) ListStale(_ context.Context, olderThanMinutes int) ([]*domain.ReviewJob, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	cutoff := time.Now().Add(-time.Duration(olderThanMinutes) * time.Minute)
	files, err := s.b.listFiles()
	if err != nil {
		return nil, fmt.Errorf("list review jobs: %w", err)
	}

	var jobs []*domain.ReviewJob
	for _, f := range files {
		var job domain.ReviewJob
		if err := s.b.readJSON(f, &job); err != nil {
			continue
		}
		if job.Status == domain.ReviewJobStatusReviewing &&
			job.StartedAt != nil && job.StartedAt.Before(cutoff) {
			jobs = append(jobs, &job)
		}
	}
	return jobs, nil
}

func (s *ReviewJobStore) ListByProject(_ context.Context, projectID int64, limit int) ([]*domain.ReviewJob, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	files, err := s.b.listFiles()
	if err != nil {
		return nil, fmt.Errorf("list review jobs: %w", err)
	}

	var jobs []*domain.ReviewJob
	for _, f := range files {
		var job domain.ReviewJob
		if err := s.b.readJSON(f, &job); err != nil {
			continue
		}
		if job.GitLabProjectID == projectID {
			jobs = append(jobs, &job)
		}
	}

	// Sort by created_at DESC
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})

	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}
