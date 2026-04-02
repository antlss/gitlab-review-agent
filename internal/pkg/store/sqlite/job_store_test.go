package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

func TestReviewJobStoreExistsPendingOrCompletedIncludesPosting(t *testing.T) {
	db, err := Connect(filepath.Join(t.TempDir(), "review-agent.db"))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := NewReviewJobStore(db)

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
	db, err := Connect(filepath.Join(t.TempDir(), "review-agent.db"))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := NewReviewJobStore(db)
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
	if err := store.UpdateSessionMetadata(context.Background(), job.ID, "triage", domain.DefaultPromptVersion, domain.DefaultPolicyVersion, domain.DefaultModelPlanVersion, 4); err != nil {
		t.Fatalf("UpdateSessionMetadata() error = %v", err)
	}

	var reviewMode, promptVersion, policyVersion, modelPlanVersion string
	var findingsBudget int
	if err := db.QueryRowContext(context.Background(), `SELECT review_mode, prompt_version, policy_version, model_plan_version, findings_budget FROM review_jobs WHERE id = ?`, job.ID.String()).Scan(&reviewMode, &promptVersion, &policyVersion, &modelPlanVersion, &findingsBudget); err != nil {
		t.Fatalf("QueryRowContext() error = %v", err)
	}
	if reviewMode != "triage" {
		t.Fatalf("review_mode = %q, want triage", reviewMode)
	}
	if promptVersion != domain.DefaultPromptVersion {
		t.Fatalf("prompt_version = %q, want %q", promptVersion, domain.DefaultPromptVersion)
	}
	if policyVersion != domain.DefaultPolicyVersion {
		t.Fatalf("policy_version = %q, want %q", policyVersion, domain.DefaultPolicyVersion)
	}
	if modelPlanVersion != domain.DefaultModelPlanVersion {
		t.Fatalf("model_plan_version = %q, want %q", modelPlanVersion, domain.DefaultModelPlanVersion)
	}
	if findingsBudget != 4 {
		t.Fatalf("findings_budget = %d, want 4", findingsBudget)
	}
}

func TestReviewJobStoreCreateAppliesVersionDefaults(t *testing.T) {
	db, err := Connect(filepath.Join(t.TempDir(), "review-agent.db"))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := NewReviewJobStore(db)
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

	var promptVersion, policyVersion, modelPlanVersion string
	if err := db.QueryRowContext(context.Background(), `SELECT prompt_version, policy_version, model_plan_version FROM review_jobs WHERE id = ?`, job.ID.String()).Scan(&promptVersion, &policyVersion, &modelPlanVersion); err != nil {
		t.Fatalf("QueryRowContext() error = %v", err)
	}
	if promptVersion != domain.DefaultPromptVersion {
		t.Fatalf("prompt_version = %q, want %q", promptVersion, domain.DefaultPromptVersion)
	}
	if policyVersion != domain.DefaultPolicyVersion {
		t.Fatalf("policy_version = %q, want %q", policyVersion, domain.DefaultPolicyVersion)
	}
	if modelPlanVersion != domain.DefaultModelPlanVersion {
		t.Fatalf("model_plan_version = %q, want %q", modelPlanVersion, domain.DefaultModelPlanVersion)
	}
}

func TestFeedbackStorePersistsSessionMetadata(t *testing.T) {
	db, err := Connect(filepath.Join(t.TempDir(), "review-agent.db"))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := NewFeedbackStore(db)
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

	var gotReviewJobID, reviewMode, promptVersion, policyVersion, modelPlanVersion string
	if err := db.QueryRowContext(context.Background(), `SELECT review_job_id, review_mode, prompt_version, policy_version, model_plan_version FROM review_feedbacks WHERE gitlab_note_id = ?`, feedback.GitLabNoteID).Scan(&gotReviewJobID, &reviewMode, &promptVersion, &policyVersion, &modelPlanVersion); err != nil {
		t.Fatalf("QueryRowContext() error = %v", err)
	}
	if gotReviewJobID != reviewJobID.String() {
		t.Fatalf("review_job_id = %q, want %q", gotReviewJobID, reviewJobID.String())
	}
	if reviewMode != "chunked" {
		t.Fatalf("review_mode = %q, want chunked", reviewMode)
	}
	if promptVersion != domain.DefaultPromptVersion {
		t.Fatalf("prompt_version = %q, want %q", promptVersion, domain.DefaultPromptVersion)
	}
	if policyVersion != domain.DefaultPolicyVersion {
		t.Fatalf("policy_version = %q, want %q", policyVersion, domain.DefaultPolicyVersion)
	}
	if modelPlanVersion != domain.DefaultModelPlanVersion {
		t.Fatalf("model_plan_version = %q, want %q", modelPlanVersion, domain.DefaultModelPlanVersion)
	}
}

func TestReviewRecordStorePersistsSessionMetadata(t *testing.T) {
	db, err := Connect(filepath.Join(t.TempDir(), "review-agent.db"))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := NewReviewRecordStore(db)
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

	var reviewMode, promptVersion, policyVersion, modelPlanVersion string
	if err := db.QueryRowContext(context.Background(), `SELECT review_mode, prompt_version, policy_version, model_plan_version FROM review_records WHERE gitlab_project_id = ? AND mr_iid = ?`, record.GitLabProjectID, record.MrIID).Scan(&reviewMode, &promptVersion, &policyVersion, &modelPlanVersion); err != nil {
		t.Fatalf("QueryRowContext() error = %v", err)
	}
	if reviewMode != "triage" {
		t.Fatalf("review_mode = %q, want triage", reviewMode)
	}
	if promptVersion != domain.DefaultPromptVersion {
		t.Fatalf("prompt_version = %q, want %q", promptVersion, domain.DefaultPromptVersion)
	}
	if policyVersion != domain.DefaultPolicyVersion {
		t.Fatalf("policy_version = %q, want %q", policyVersion, domain.DefaultPolicyVersion)
	}
	if modelPlanVersion != domain.DefaultModelPlanVersion {
		t.Fatalf("model_plan_version = %q, want %q", modelPlanVersion, domain.DefaultModelPlanVersion)
	}
}

func TestFeedbackStorePersistsFingerprints(t *testing.T) {
	db, err := Connect(filepath.Join(t.TempDir(), "review-agent.db"))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := NewFeedbackStore(db)
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

	var contentHash, semanticFingerprint, locationFingerprint string
	if err := db.QueryRowContext(context.Background(), `SELECT content_hash, semantic_fingerprint, location_fingerprint FROM review_feedbacks WHERE gitlab_note_id = ?`, feedback.GitLabNoteID).Scan(&contentHash, &semanticFingerprint, &locationFingerprint); err != nil {
		t.Fatalf("QueryRowContext() error = %v", err)
	}
	if contentHash != "content-hash" {
		t.Fatalf("content_hash = %q, want content-hash", contentHash)
	}
	if semanticFingerprint != "semantic-fingerprint" {
		t.Fatalf("semantic_fingerprint = %q, want semantic-fingerprint", semanticFingerprint)
	}
	if locationFingerprint != "location-fingerprint" {
		t.Fatalf("location_fingerprint = %q, want location-fingerprint", locationFingerprint)
	}
}
