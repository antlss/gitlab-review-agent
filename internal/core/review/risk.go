package review

import (
	"github.com/antlss/gitlab-review-agent/internal/domain"
	"math"
	"path/filepath"
	"strings"
)

var (
	highRiskKeywords   = []string{"migration", "schema", "auth", "security", "middleware", "permission", "role"}
	mediumRiskKeywords = []string{"config", "setting", "env", "domain", "model", "entity", "service"}

	defaultExcludePatterns = []string{
		"**/*.pb.go", "vendor/**", "**/*_mock.go", "**/*.generated.go",
		"**/*.min.js", "**/*.min.css", "go.sum", "package-lock.json", "yarn.lock",
	}

	extScore = map[string]float64{
		".sql": 15, ".proto": 12, ".go": 5, ".ts": 5, ".py": 5,
		".yaml": 8, ".yml": 8, ".json": 3, ".md": -5, ".sh": 7,
	}
)

// DefaultExcludePatterns returns the default file exclude patterns.
func DefaultExcludePatterns() []string {
	return defaultExcludePatterns
}

// ScoreRisk calculates risk score and tier for a diff file.
func ScoreRisk(file *domain.DiffFile) {
	score := 0.0
	lowerPath := strings.ToLower(file.Path)

	for _, kw := range highRiskKeywords {
		if strings.Contains(lowerPath, kw) {
			score += 10
		}
	}
	for _, kw := range mediumRiskKeywords {
		if strings.Contains(lowerPath, kw) {
			score += 5
		}
	}

	ext := filepath.Ext(file.Path)
	if s, ok := extScore[ext]; ok {
		score += s
	}

	totalLines := float64(file.LinesAdded + file.LinesRemoved)
	if totalLines > 0 {
		score += math.Log10(totalLines) * 3
	}

	if file.Status == "A" {
		score += 5
	}

	if strings.Contains(file.Path, "_test.") || strings.Contains(file.Path, "test/") {
		score -= 8
	}

	file.RiskScore = score
	switch {
	case score >= 15:
		file.RiskTier = domain.RiskHigh
	case score >= 7:
		file.RiskTier = domain.RiskMedium
	default:
		file.RiskTier = domain.RiskLow
	}
}

// CalculateBudget returns maxIterations and softWarnAt based on file count.
// These are raw budgets for non-preloaded, non-chunked reviews.
func CalculateBudget(fileCount int) (maxIter, softWarn int) {
	switch {
	case fileCount <= 3:
		return 8, 6
	case fileCount <= 8:
		return 12, 9
	case fileCount <= 15:
		return 15, 11
	case fileCount <= 30:
		return 20, 15
	default:
		return 25, 19
	}
}

// CalculateBudgetWithPreload returns a tighter budget when diffs are pre-injected.
// When all diffs are preloaded, the agent only needs tool calls for context exploration,
// not for fetching diffs — so the budget can be significantly reduced.
func CalculateBudgetWithPreload(fileCount int, diffsPreloaded bool) (maxIter, softWarn int) {
	if !diffsPreloaded {
		return CalculateBudget(fileCount)
	}
	// With all diffs preloaded, the agent needs far fewer iterations.
	// Each file needs at most ~0.5-1 tool calls for context, not 1-2 for fetching diffs.
	switch {
	case fileCount <= 3:
		return 5, 3
	case fileCount <= 8:
		return 8, 5
	case fileCount <= 15:
		return 10, 7
	default:
		return 12, 9
	}
}

// ShouldExclude checks if a file path matches any exclude pattern.
func ShouldExclude(path string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, path); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(path)); matched {
			return true
		}
		if strings.HasPrefix(pattern, "**") {
			suffix := strings.TrimPrefix(pattern, "**")
			if strings.HasSuffix(path, suffix) || strings.Contains(path, strings.TrimPrefix(suffix, "/")) {
				return true
			}
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
	}
	return false
}
