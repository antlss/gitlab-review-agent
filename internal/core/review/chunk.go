package review

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

// ChunkFiles groups diff files by domain/directory and balances into chunks of
// approximately targetSize files each. Related files (same domain) stay together
// so the agent can review them with shared context, reducing exploratory tool calls.
func ChunkFiles(files []domain.DiffFile, targetSize int) [][]domain.DiffFile {
	if len(files) == 0 {
		return nil
	}
	if targetSize <= 0 {
		targetSize = 10
	}

	// If total files fit in one chunk, return as-is
	if len(files) <= targetSize {
		return [][]domain.DiffFile{files}
	}

	// Group files by top-level domain directory
	groups := groupByDomain(files)

	// Sort groups by total risk score (highest first) so the riskiest chunks run first
	slices.SortFunc(groups, func(a, b []domain.DiffFile) int {
		return cmp.Compare(groupRiskScore(b), groupRiskScore(a)) // descending
	})

	// Balance groups into chunks of ~targetSize
	return balanceChunks(groups, targetSize)
}

// groupByDomain groups files by their top-level directory (e.g., "domains/partner",
// "entities", "singleton"). Files in the same domain directory are likely related
// and benefit from being reviewed together.
func groupByDomain(files []domain.DiffFile) [][]domain.DiffFile {
	domainMap := make(map[string][]domain.DiffFile)
	var order []string

	for _, f := range files {
		key := domainKey(f.Path)
		if _, exists := domainMap[key]; !exists {
			order = append(order, key)
		}
		domainMap[key] = append(domainMap[key], f)
	}

	groups := make([][]domain.DiffFile, 0, len(order))
	for _, key := range order {
		groups = append(groups, domainMap[key])
	}
	return groups
}

// domainKey extracts a grouping key from a file path.
// For "domains/partner/biz/invite.go" → "domains/partner"
// For "entities/enum/job.go" → "entities"
// For "singleton/csv/marshal.go" → "singleton/csv"
// For "gen/db/entities.go" → "gen"
func domainKey(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) <= 1 {
		return "_root"
	}

	// For "domains/X/..." use first two levels to keep related domain files together
	if parts[0] == "domains" && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}

	// For other top-level dirs, use first level (entities, gen, singleton, etc.)
	// but if second level is also a directory, include it for better grouping
	if len(parts) >= 3 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

func groupRiskScore(files []domain.DiffFile) float64 {
	total := 0.0
	for _, f := range files {
		total += f.RiskScore
	}
	return total
}

// balanceChunks merges small groups and splits large groups to achieve ~targetSize per chunk.
func balanceChunks(groups [][]domain.DiffFile, targetSize int) [][]domain.DiffFile {
	var chunks [][]domain.DiffFile
	var current []domain.DiffFile

	for _, group := range groups {
		// If a single group exceeds target, split it into its own chunks
		if len(group) > targetSize {
			// Flush current accumulator first
			if len(current) > 0 {
				chunks = append(chunks, current)
				current = nil
			}
			for i := 0; i < len(group); i += targetSize {
				end := i + targetSize
				if end > len(group) {
					end = len(group)
				}
				chunks = append(chunks, group[i:end])
			}
			continue
		}

		// If adding this group would exceed target, flush current and start new chunk
		if len(current)+len(group) > targetSize {
			if len(current) > 0 {
				chunks = append(chunks, current)
				current = nil
			}
		}
		current = append(current, group...)
	}

	if len(current) > 0 {
		chunks = append(chunks, current)
	}

	return chunks
}
