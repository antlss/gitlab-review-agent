package review

import (
	"testing"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

func TestFilterExtractedCandidates(t *testing.T) {
	candidates := []extractedCandidate{
		{
			FilePath:         "a.go",
			LineNumber:       10,
			Summary:          "rename this field",
			Severity:         "high",
			Confidence:       "HIGH",
			Category:         "naming",
			FailureMode:      "none",
			ProductionImpact: "none",
		},
		{
			FilePath:         "a.go",
			LineNumber:       11,
			Summary:          "missing failure mode",
			Severity:         "high",
			Confidence:       "HIGH",
			Category:         "logic",
			FailureMode:      "",
			ProductionImpact: "request can panic",
		},
		{
			FilePath:          "a.go",
			LineNumber:        12,
			Summary:           "stale permission cache after write",
			Severity:          "high",
			Confidence:        "HIGH",
			Category:          "logic",
			FailureMode:       "the read path can serve stale permissions until another sync occurs",
			ProductionImpact:  "users can be authorized with outdated permission data",
			NeedsVerification: true,
			Verification: candidateVerifySpec{
				Paths:    []string{"dep.go", "dep.go"},
				Symbols:  []string{"SyncPermissions"},
				Patterns: []string{"SyncPermissions"},
			},
		},
		{
			FilePath:         "a.go",
			LineNumber:       13,
			Summary:          "already reported",
			Severity:         "critical",
			Confidence:       "HIGH",
			Category:         "security",
			FailureMode:      "auth bypass",
			ProductionImpact: "unauthorized access",
		},
	}

	diffFiles := []domain.DiffFile{
		{Path: "a.go", AddedLines: []int{10, 11, 12, 13}},
	}

	existing := []domain.ExistingComment{
		{FilePath: "a.go", LineNumber: 13, Summary: "same issue"},
	}

	filtered := filterExtractedCandidates(candidates, diffFiles, existing)
	if len(filtered) != 1 {
		t.Fatalf("expected exactly 1 candidate, got %d", len(filtered))
	}

	got := filtered[0]
	if got.FilePath != "a.go" || got.LineNumber != 12 {
		t.Fatalf("unexpected candidate kept: %+v", got)
	}
	if !got.NeedsVerification {
		t.Fatal("expected kept candidate to still require verification")
	}
	if len(got.Verification.Paths) != 1 || got.Verification.Paths[0] != "dep.go" {
		t.Fatalf("expected verification paths to be deduplicated, got %+v", got.Verification.Paths)
	}
}
