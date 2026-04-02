package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

type ParsedOutput struct {
	Reviews []RawReview `json:"reviews"`
}

type RawReview struct {
	FilePath      string `json:"filePath"`
	LineNumber    int    `json:"lineNumber"`
	ReviewComment string `json:"reviewComment"`
	Confidence    string `json:"confidence"`
	Severity      string `json:"severity"`
	Category      string `json:"category"`
	Suggestion    string `json:"suggestion,omitempty"`
}

// Parse tries 4 strategies in order, stopping at first success.
func Parse(rawOutput string) (*ParsedOutput, error) {
	strategies := []func(string) (*ParsedOutput, error){
		parseDirectJSON,
		parseMarkdownCodeFence,
		parseXMLTags,
		parseJSONScan,
	}

	for _, strategy := range strategies {
		result, err := strategy(rawOutput)
		if err == nil && result != nil {
			return result, nil
		}
	}
	return nil, fmt.Errorf("all parse strategies failed")
}

func parseDirectJSON(s string) (*ParsedOutput, error) {
	s = strings.TrimSpace(s)
	var out ParsedOutput
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out.Reviews == nil {
		return nil, fmt.Errorf("no reviews key")
	}
	return &out, nil
}

func parseMarkdownCodeFence(s string) (*ParsedOutput, error) {
	re := regexp.MustCompile("```(?:json)?\\s*([\\s\\S]+?)```")
	matches := re.FindStringSubmatch(s)
	if len(matches) < 2 {
		return nil, fmt.Errorf("no code fence")
	}
	return parseDirectJSON(matches[1])
}

func parseXMLTags(s string) (*ParsedOutput, error) {
	for _, tag := range []string{"review", "json", "output"} {
		re := regexp.MustCompile(fmt.Sprintf("<%s>([\\s\\S]+?)</%s>", tag, tag))
		matches := re.FindStringSubmatch(s)
		if len(matches) >= 2 {
			if result, err := parseDirectJSON(matches[1]); err == nil {
				return result, nil
			}
		}
	}
	return nil, fmt.Errorf("no XML tags found")
}

func parseJSONScan(s string) (*ParsedOutput, error) {
	start := strings.Index(s, `{"reviews"`)
	if start == -1 {
		start = strings.Index(s, `{ "reviews"`)
	}
	if start == -1 {
		return nil, fmt.Errorf("no reviews key found")
	}

	depth := 0
	end := -1
	inString := false
	escaped := false
scan:
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i + 1
				break scan
			}
		}
	}
	if end == -1 {
		return nil, fmt.Errorf("no matching brace")
	}
	return parseDirectJSON(s[start:end])
}

// ValidateAndFilter validates parsed comments against diff files and filters duplicates.
func ValidateAndFilter(
	parsed *ParsedOutput,
	diffFiles []domain.DiffFile,
	existingComments []domain.ExistingComment,
) []domain.ParsedComment {
	addedLinesMap := make(map[string]map[int]bool)
	for _, f := range diffFiles {
		lines := make(map[int]bool)
		for _, l := range f.AddedLines {
			lines[l] = true
		}
		addedLinesMap[f.Path] = lines
	}

	existingLocationSet := make(map[string]bool)
	existingContentSet := make(map[string]bool)
	existingSemanticSet := make(map[string]bool)
	existingLocationFingerprintSet := make(map[string]bool)
	for _, c := range existingComments {
		locationKey := fmt.Sprintf("%s:%d", c.FilePath, c.LineNumber)
		existingLocationSet[locationKey] = true
		if c.ContentHash != "" {
			existingContentSet[c.ContentHash] = true
		}
		if c.SemanticFingerprint != "" {
			existingSemanticSet[c.SemanticFingerprint] = true
		}
		if c.LocationFingerprint != "" {
			existingLocationFingerprintSet[c.LocationFingerprint] = true
		}
	}

	validSeverities := map[string]domain.CommentSeverity{
		"critical": domain.SeverityCritical,
		"high":     domain.SeverityHigh,
		"medium":   domain.SeverityMedium,
		"low":      domain.SeverityLow,
	}

	var results []domain.ParsedComment
	seenLocation := make(map[string]bool)
	seenContent := make(map[string]bool)
	seenSemantic := make(map[string]bool)
	seenLocationFingerprint := make(map[string]bool)

	for _, r := range parsed.Reviews {
		comment := domain.ParsedComment{
			FilePath:      r.FilePath,
			LineNumber:    r.LineNumber,
			ReviewComment: r.ReviewComment,
			Confidence:    strings.ToUpper(r.Confidence),
			Category:      domain.CommentCategory(strings.ToLower(r.Category)),
			Severity:      domain.CommentSeverity(strings.ToLower(r.Severity)),
			Suggestion:    r.Suggestion,
		}
		comment.ContentHash = buildContentHash(comment.ReviewComment)
		comment.SemanticFingerprint = buildSemanticFingerprint(comment.Category, comment.ReviewComment)
		comment.LocationFingerprint = buildLocationFingerprint(comment.FilePath, comment.LineNumber, comment.Category)

		if _, ok := validSeverities[string(comment.Severity)]; !ok {
			switch comment.Confidence {
			case "HIGH":
				comment.Severity = domain.SeverityHigh
			case "LOW":
				comment.Severity = domain.SeverityLow
			default:
				comment.Severity = domain.SeverityMedium
			}
		}

		if comment.Confidence != "HIGH" && comment.Confidence != "MEDIUM" && comment.Confidence != "LOW" {
			comment.Confidence = "MEDIUM"
		}

		if addedLines, ok := addedLinesMap[comment.FilePath]; ok {
			if !addedLines[comment.LineNumber] && len(addedLines) > 0 {
				comment.Suppressed = true
				comment.DropReason = "invalid_line"
			}
		}

		locationKey := fmt.Sprintf("%s:%d", comment.FilePath, comment.LineNumber)
		if !comment.Suppressed && existingLocationSet[locationKey] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}
		if !comment.Suppressed && comment.ContentHash != "" && existingContentSet[comment.ContentHash] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}
		if !comment.Suppressed && comment.SemanticFingerprint != "" && existingSemanticSet[comment.SemanticFingerprint] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}
		if !comment.Suppressed && comment.LocationFingerprint != "" && existingLocationFingerprintSet[comment.LocationFingerprint] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}

		if comment.Confidence == "LOW" {
			comment.Suppressed = true
			comment.DropReason = "low_confidence"
		}

		if !comment.Suppressed && !isAllowedFinalCategory(string(comment.Category)) {
			comment.Suppressed = true
			comment.DropReason = "non_production_category"
		}

		if !comment.Suppressed && comment.Severity == domain.SeverityLow {
			comment.Suppressed = true
			comment.DropReason = "low_severity"
		}

		if !comment.Suppressed && isNonActionableReviewComment(comment.ReviewComment) {
			comment.Suppressed = true
			comment.DropReason = "non_actionable"
		}

		if !comment.Suppressed && seenLocation[locationKey] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}
		if !comment.Suppressed && comment.ContentHash != "" && seenContent[comment.ContentHash] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}
		if !comment.Suppressed && comment.SemanticFingerprint != "" && seenSemantic[comment.SemanticFingerprint] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}
		if !comment.Suppressed && comment.LocationFingerprint != "" && seenLocationFingerprint[comment.LocationFingerprint] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}

		seenLocation[locationKey] = true
		if comment.ContentHash != "" {
			seenContent[comment.ContentHash] = true
		}
		if comment.SemanticFingerprint != "" {
			seenSemantic[comment.SemanticFingerprint] = true
		}
		if comment.LocationFingerprint != "" {
			seenLocationFingerprint[comment.LocationFingerprint] = true
		}

		results = append(results, comment)
	}

	return results
}

func buildContentHash(reviewComment string) string {
	return hashFingerprint(normalizeFingerprintText(reviewComment))
}

func buildSemanticFingerprint(category domain.CommentCategory, reviewComment string) string {
	return hashFingerprint(strings.ToLower(strings.TrimSpace(string(category))) + "|" + normalizeFingerprintText(reviewComment))
}

func buildLocationFingerprint(filePath string, lineNumber int, category domain.CommentCategory) string {
	return hashFingerprint(strings.ToLower(strings.TrimSpace(filePath)) + "|" + fmt.Sprintf("%d", lineNumber) + "|" + strings.ToLower(strings.TrimSpace(string(category))))
}

func normalizeFingerprintText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func hashFingerprint(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func isAllowedFinalCategory(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "security", "bug", "logic", "performance":
		return true
	default:
		return false
	}
}

func isNonActionableReviewComment(comment string) bool {
	lower := strings.ToLower(strings.TrimSpace(comment))
	if lower == "" {
		return true
	}
	if strings.HasSuffix(lower, "?") {
		return true
	}

	phrases := []string{
		"please check",
		"should check",
		"needs verification",
		"requires deeper audit",
		"could you verify",
		"hãy kiểm tra",
		"kiểm tra lại",
		"cần kiểm tra",
		"xác nhận lại",
	}
	for _, phrase := range phrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}
