package review

import (
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
	for i := start; i < len(s); i++ {
		if s[i] == '{' {
			depth++
		}
		if s[i] == '}' {
			depth--
			if depth == 0 {
				end = i + 1
				break
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

	existingSet := make(map[string]bool)
	for _, c := range existingComments {
		key := fmt.Sprintf("%s:%d", c.FilePath, c.LineNumber)
		existingSet[key] = true
	}

	validCategories := map[string]bool{
		"security": true, "bug": true, "logic": true,
		"performance": true, "naming": true, "style": true,
	}

	validSeverities := map[string]domain.CommentSeverity{
		"critical": domain.SeverityCritical,
		"high":     domain.SeverityHigh,
		"medium":   domain.SeverityMedium,
		"low":      domain.SeverityLow,
	}

	var results []domain.ParsedComment
	seen := make(map[string]bool)

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

		if !validCategories[string(comment.Category)] {
			comment.Category = domain.CategoryLogic
		}

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

		key := fmt.Sprintf("%s:%d", comment.FilePath, comment.LineNumber)
		if existingSet[key] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}

		if comment.Confidence == "LOW" {
			comment.Suppressed = true
			comment.DropReason = "low_confidence"
		}

		dedupKey := fmt.Sprintf("%s:%d:%s", comment.FilePath, comment.LineNumber, comment.Category)
		if seen[dedupKey] {
			comment.Suppressed = true
			comment.DropReason = "duplicate"
		}
		seen[dedupKey] = true

		results = append(results, comment)
	}

	return results
}
