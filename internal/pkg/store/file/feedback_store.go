package file

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"

	"github.com/google/uuid"
)

type FeedbackStore struct {
	b *base
}

func NewFeedbackStore(dataDir string) (*FeedbackStore, error) {
	b, err := newBase(dataDir, "review_feedbacks")
	if err != nil {
		return nil, err
	}
	return &FeedbackStore{b: b}, nil
}

func feedbackFilename(id uuid.UUID) string {
	return id.String() + ".json"
}

func feedbackByNoteFilename(noteID int64) string {
	return fmt.Sprintf("_note_%d.json", noteID)
}

func (s *FeedbackStore) Create(_ context.Context, feedback *domain.ReviewFeedback) error {
	feedback.ID = uuid.New()
	feedback.CreatedAt = time.Now()
	feedback.UpdatedAt = time.Now()

	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	if err := s.b.writeJSON(feedbackFilename(feedback.ID), feedback); err != nil {
		return err
	}
	// Write a note-ID index file pointing to the feedback ID
	return s.b.writeJSON(feedbackByNoteFilename(feedback.GitLabNoteID), feedback.ID)
}

func (s *FeedbackStore) GetByNoteID(_ context.Context, noteID int64) (*domain.ReviewFeedback, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	// Read the note index to get feedback ID
	var id uuid.UUID
	err := s.b.readJSON(feedbackByNoteFilename(noteID), &id)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read note index: %w", err)
	}

	var fb domain.ReviewFeedback
	err = s.b.readJSON(feedbackFilename(id), &fb)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read feedback: %w", err)
	}
	return &fb, nil
}

func (s *FeedbackStore) UpdateSignal(_ context.Context, noteID int64, signal domain.FeedbackSignal, replyContent string, threadState domain.ThreadState) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	// Find feedback by note ID
	var id uuid.UUID
	if err := s.b.readJSON(feedbackByNoteFilename(noteID), &id); err != nil {
		return fmt.Errorf("find feedback by note: %w", err)
	}

	fname := feedbackFilename(id)
	var fb domain.ReviewFeedback
	if err := s.b.readJSON(fname, &fb); err != nil {
		return fmt.Errorf("read feedback: %w", err)
	}

	fb.Signal = &signal
	fb.SignalReplyContent = &replyContent
	fb.ThreadState = domain.Ptr(threadState)
	fb.UpdatedAt = time.Now()
	return s.b.writeJSON(fname, &fb)
}

func (s *FeedbackStore) ListForConsolidation(_ context.Context, projectID int64, minAgeDays int) ([]*domain.ReviewFeedback, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	cutoff := time.Now().AddDate(0, 0, -minAgeDays)
	files, err := s.b.listFiles()
	if err != nil {
		return nil, fmt.Errorf("list feedback files: %w", err)
	}

	var results []*domain.ReviewFeedback
	for _, f := range files {
		// Skip index files
		if len(f) > 0 && f[0] == '_' {
			continue
		}
		var fb domain.ReviewFeedback
		if err := s.b.readJSON(f, &fb); err != nil {
			continue
		}
		if fb.GitLabProjectID == projectID &&
			fb.ConsolidatedAt == nil &&
			fb.Signal != nil &&
			fb.CreatedAt.Before(cutoff) {
			results = append(results, &fb)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.Before(results[j].CreatedAt)
	})
	return results, nil
}

func (s *FeedbackStore) MarkConsolidated(_ context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	now := time.Now()
	for _, id := range ids {
		fname := feedbackFilename(id)
		var fb domain.ReviewFeedback
		if err := s.b.readJSON(fname, &fb); err != nil {
			continue
		}
		fb.ConsolidatedAt = &now
		fb.UpdatedAt = now
		s.b.writeJSON(fname, &fb)
	}
	return nil
}

func (s *FeedbackStore) ListRecentByProject(_ context.Context, projectID int64, limit int) ([]*domain.ReviewFeedback, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	files, err := s.b.listFiles()
	if err != nil {
		return nil, fmt.Errorf("list feedback files: %w", err)
	}

	var results []*domain.ReviewFeedback
	for _, f := range files {
		if len(f) > 0 && f[0] == '_' {
			continue
		}
		var fb domain.ReviewFeedback
		if err := s.b.readJSON(f, &fb); err != nil {
			continue
		}
		if fb.GitLabProjectID == projectID && fb.Signal != nil {
			results = append(results, &fb)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
