package review

import (
	"fmt"
	"testing"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

func TestPlanReviewModes(t *testing.T) {
	files := makeFiles(90)

	tests := []struct {
		name      string
		input     PlanInput
		files     []domain.DiffFile
		wantMode  ReviewMode
		wantCount int
	}{
		{
			name:      "small MR stays unified",
			input:     PlanInput{ChunkThreshold: 21, TriageThreshold: 81, SampleFileCount: 30, MaxFindings: 10},
			files:     files[:5],
			wantMode:  ReviewModeUnified,
			wantCount: 5,
		},
		{
			name:      "medium MR becomes chunked",
			input:     PlanInput{ChunkThreshold: 21, TriageThreshold: 81, SampleFileCount: 30, MaxFindings: 10},
			files:     files[:30],
			wantMode:  ReviewModeChunked,
			wantCount: 30,
		},
		{
			name:      "very large MR becomes triage and samples files",
			input:     PlanInput{ChunkThreshold: 21, TriageThreshold: 81, SampleFileCount: 30, MaxFindings: 10},
			files:     files,
			wantMode:  ReviewModeTriage,
			wantCount: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanReview(tt.input, tt.files)
			if plan.Mode != tt.wantMode {
				t.Fatalf("PlanReview() mode = %s, want %s", plan.Mode, tt.wantMode)
			}
			if len(plan.Files) != tt.wantCount {
				t.Fatalf("PlanReview() file count = %d, want %d", len(plan.Files), tt.wantCount)
			}
			if plan.FindingsBudget != 10 {
				t.Fatalf("PlanReview() findings budget = %d, want 10", plan.FindingsBudget)
			}
		})
	}
}

func TestApplyFindingsBudgetSuppressesLowerPriorityComments(t *testing.T) {
	comments := []domain.ParsedComment{
		{FilePath: "a.go", LineNumber: 10, Severity: domain.SeverityMedium, Confidence: "HIGH"},
		{FilePath: "b.go", LineNumber: 20, Severity: domain.SeverityCritical, Confidence: "HIGH"},
		{FilePath: "c.go", LineNumber: 30, Severity: domain.SeverityHigh, Confidence: "MEDIUM"},
	}
	files := []domain.DiffFile{
		{Path: "a.go", RiskScore: 10},
		{Path: "b.go", RiskScore: 5},
		{Path: "c.go", RiskScore: 100},
	}

	got := applyFindingsBudget(comments, files, 2)

	if !got[0].Suppressed || got[0].DropReason != "findings_budget" {
		t.Fatalf("first comment = suppressed %v reason %q, want findings_budget suppression", got[0].Suppressed, got[0].DropReason)
	}
	if got[1].Suppressed {
		t.Fatalf("critical comment should remain postable")
	}
	if got[2].Suppressed {
		t.Fatalf("high severity comment should remain postable")
	}
}

func makeFiles(count int) []domain.DiffFile {
	files := make([]domain.DiffFile, 0, count)
	for i := range count {
		files = append(files, domain.DiffFile{Path: fmt.Sprintf("pkg/file-%d.go", i)})
	}
	return files
}
