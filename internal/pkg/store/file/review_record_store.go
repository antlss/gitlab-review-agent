package file

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"

	"github.com/google/uuid"
)

type ReviewRecordStore struct {
	b *base
}

func NewReviewRecordStore(dataDir string) (*ReviewRecordStore, error) {
	b, err := newBase(dataDir, "review_records")
	if err != nil {
		return nil, err
	}
	return &ReviewRecordStore{b: b}, nil
}

func recordFilename(projectID, mrIID int64) string {
	return fmt.Sprintf("%d_%d.json", projectID, mrIID)
}

func (s *ReviewRecordStore) GetLastCompleted(_ context.Context, projectID, mrIID int64) (*domain.ReviewRecord, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	var record domain.ReviewRecord
	err := s.b.readJSON(recordFilename(projectID, mrIID), &record)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read review record: %w", err)
	}
	return &record, nil
}

func (s *ReviewRecordStore) Upsert(_ context.Context, record *domain.ReviewRecord) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	record.ID = uuid.New()
	record.CreatedAt = time.Now()
	return s.b.writeJSON(recordFilename(record.GitLabProjectID, record.MrIID), record)
}
