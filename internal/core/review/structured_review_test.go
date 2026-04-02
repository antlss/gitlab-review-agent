package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

func TestParseExtractionOutputFromPrettyPrintedFence(t *testing.T) {
	input := "Candidate extraction:\n```json\n{\n  \"candidates\": [\n    {\n      \"filePath\": \"a.go\",\n      \"lineNumber\": 12,\n      \"summary\": \"stale permission cache after write\",\n      \"severity\": \"high\",\n      \"confidence\": \"HIGH\",\n      \"category\": \"logic\",\n      \"failureMode\": \"the read path can serve stale permissions until another sync occurs\",\n      \"productionImpact\": \"users can be authorized with outdated permission data\",\n      \"needsVerification\": false,\n      \"verification\": {\n        \"paths\": [],\n        \"symbols\": [],\n        \"patterns\": []\n      }\n    }\n  ]\n}\n```"

	output, err := parseExtractionOutput(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(output.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(output.Candidates))
	}
	if output.Candidates[0].FilePath != "a.go" || output.Candidates[0].LineNumber != 12 {
		t.Fatalf("unexpected parsed candidate: %+v", output.Candidates[0])
	}
}

func TestExtractJSONForKeyWithNestedPrettyPrintedObject(t *testing.T) {
	input := "prefix\n```json\n{\n  \"meta\": \"ignored\",\n  \"candidates\": []\n}\n```\nsuffix"

	jsonBlob, err := extractJSONForKey(input, "candidates")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jsonBlob == "" {
		t.Fatal("expected extracted JSON blob")
	}
}

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

func TestBuildExcerptSectionsSupportsTieredRadius(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	numbered := addLineNumbers(lines)

	extraction := buildExcerptSections(numbered, []int{30}, len(numbered), extractionExcerptRadius, extractionExcerptMaxSections)
	verification := buildExcerptSections(numbered, []int{30}, len(numbered), verificationExcerptRadius, verificationExcerptMaxSections)

	if !strings.Contains(extraction, "lines 18-42") {
		t.Fatalf("extraction excerpt = %q, want tighter radius", extraction)
	}
	if !strings.Contains(verification, "lines 1-60") {
		t.Fatalf("verification excerpt = %q, want broader context window", verification)
	}
}
