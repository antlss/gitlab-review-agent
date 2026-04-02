package review

import (
	"testing"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

func TestParseDirectJSON(t *testing.T) {
	input := `{"reviews": [{"filePath": "main.go", "lineNumber": 10, "reviewComment": "issue", "confidence": "HIGH", "category": "logic"}]}`
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(result.Reviews))
	}
	if result.Reviews[0].FilePath != "main.go" {
		t.Errorf("expected main.go, got %s", result.Reviews[0].FilePath)
	}
}

func TestParseMarkdownFence(t *testing.T) {
	input := "Some text before\n```json\n{\"reviews\": [{\"filePath\": \"a.go\", \"lineNumber\": 5, \"reviewComment\": \"test\", \"confidence\": \"MEDIUM\", \"category\": \"style\"}]}\n```\nSome text after"
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(result.Reviews))
	}
}

func TestParseXMLTags(t *testing.T) {
	input := `<review>{"reviews": [{"filePath": "b.go", "lineNumber": 1, "reviewComment": "x", "confidence": "LOW", "category": "naming"}]}</review>`
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(result.Reviews))
	}
}

func TestParseJSONScan(t *testing.T) {
	input := `Here is my review output: {"reviews": [{"filePath": "c.go", "lineNumber": 20, "reviewComment": "bug", "confidence": "HIGH", "category": "security"}]} That's all.`
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(result.Reviews))
	}
}

func TestParseJSONScanHandlesBracesInsideJSONString(t *testing.T) {
	input := `Result: {"reviews": [{"filePath": "c.go", "lineNumber": 20, "reviewComment": "bug", "confidence": "HIGH", "category": "logic", "suggestion": "if err != nil {\n    return err\n}"}]} trailing text`
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(result.Reviews))
	}
	if result.Reviews[0].Suggestion == "" {
		t.Fatal("expected suggestion to be preserved")
	}
}

func TestParseEmptyReviews(t *testing.T) {
	input := `{"reviews": []}`
	result, err := Parse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Reviews) != 0 {
		t.Fatalf("expected 0 reviews, got %d", len(result.Reviews))
	}
}

func TestParseAllStrategiesFail(t *testing.T) {
	input := "This is just plain text with no JSON at all"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestValidateAndFilter(t *testing.T) {
	parsed := &ParsedOutput{
		Reviews: []RawReview{
			{FilePath: "a.go", LineNumber: 10, ReviewComment: "This dereference can panic when the lookup misses.", Confidence: "HIGH", Category: "logic", Severity: "high"},
			{FilePath: "a.go", LineNumber: 20, ReviewComment: "issue2", Confidence: "LOW", Category: "style", Severity: "low"},
			{FilePath: "a.go", LineNumber: 99, ReviewComment: "issue3", Confidence: "HIGH", Category: "security", Severity: "high"},
		},
	}

	diffFiles := []domain.DiffFile{
		{Path: "a.go", AddedLines: []int{10, 11, 12, 20, 21}},
	}

	results := ValidateAndFilter(parsed, diffFiles, nil)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// First: valid HIGH
	if results[0].Suppressed {
		t.Error("first comment should not be suppressed")
	}
	// Second: LOW confidence → suppressed
	if !results[1].Suppressed || results[1].DropReason != "low_confidence" {
		t.Error("second comment should be suppressed for low confidence")
	}
	// Third: invalid line → suppressed
	if !results[2].Suppressed || results[2].DropReason != "invalid_line" {
		t.Error("third comment should be suppressed for invalid line")
	}
}

func TestValidateAndFilterSuppressesNonProductionAndNonActionable(t *testing.T) {
	parsed := &ParsedOutput{
		Reviews: []RawReview{
			{FilePath: "a.go", LineNumber: 10, ReviewComment: "Please check whether this is safe.", Confidence: "HIGH", Category: "logic", Severity: "high"},
			{FilePath: "a.go", LineNumber: 11, ReviewComment: "Rename this field for consistency.", Confidence: "HIGH", Category: "naming", Severity: "high"},
			{FilePath: "a.go", LineNumber: 12, ReviewComment: "This can return stale permission data after the write succeeds.", Confidence: "HIGH", Category: "logic", Severity: "low"},
		},
	}

	diffFiles := []domain.DiffFile{
		{Path: "a.go", AddedLines: []int{10, 11, 12}},
	}

	results := ValidateAndFilter(parsed, diffFiles, nil)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if !results[0].Suppressed || results[0].DropReason != "non_actionable" {
		t.Fatalf("expected first review to be suppressed as non_actionable, got suppressed=%v reason=%s", results[0].Suppressed, results[0].DropReason)
	}
	if !results[1].Suppressed || results[1].DropReason != "non_production_category" {
		t.Fatalf("expected second review to be suppressed as non_production_category, got suppressed=%v reason=%s", results[1].Suppressed, results[1].DropReason)
	}
	if !results[2].Suppressed || results[2].DropReason != "low_severity" {
		t.Fatalf("expected third review to be suppressed as low_severity, got suppressed=%v reason=%s", results[2].Suppressed, results[2].DropReason)
	}
}

func TestValidateAndFilterComputesFingerprints(t *testing.T) {
	parsed := &ParsedOutput{
		Reviews: []RawReview{{
			FilePath:      "a.go",
			LineNumber:    10,
			ReviewComment: "This dereference can panic when the lookup misses.",
			Confidence:    "HIGH",
			Category:      "logic",
			Severity:      "high",
		}},
	}

	diffFiles := []domain.DiffFile{{Path: "a.go", AddedLines: []int{10}}}
	results := ValidateAndFilter(parsed, diffFiles, nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ContentHash == "" || results[0].SemanticFingerprint == "" || results[0].LocationFingerprint == "" {
		t.Fatalf("expected fingerprints to be populated, got %+v", results[0])
	}
}

func TestValidateAndFilterSuppressesSemanticDuplicatesAgainstExistingComments(t *testing.T) {
	parsed := &ParsedOutput{
		Reviews: []RawReview{{
			FilePath:      "other.go",
			LineNumber:    42,
			ReviewComment: "This dereference can panic when the lookup misses.",
			Confidence:    "HIGH",
			Category:      "logic",
			Severity:      "high",
		}},
	}

	existing := []domain.ExistingComment{{
		FilePath:            "a.go",
		LineNumber:          10,
		Summary:             "This dereference can panic when the lookup misses.",
		SemanticFingerprint: buildSemanticFingerprint(domain.CommentCategory("logic"), "This dereference can panic when the lookup misses."),
	}}

	diffFiles := []domain.DiffFile{{Path: "other.go", AddedLines: []int{42}}}
	results := ValidateAndFilter(parsed, diffFiles, existing)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Suppressed || results[0].DropReason != "duplicate" {
		t.Fatalf("expected semantic duplicate to be suppressed, got suppressed=%v reason=%s", results[0].Suppressed, results[0].DropReason)
	}
}

func TestValidateAndFilterSuppressesDuplicateReviewsWithinBatchBySemanticFingerprint(t *testing.T) {
	parsed := &ParsedOutput{
		Reviews: []RawReview{
			{FilePath: "a.go", LineNumber: 10, ReviewComment: "This dereference can panic when the lookup misses.", Confidence: "HIGH", Category: "logic", Severity: "high"},
			{FilePath: "b.go", LineNumber: 20, ReviewComment: "This dereference can panic when the lookup misses.", Confidence: "HIGH", Category: "logic", Severity: "high"},
		},
	}

	diffFiles := []domain.DiffFile{
		{Path: "a.go", AddedLines: []int{10}},
		{Path: "b.go", AddedLines: []int{20}},
	}

	results := ValidateAndFilter(parsed, diffFiles, nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Suppressed {
		t.Fatal("expected first review to survive dedupe")
	}
	if !results[1].Suppressed || results[1].DropReason != "duplicate" {
		t.Fatalf("expected second semantic duplicate to be suppressed, got suppressed=%v reason=%s", results[1].Suppressed, results[1].DropReason)
	}
}

func TestValidateAndFilterSuppressesLocationDuplicatesAgainstExistingComments(t *testing.T) {
	parsed := &ParsedOutput{
		Reviews: []RawReview{{
			FilePath:      "a.go",
			LineNumber:    10,
			ReviewComment: "new wording for the same risky line",
			Confidence:    "HIGH",
			Category:      "logic",
			Severity:      "high",
		}},
	}

	existing := []domain.ExistingComment{{
		FilePath:            "a.go",
		LineNumber:          10,
		Summary:             "older comment",
		LocationFingerprint: buildLocationFingerprint("a.go", 10, domain.CommentCategory("logic")),
	}}

	diffFiles := []domain.DiffFile{{Path: "a.go", AddedLines: []int{10}}}
	results := ValidateAndFilter(parsed, diffFiles, existing)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Suppressed || results[0].DropReason != "duplicate" {
		t.Fatalf("expected location duplicate to be suppressed, got suppressed=%v reason=%s", results[0].Suppressed, results[0].DropReason)
	}
}

func TestBuildFingerprintHelpersNormalizeInput(t *testing.T) {
	left := buildSemanticFingerprint(domain.CommentCategory("logic"), "  This dereference can panic   when the lookup misses. ")
	right := buildSemanticFingerprint(domain.CommentCategory("logic"), "this dereference can panic when the lookup misses.")
	if left != right {
		t.Fatalf("expected normalized semantic fingerprints to match, got %q vs %q", left, right)
	}
}
