package review

import (
	"cmp"
	"slices"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

type ReviewMode string

const (
	ReviewModeUnified ReviewMode = "unified"
	ReviewModeChunked ReviewMode = "chunked"
	ReviewModeTriage  ReviewMode = "triage"
)

type Plan struct {
	Mode            ReviewMode
	Files           []domain.DiffFile
	FindingsBudget  int
	OriginalFileCnt int
}

type PlanInput struct {
	ChunkThreshold  int
	TriageThreshold int
	SampleFileCount int
	MaxFindings     int
}

func PlanReview(input PlanInput, files []domain.DiffFile) Plan {
	plannedFiles := append([]domain.DiffFile(nil), files...)
	plan := Plan{
		Mode:            ReviewModeUnified,
		Files:           plannedFiles,
		FindingsBudget:  normalizeFindingsBudget(input.MaxFindings),
		OriginalFileCnt: len(files),
	}

	if len(files) >= normalizeTriageThreshold(input.TriageThreshold, input.ChunkThreshold) {
		plan.Mode = ReviewModeTriage
		if input.SampleFileCount > 0 && len(plan.Files) > input.SampleFileCount {
			plan.Files = plan.Files[:input.SampleFileCount]
		}
		return plan
	}

	if len(files) >= normalizeChunkedThreshold(input.ChunkThreshold) {
		plan.Mode = ReviewModeChunked
	}

	return plan
}

func applyFindingsBudget(comments []domain.ParsedComment, diffFiles []domain.DiffFile, maxFindings int) []domain.ParsedComment {
	if maxFindings <= 0 {
		return comments
	}

	var unsuppressed []int
	for i := range comments {
		if !comments[i].Suppressed {
			unsuppressed = append(unsuppressed, i)
		}
	}
	if len(unsuppressed) <= maxFindings {
		return comments
	}

	fileRisk := make(map[string]float64, len(diffFiles))
	for _, diffFile := range diffFiles {
		fileRisk[diffFile.Path] = diffFile.RiskScore
	}

	slices.SortFunc(unsuppressed, func(a, b int) int {
		left := comments[a]
		right := comments[b]
		if rank := cmp.Compare(severityRank(string(right.Severity)), severityRank(string(left.Severity))); rank != 0 {
			return rank
		}
		if rank := cmp.Compare(confidenceRank(right.Confidence), confidenceRank(left.Confidence)); rank != 0 {
			return rank
		}
		if rank := cmp.Compare(fileRisk[right.FilePath], fileRisk[left.FilePath]); rank != 0 {
			return rank
		}
		if rank := cmp.Compare(left.FilePath, right.FilePath); rank != 0 {
			return rank
		}
		return cmp.Compare(left.LineNumber, right.LineNumber)
	})

	for _, idx := range unsuppressed[maxFindings:] {
		comments[idx].Suppressed = true
		comments[idx].DropReason = "findings_budget"
	}

	return comments
}

func normalizeChunkedThreshold(value int) int {
	if value > 1 {
		return value
	}
	return 21
}

func normalizeTriageThreshold(value, chunkedThreshold int) int {
	if value > normalizeChunkedThreshold(chunkedThreshold) {
		return value
	}
	return 81
}

func normalizeFindingsBudget(value int) int {
	if value > 0 {
		return value
	}
	return 10
}
