package review

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/domain"
	"github.com/antlss/gitlab-review-agent/internal/pkg/tools"
)

const (
	structuredReviewMaxTokens   = 4096
	bundleFullFileMaxBytes      = 24 * 1024
	bundleFullFileMaxLines      = 400
	bundleExcerptRadius         = 30
	bundleExcerptMaxSections    = 6
	maxExtractedCandidates      = 8
	maxVerificationPaths        = 12
	maxVerificationSymbols      = 8
	maxVerificationPatterns     = 8
)

type structuredChunkResult struct {
	parsed          *ParsedOutput
	rawOutput       string
	llmCalls        int
	tokensEstimated int
	stopReason      string
}

type reviewBundle struct {
	Files []reviewBundleFile
}

type reviewBundleFile struct {
	Path         string
	RiskTier     domain.RiskTier
	LinesAdded   int
	LinesRemoved int
	Diff         string
	Content      string
	ContentMode  string
}

type extractionOutput struct {
	Candidates []extractedCandidate `json:"candidates"`
}

type extractedCandidate struct {
	FilePath          string              `json:"filePath"`
	LineNumber        int                 `json:"lineNumber"`
	Summary           string              `json:"summary"`
	Severity          string              `json:"severity"`
	Confidence        string              `json:"confidence"`
	Category          string              `json:"category"`
	FailureMode       string              `json:"failureMode"`
	ProductionImpact  string              `json:"productionImpact"`
	NeedsVerification bool                `json:"needsVerification"`
	Verification      candidateVerifySpec `json:"verification"`
}

type candidateVerifySpec struct {
	Paths    []string `json:"paths"`
	Symbols  []string `json:"symbols"`
	Patterns []string `json:"patterns"`
}

func (p *Pipeline) runStructuredChunkReview(
	ctx context.Context,
	job *domain.ReviewJob,
	chunkFiles []domain.DiffFile,
	reviewCtx *domain.ReviewContext,
	llmClient domain.LLMClient,
	baseSHA string,
	lang prompt.ResponseLanguage,
) (*structuredChunkResult, error) {
	repoPath := p.gitManager.RepoPath(job.GitLabProjectID)
	toolCfg := p.cfg.Tool
	toolCfg.BaseSHA = baseSHA
	toolCfg.HeadSHA = job.HeadSHA
	toolCfg.GitEnv = p.gitManager.GitEnv()
	registry := tools.NewRegistry(repoPath, chunkFiles, toolCfg)

	bundle, err := p.buildReviewBundle(ctx, job.GitLabProjectID, chunkFiles, baseSHA, job.HeadSHA)
	if err != nil {
		return nil, err
	}

	extractReq := domain.ChatRequest{
		Model:       llmClient.ModelName(),
		MaxTokens:   structuredReviewMaxTokens,
		Temperature: 0.1,
		Messages: []domain.ChatMessage{
			{Role: "system", Content: prompt.StructuredExtractionSystemPrompt(reviewCtx)},
			{Role: "user", Content: buildExtractionUserPrompt(reviewCtx, chunkFiles, bundle)},
		},
	}

	extractResp, err := llmClient.Chat(ctx, extractReq)
	if err != nil {
		return nil, fmt.Errorf("extract candidates: %w", err)
	}

	totalTokens := extractResp.Usage.InputTokens + extractResp.Usage.OutputTokens
	candidateOutput, err := parseExtractionOutput(extractResp.Content)
	if err != nil {
		return nil, fmt.Errorf("parse candidates: %w", err)
	}

	candidates := filterExtractedCandidates(candidateOutput.Candidates, chunkFiles, reviewCtx.ExistingUnresolvedComments)
	if len(candidates) == 0 {
		return &structuredChunkResult{
			parsed:          &ParsedOutput{Reviews: []RawReview{}},
			rawOutput:       extractResp.Content,
			llmCalls:        1,
			tokensEstimated: totalTokens,
			stopReason:      "extract_complete_no_candidates",
		}, nil
	}

	verificationBundle := buildVerificationBundle(ctx, registry, reviewCtx.DetectedLanguage, candidates)
	candidateFiles := make(map[string]bool)
	for _, candidate := range candidates {
		candidateFiles[candidate.FilePath] = true
	}

	verifyReq := domain.ChatRequest{
		Model:       llmClient.ModelName(),
		MaxTokens:   structuredReviewMaxTokens,
		Temperature: 0.1,
		Messages: []domain.ChatMessage{
			{Role: "system", Content: prompt.StructuredVerificationSystemPrompt(reviewCtx, lang)},
			{Role: "user", Content: buildVerificationUserPrompt(reviewCtx, bundle.Filter(candidateFiles), candidates, verificationBundle)},
		},
	}

	verifyResp, err := llmClient.Chat(ctx, verifyReq)
	if err != nil {
		return nil, fmt.Errorf("verify candidates: %w", err)
	}

	totalTokens += verifyResp.Usage.InputTokens + verifyResp.Usage.OutputTokens
	parsed, err := Parse(verifyResp.Content)
	if err != nil {
		return nil, fmt.Errorf("parse verified reviews: %w", err)
	}

	return &structuredChunkResult{
		parsed:          parsed,
		rawOutput:       extractResp.Content + "\n--- VERIFIED ---\n" + verifyResp.Content,
		llmCalls:        2,
		tokensEstimated: totalTokens,
		stopReason:      "structured_verify_complete",
	}, nil
}

func (p *Pipeline) buildReviewBundle(
	ctx context.Context,
	projectID int64,
	files []domain.DiffFile,
	baseSHA, headSHA string,
) (reviewBundle, error) {
	bundle := reviewBundle{Files: make([]reviewBundleFile, 0, len(files))}

	for _, f := range files {
		diff, err := p.gitManager.DiffFile(ctx, projectID, baseSHA, headSHA, f.Path)
		if err != nil {
			return reviewBundle{}, fmt.Errorf("load diff for %s: %w", f.Path, err)
		}
		diff = CompactDiff(diff, 2)

		content, mode, err := p.readChangedFileContext(ctx, projectID, headSHA, f)
		if err != nil {
			return reviewBundle{}, fmt.Errorf("load changed file context for %s: %w", f.Path, err)
		}

		bundle.Files = append(bundle.Files, reviewBundleFile{
			Path:         f.Path,
			RiskTier:     f.RiskTier,
			LinesAdded:   f.LinesAdded,
			LinesRemoved: f.LinesRemoved,
			Diff:         diff,
			Content:      content,
			ContentMode:  mode,
		})
	}

	return bundle, nil
}

func (p *Pipeline) readChangedFileContext(ctx context.Context, projectID int64, headSHA string, diffFile domain.DiffFile) (string, string, error) {
	content, err := p.gitManager.ReadFileAtSHA(ctx, projectID, headSHA, diffFile.Path)
	if err != nil {
		return "", "", err
	}

	lines := strings.Split(string(content), "\n")
	numbered := addLineNumbers(lines)

	if len(content) <= bundleFullFileMaxBytes && len(lines) <= bundleFullFileMaxLines {
		return strings.Join(numbered, "\n"), "full", nil
	}

	return buildExcerptSections(numbered, diffFile.AddedLines, len(lines)), "excerpt", nil
}

func addLineNumbers(lines []string) []string {
	numbered := make([]string, len(lines))
	for i, line := range lines {
		numbered[i] = fmt.Sprintf("%d: %s", i+1, line)
	}
	return numbered
}

func buildExcerptSections(numberedLines []string, addedLines []int, totalLines int) string {
	if totalLines == 0 {
		return ""
	}

	sections := mergeLineWindows(addedLines, totalLines, bundleExcerptRadius, bundleExcerptMaxSections)
	if len(sections) == 0 {
		end := min(totalLines, bundleExcerptRadius*2)
		sections = append(sections, [2]int{1, end})
	}

	var sb strings.Builder
	for i, section := range sections {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("[excerpt %d: lines %d-%d]\n", i+1, section[0], section[1]))
		for line := section[0]; line <= section[1] && line <= len(numberedLines); line++ {
			sb.WriteString(numberedLines[line-1])
			sb.WriteByte('\n')
		}
	}
	return strings.TrimSpace(sb.String())
}

func mergeLineWindows(lines []int, totalLines, radius, maxSections int) [][2]int {
	if len(lines) == 0 {
		return nil
	}

	sorted := append([]int(nil), lines...)
	sort.Ints(sorted)

	var windows [][2]int
	for _, line := range sorted {
		start := max(1, line-radius)
		end := min(totalLines, line+radius)
		if len(windows) == 0 {
			windows = append(windows, [2]int{start, end})
			continue
		}

		last := &windows[len(windows)-1]
		if start <= last[1]+5 {
			if end > last[1] {
				last[1] = end
			}
			continue
		}

		windows = append(windows, [2]int{start, end})
	}

	if len(windows) > maxSections {
		return windows[:maxSections]
	}
	return windows
}

func (b reviewBundle) Render() string {
	var sb strings.Builder
	for i, file := range b.Files {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("### %s\n", file.Path))
		sb.WriteString(fmt.Sprintf("- Risk: %s\n", file.RiskTier))
		sb.WriteString(fmt.Sprintf("- Change size: +%d/-%d\n", file.LinesAdded, file.LinesRemoved))
		sb.WriteString("#### Diff\n```diff\n")
		sb.WriteString(strings.TrimSpace(file.Diff))
		sb.WriteString("\n```\n")
		sb.WriteString(fmt.Sprintf("#### Current File (%s)\n```\n", file.ContentMode))
		sb.WriteString(strings.TrimSpace(file.Content))
		sb.WriteString("\n```\n")
	}
	return strings.TrimSpace(sb.String())
}

func (b reviewBundle) Filter(paths map[string]bool) reviewBundle {
	if len(paths) == 0 {
		return reviewBundle{}
	}

	filtered := reviewBundle{}
	for _, file := range b.Files {
		if paths[file.Path] {
			filtered.Files = append(filtered.Files, file)
		}
	}
	return filtered
}

func buildExtractionUserPrompt(reviewCtx *domain.ReviewContext, files []domain.DiffFile, bundle reviewBundle) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## MR: %s\n\n", reviewCtx.MRTitle))
	if reviewCtx.MRDescription != "" {
		sb.WriteString("## Description\n")
		sb.WriteString(reviewCtx.MRDescription)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Changed Files\n")
	sb.WriteString(formatDiffStat(files))
	sb.WriteString("\n## Changed File Bundle\n")
	sb.WriteString(bundle.Render())
	return sb.String()
}

func buildVerificationUserPrompt(
	reviewCtx *domain.ReviewContext,
	bundle reviewBundle,
	candidates []extractedCandidate,
	verificationBundle string,
) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## MR: %s\n\n", reviewCtx.MRTitle))
	sb.WriteString("## Candidate Findings To Verify\n")
	for i, candidate := range candidates {
		sb.WriteString(fmt.Sprintf("%d. %s:%d [%s/%s]\n", i+1, candidate.FilePath, candidate.LineNumber, strings.ToUpper(candidate.Severity), strings.ToUpper(candidate.Confidence)))
		sb.WriteString(fmt.Sprintf("Summary: %s\n", candidate.Summary))
		sb.WriteString(fmt.Sprintf("Failure mode: %s\n", candidate.FailureMode))
		sb.WriteString(fmt.Sprintf("Production impact: %s\n", candidate.ProductionImpact))
		if candidate.NeedsVerification {
			sb.WriteString("Needs verification: true\n")
		}
		sb.WriteByte('\n')
	}

	sb.WriteString("## Changed File Context\n")
	sb.WriteString(bundle.Render())
	sb.WriteString("\n\n## Verification Evidence\n")
	if strings.TrimSpace(verificationBundle) == "" {
		sb.WriteString("(no additional verification evidence requested)\n")
	} else {
		sb.WriteString(verificationBundle)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func parseExtractionOutput(raw string) (*extractionOutput, error) {
	jsonBlob, err := extractJSONForKey(raw, "candidates")
	if err != nil {
		return nil, err
	}
	var output extractionOutput
	if err := json.Unmarshal([]byte(jsonBlob), &output); err != nil {
		return nil, err
	}
	if output.Candidates == nil {
		return nil, fmt.Errorf("no candidates key")
	}
	return &output, nil
}

func extractJSONForKey(s, key string) (string, error) {
	s = strings.TrimSpace(s)
	for start := 0; start < len(s); start++ {
		if s[start] != '{' {
			continue
		}

		end, ok := scanJSONObjectEnd(s, start)
		if !ok {
			continue
		}

		candidate := s[start:end]
		if jsonObjectHasKey(candidate, key) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no JSON object containing %s key found", key)
}

func scanJSONObjectEnd(s string, start int) (int, bool) {
	if start < 0 || start >= len(s) || s[start] != '{' {
		return 0, false
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
		return 0, false
	}

	return end, true
}

func jsonObjectHasKey(raw, key string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return false
	}
	_, ok := obj[key]
	return ok
}

func filterExtractedCandidates(
	candidates []extractedCandidate,
	diffFiles []domain.DiffFile,
	existing []domain.ExistingComment,
) []extractedCandidate {
	addedLinesMap := make(map[string]map[int]bool, len(diffFiles))
	for _, f := range diffFiles {
		lines := make(map[int]bool, len(f.AddedLines))
		for _, line := range f.AddedLines {
			lines[line] = true
		}
		addedLinesMap[f.Path] = lines
	}

	existingSet := make(map[string]bool, len(existing))
	for _, c := range existing {
		existingSet[fmt.Sprintf("%s:%d", c.FilePath, c.LineNumber)] = true
	}

	seen := make(map[string]bool)
	filtered := make([]extractedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.Category = strings.ToLower(strings.TrimSpace(candidate.Category))
		candidate.Severity = strings.ToLower(strings.TrimSpace(candidate.Severity))
		candidate.Confidence = strings.ToUpper(strings.TrimSpace(candidate.Confidence))
		candidate.Summary = strings.TrimSpace(candidate.Summary)
		candidate.FailureMode = strings.TrimSpace(candidate.FailureMode)
		candidate.ProductionImpact = strings.TrimSpace(candidate.ProductionImpact)

		if !isAllowedFinalCategory(candidate.Category) {
			continue
		}
		if candidate.Confidence == "" {
			candidate.Confidence = "MEDIUM"
		}
		if candidate.Confidence == "LOW" {
			continue
		}
		if candidate.LineNumber <= 0 || candidate.FilePath == "" {
			continue
		}
		if candidate.Summary == "" || candidate.FailureMode == "" || candidate.ProductionImpact == "" {
			continue
		}
		if lines, ok := addedLinesMap[candidate.FilePath]; ok && len(lines) > 0 && !lines[candidate.LineNumber] {
			continue
		}
		if existingSet[fmt.Sprintf("%s:%d", candidate.FilePath, candidate.LineNumber)] {
			continue
		}
		dedupKey := fmt.Sprintf("%s:%d:%s", candidate.FilePath, candidate.LineNumber, candidate.Category)
		if seen[dedupKey] {
			continue
		}
		seen[dedupKey] = true
		filtered = append(filtered, sanitizeVerificationRequests(candidate))
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if severityRank(filtered[i].Severity) != severityRank(filtered[j].Severity) {
			return severityRank(filtered[i].Severity) > severityRank(filtered[j].Severity)
		}
		return confidenceRank(filtered[i].Confidence) > confidenceRank(filtered[j].Confidence)
	})

	if len(filtered) > maxExtractedCandidates {
		filtered = filtered[:maxExtractedCandidates]
	}
	return filtered
}

func sanitizeVerificationRequests(candidate extractedCandidate) extractedCandidate {
	candidate.Verification.Paths = uniqueTrimmed(candidate.Verification.Paths, maxVerificationPaths)
	candidate.Verification.Symbols = uniqueTrimmed(candidate.Verification.Symbols, maxVerificationSymbols)
	candidate.Verification.Patterns = uniqueTrimmed(candidate.Verification.Patterns, maxVerificationPatterns)

	if len(candidate.Verification.Paths) == 0 && len(candidate.Verification.Symbols) == 0 && len(candidate.Verification.Patterns) == 0 {
		candidate.NeedsVerification = false
	}
	return candidate
}

func uniqueTrimmed(values []string, limit int) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func confidenceRank(confidence string) int {
	switch confidence {
	case "HIGH":
		return 3
	case "MEDIUM":
		return 2
	case "LOW":
		return 1
	default:
		return 0
	}
}

func buildVerificationBundle(
	ctx context.Context,
	registry *tools.Registry,
	language string,
	candidates []extractedCandidate,
) string {
	if registry == nil || len(candidates) == 0 {
		return ""
	}

	var sb strings.Builder
	paths := gatherVerificationPaths(candidates)
	symbols := gatherVerificationSymbols(candidates)
	patterns := gatherVerificationPatterns(candidates)

	if len(paths) > 0 {
		for start := 0; start < len(paths); start += 8 {
			end := min(len(paths), start+8)
			result, err := registry.Execute(ctx, "read_multi_file", domain.ToolInput{"paths": stringSliceToAny(paths[start:end])})
			if err == nil && result != nil {
				sb.WriteString("### Verification Files\n")
				sb.WriteString(strings.TrimSpace(result.Content))
				sb.WriteString("\n\n")
			}
		}
	}

	for _, symbol := range symbols {
		result, err := registry.Execute(ctx, "get_symbol_definition", domain.ToolInput{
			"symbol":   symbol,
			"language": language,
		})
		if err == nil && result != nil {
			sb.WriteString(fmt.Sprintf("### Symbol: %s\n", symbol))
			sb.WriteString(strings.TrimSpace(result.Content))
			sb.WriteString("\n\n")
		}
	}

	for _, pattern := range patterns {
		result, err := registry.Execute(ctx, "search_code", domain.ToolInput{"pattern": pattern})
		if err == nil && result != nil {
			sb.WriteString(fmt.Sprintf("### Search: %s\n", pattern))
			sb.WriteString(strings.TrimSpace(result.Content))
			sb.WriteString("\n\n")
		}
	}

	return strings.TrimSpace(sb.String())
}

func gatherVerificationPaths(candidates []extractedCandidate) []string {
	var paths []string
	for _, candidate := range candidates {
		if !candidate.NeedsVerification {
			continue
		}
		paths = append(paths, candidate.Verification.Paths...)
	}
	return uniqueTrimmed(paths, maxVerificationPaths)
}

func gatherVerificationSymbols(candidates []extractedCandidate) []string {
	var symbols []string
	for _, candidate := range candidates {
		if !candidate.NeedsVerification {
			continue
		}
		symbols = append(symbols, candidate.Verification.Symbols...)
	}
	return uniqueTrimmed(symbols, maxVerificationSymbols)
}

func gatherVerificationPatterns(candidates []extractedCandidate) []string {
	var patterns []string
	for _, candidate := range candidates {
		if !candidate.NeedsVerification {
			continue
		}
		patterns = append(patterns, candidate.Verification.Patterns...)
	}
	return uniqueTrimmed(patterns, maxVerificationPatterns)
}

func stringSliceToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
