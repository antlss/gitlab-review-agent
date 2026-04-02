package file

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

func TestReviewJobStoreExistsPendingOrCompletedIncludesPosting(t *testing.T) {
	store, err := NewReviewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReviewJobStore() error = %v", err)
	}

	tests := []struct {
		name   string
		status domain.ReviewJobStatus
		want   bool
	}{
		{name: "posting counts as in-flight", status: domain.ReviewJobStatusPosting, want: true},
		{name: "failed does not block duplicate", status: domain.ReviewJobStatusFailed, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headSHA := tt.name
			job := &domain.ReviewJob{
				ID:              uuid.New(),
				GitLabProjectID: 1,
				MrIID:           2,
				HeadSHA:         headSHA,
				Status:          tt.status,
				TriggerSource:   domain.TriggerSourceWebhook,
				TargetBranch:    "main",
				SourceBranch:    "feature",
			}
			if err := store.Create(context.Background(), job); err != nil {
				t.Fatalf("Create() error = %v", err)
			}

			exists, err := store.ExistsPendingOrCompleted(context.Background(), 1, 2, headSHA, 30)
			if err != nil {
				t.Fatalf("ExistsPendingOrCompleted() error = %v", err)
			}
			if exists != tt.want {
				t.Fatalf("ExistsPendingOrCompleted() = %v, want %v", exists, tt.want)
			}
		})
	}
}
