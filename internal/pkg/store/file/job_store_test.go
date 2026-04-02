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

func TestReviewJobStorePersistsSessionMetadata(t *testing.T) {
	store, err := NewReviewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReviewJobStore() error = %v", err)
	}

	job := &domain.ReviewJob{
		ID:              uuid.New(),
		GitLabProjectID: 1,
		MrIID:           2,
		HeadSHA:         "abc123",
		Status:          domain.ReviewJobStatusPending,
		TriggerSource:   domain.TriggerSourceWebhook,
		TargetBranch:    "main",
		SourceBranch:    "feature",
	}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.UpdateSessionMetadata(context.Background(), job.ID, "chunked", domain.DefaultPromptVersion, domain.DefaultPolicyVersion, domain.DefaultModelPlanVersion, 7); err != nil {
		t.Fatalf("UpdateSessionMetadata() error = %v", err)
	}

	got, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetByID() = nil, want job")
	}
	if domain.DerefStr(got.PromptVersion) != domain.DefaultPromptVersion {
		t.Fatalf("PromptVersion = %q, want %q", domain.DerefStr(got.PromptVersion), domain.DefaultPromptVersion)
	}
	if domain.DerefStr(got.PolicyVersion) != domain.DefaultPolicyVersion {
		t.Fatalf("PolicyVersion = %q, want %q", domain.DerefStr(got.PolicyVersion), domain.DefaultPolicyVersion)
	}
	if domain.DerefStr(got.ModelPlanVersion) != domain.DefaultModelPlanVersion {
		t.Fatalf("ModelPlanVersion = %q, want %q", domain.DerefStr(got.ModelPlanVersion), domain.DefaultModelPlanVersion)
	}
	if domain.DerefStr(got.ReviewMode) != "chunked" {
		t.Fatalf("ReviewMode = %q, want %q", domain.DerefStr(got.ReviewMode), "chunked")
	}
	if domain.DerefInt(got.FindingsBudget) != 7 {
		t.Fatalf("FindingsBudget = %d, want 7", domain.DerefInt(got.FindingsBudget))
	}
}

func TestReviewJobStoreCreateAppliesVersionDefaults(t *testing.T) {
	store, err := NewReviewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReviewJobStore() error = %v", err)
	}

	job := &domain.ReviewJob{
		ID:              uuid.New(),
		GitLabProjectID: 1,
		MrIID:           2,
		HeadSHA:         "def456",
		Status:          domain.ReviewJobStatusPending,
		TriggerSource:   domain.TriggerSourceCLI,
		TargetBranch:    "main",
		SourceBranch:    "feature",
	}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetByID() = nil, want job")
	}
	if domain.DerefStr(got.PromptVersion) != domain.DefaultPromptVersion {
		t.Fatalf("PromptVersion = %q, want %q", domain.DerefStr(got.PromptVersion), domain.DefaultPromptVersion)
	}
	if domain.DerefStr(got.PolicyVersion) != domain.DefaultPolicyVersion {
		t.Fatalf("PolicyVersion = %q, want %q", domain.DerefStr(got.PolicyVersion), domain.DefaultPolicyVersion)
	}
	if domain.DerefStr(got.ModelPlanVersion) != domain.DefaultModelPlanVersion {
		t.Fatalf("ModelPlanVersion = %q, want %q", domain.DerefStr(got.ModelPlanVersion), domain.DefaultModelPlanVersion)
	}
}

func TestFeedbackStorePersistsSessionMetadata(t *testing.T) {
	store, err := NewFeedbackStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFeedbackStore() error = %v", err)
	}

	reviewJobID := uuid.New()
	feedback := &domain.ReviewFeedback{
		GitLabProjectID:    1,
		ReviewJobID:        &reviewJobID,
		GitLabDiscussionID: "discussion-1",
		GitLabNoteID:       123,
		ReviewMode:         domain.Ptr("chunked"),
		PromptVersion:      domain.Ptr(domain.DefaultPromptVersion),
		PolicyVersion:      domain.Ptr(domain.DefaultPolicyVersion),
		ModelPlanVersion:   domain.Ptr(domain.DefaultModelPlanVersion),
		CommentSummary:     domain.Ptr("possible nil dereference"),
	}
	if err := store.Create(context.Background(), feedback); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.GetByNoteID(context.Background(), feedback.GitLabNoteID)
	if err != nil {
		t.Fatalf("GetByNoteID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetByNoteID() = nil, want feedback")
	}
	if got.ReviewJobID == nil || *got.ReviewJobID != reviewJobID {
		t.Fatalf("ReviewJobID = %v, want %v", got.ReviewJobID, reviewJobID)
	}
	if domain.DerefStr(got.ReviewMode) != "chunked" {
		t.Fatalf("ReviewMode = %q, want chunked", domain.DerefStr(got.ReviewMode))
	}
	if domain.DerefStr(got.PromptVersion) != domain.DefaultPromptVersion {
		t.Fatalf("PromptVersion = %q, want %q", domain.DerefStr(got.PromptVersion), domain.DefaultPromptVersion)
	}
	if domain.DerefStr(got.PolicyVersion) != domain.DefaultPolicyVersion {
		t.Fatalf("PolicyVersion = %q, want %q", domain.DerefStr(got.PolicyVersion), domain.DefaultPolicyVersion)
	}
	if domain.DerefStr(got.ModelPlanVersion) != domain.DefaultModelPlanVersion {
		t.Fatalf("ModelPlanVersion = %q, want %q", domain.DerefStr(got.ModelPlanVersion), domain.DefaultModelPlanVersion)
	}
}

func TestReviewRecordStorePersistsSessionMetadata(t *testing.T) {
	store, err := NewReviewRecordStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReviewRecordStore() error = %v", err)
	}

	record := &domain.ReviewRecord{
		GitLabProjectID:  1,
		MrIID:            2,
		ReviewJobID:      uuid.New(),
		HeadSHA:          "abc123",
		ReviewMode:       domain.Ptr("triage"),
		PromptVersion:    domain.Ptr(domain.DefaultPromptVersion),
		PolicyVersion:    domain.Ptr(domain.DefaultPolicyVersion),
		ModelPlanVersion: domain.Ptr(domain.DefaultModelPlanVersion),
		ReviewedFiles:    []byte(`["main.go"]`),
		CommentsPosted:   3,
	}
	if err := store.Upsert(context.Background(), record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, err := store.GetLastCompleted(context.Background(), record.GitLabProjectID, record.MrIID)
	if err != nil {
		t.Fatalf("GetLastCompleted() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetLastCompleted() = nil, want record")
	}
	if domain.DerefStr(got.ReviewMode) != "triage" {
		t.Fatalf("ReviewMode = %q, want triage", domain.DerefStr(got.ReviewMode))
	}
	if domain.DerefStr(got.PromptVersion) != domain.DefaultPromptVersion {
		t.Fatalf("PromptVersion = %q, want %q", domain.DerefStr(got.PromptVersion), domain.DefaultPromptVersion)
	}
	if domain.DerefStr(got.PolicyVersion) != domain.DefaultPolicyVersion {
		t.Fatalf("PolicyVersion = %q, want %q", domain.DerefStr(got.PolicyVersion), domain.DefaultPolicyVersion)
	}
	if domain.DerefStr(got.ModelPlanVersion) != domain.DefaultModelPlanVersion {
		t.Fatalf("ModelPlanVersion = %q, want %q", domain.DerefStr(got.ModelPlanVersion), domain.DefaultModelPlanVersion)
	}
}

func TestFeedbackStorePersistsFingerprints(t *testing.T) {
	store, err := NewFeedbackStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFeedbackStore() error = %v", err)
	}

	feedback := &domain.ReviewFeedback{
		GitLabProjectID:     1,
		GitLabDiscussionID:  "discussion-1",
		GitLabNoteID:        456,
		CommentSummary:      domain.Ptr("possible nil dereference"),
		ContentHash:         domain.Ptr("content-hash"),
		SemanticFingerprint: domain.Ptr("semantic-fingerprint"),
		LocationFingerprint: domain.Ptr("location-fingerprint"),
	}
	if err := store.Create(context.Background(), feedback); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.GetByNoteID(context.Background(), feedback.GitLabNoteID)
	if err != nil {
		t.Fatalf("GetByNoteID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetByNoteID() = nil, want feedback")
	}
	if domain.DerefStr(got.ContentHash) != "content-hash" {
		t.Fatalf("ContentHash = %q, want content-hash", domain.DerefStr(got.ContentHash))
	}
	if domain.DerefStr(got.SemanticFingerprint) != "semantic-fingerprint" {
		t.Fatalf("SemanticFingerprint = %q, want semantic-fingerprint", domain.DerefStr(got.SemanticFingerprint))
	}
	if domain.DerefStr(got.LocationFingerprint) != "location-fingerprint" {
		t.Fatalf("LocationFingerprint = %q, want location-fingerprint", domain.DerefStr(got.LocationFingerprint))
	}
}

func TestReplyJobStorePersistsThreadStates(t *testing.T) {
	store, err := NewReplyJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewReplyJobStore() error = %v", err)
	}

	job := &domain.ReplyJob{
		ID:              uuid.New(),
		GitLabProjectID: 1,
		MrIID:           2,
		DiscussionID:    "discussion-1",
		TriggerNoteID:   10,
		Status:          domain.ReplyJobStatusPending,
	}
	if err := store.Create(context.Background(), job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.UpdateCompleted(context.Background(), job.ID, "thanks", domain.IntentAgree, domain.FeedbackSignalAccepted, domain.ThreadStateOpen, domain.ThreadStatePendingVerification); err != nil {
		t.Fatalf("UpdateCompleted() error = %v", err)
	}

	got, err := store.GetByID(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetByID() = nil, want reply job")
	}
	if domain.DerefThreadState(got.ThreadStateBefore) != domain.ThreadStateOpen {
		t.Fatalf("ThreadStateBefore = %q, want %q", domain.DerefThreadState(got.ThreadStateBefore), domain.ThreadStateOpen)
	}
	if domain.DerefThreadState(got.ThreadStateAfter) != domain.ThreadStatePendingVerification {
		t.Fatalf("ThreadStateAfter = %q, want %q", domain.DerefThreadState(got.ThreadStateAfter), domain.ThreadStatePendingVerification)
	}
}

func TestFeedbackStorePersistsThreadStateSignal(t *testing.T) {
	store, err := NewFeedbackStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFeedbackStore() error = %v", err)
	}

	feedback := &domain.ReviewFeedback{
		GitLabProjectID:    1,
		GitLabDiscussionID: "discussion-2",
		GitLabNoteID:       789,
	}
	if err := store.Create(context.Background(), feedback); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.UpdateSignal(context.Background(), feedback.GitLabNoteID, domain.FeedbackSignalAccepted, "fixed", domain.ThreadStatePendingVerification); err != nil {
		t.Fatalf("UpdateSignal() error = %v", err)
	}

	got, err := store.GetByNoteID(context.Background(), feedback.GitLabNoteID)
	if err != nil {
		t.Fatalf("GetByNoteID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetByNoteID() = nil, want feedback")
	}
	if domain.DerefThreadState(got.ThreadState) != domain.ThreadStatePendingVerification {
		t.Fatalf("ThreadState = %q, want %q", domain.DerefThreadState(got.ThreadState), domain.ThreadStatePendingVerification)
	}
}
