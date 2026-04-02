package prompt

import (
	"fmt"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

const (
	maxStructuredFeedbacks = 8
	maxStructuredComments  = 20
)

func StructuredExtractionSystemPrompt(reviewCtx *domain.ReviewContext) string {
	var sb strings.Builder

	sb.WriteString("You are extracting candidate code-review findings from a merge request.\n\n")
	sb.WriteString("Goal:\n")
	sb.WriteString("- Keep only production-impact candidates from the provided changed-file bundle.\n")
	sb.WriteString("- Prefer false negatives over false positives.\n\n")
	sb.WriteString("Hard rules:\n")
	sb.WriteString("- Allowed categories: security, bug, logic, performance.\n")
	sb.WriteString("- Do not emit naming, style, refactor consistency, migration hygiene, or documentation comments unless they clearly break production behavior.\n")
	sb.WriteString("- Do not ask the developer to check, verify, confirm, or review something.\n")
	sb.WriteString("- Every candidate must include an exact failure mode and a concrete production impact.\n")
	sb.WriteString("- If the claim depends on code not shown here, keep it only as needsVerification=true and request exact paths, symbols, or patterns.\n")
	sb.WriteString("- If you cannot explain the failure mode, drop the candidate.\n")
	sb.WriteString("- Only analyze changed code shown in the bundle.\n\n")

	sb.WriteString(StructuredPromptContext(reviewCtx, false))
	sb.WriteString("Output exactly one JSON object:\n")
	sb.WriteString("{\"candidates\":[{\"filePath\":\"path/to/file.go\",\"lineNumber\":42,\"summary\":\"short bug statement\",\"severity\":\"high\",\"confidence\":\"HIGH\",\"category\":\"logic\",\"failureMode\":\"what breaks and under which condition\",\"productionImpact\":\"user-visible or production impact\",\"needsVerification\":true,\"verification\":{\"paths\":[\"path/to/dep.go\"],\"symbols\":[\"SomeSymbol\"],\"patterns\":[\"SomePattern\"]}}]}\n")
	sb.WriteString("If there are no production-impact candidates, output {\"candidates\":[]}.\n")
	return sb.String()
}

func StructuredVerificationSystemPrompt(reviewCtx *domain.ReviewContext, lang ResponseLanguage) string {
	var sb strings.Builder

	sb.WriteString("You are the final verification gate for AI code-review findings.\n\n")
	sb.WriteString("Goal:\n")
	sb.WriteString("- Keep only findings that remain supported after verification evidence.\n")
	sb.WriteString("- Reject speculative, non-production, or non-actionable comments.\n\n")
	sb.WriteString("Hard rules:\n")
	sb.WriteString("- Allowed categories: security, bug, logic, performance.\n")
	sb.WriteString("- Reject naming, style, refactor-only, migration hygiene, and \"please check\" comments.\n")
	sb.WriteString("- Reject any candidate that lacks a concrete failure mode or production impact.\n")
	sb.WriteString("- Reject any candidate that asks the developer to verify something instead of asserting a supported issue.\n")
	sb.WriteString("- If evidence is inconclusive, drop the candidate.\n")
	sb.WriteString("- Only include issues anchored to changed lines in changed files.\n\n")

	sb.WriteString(StructuredPromptContext(reviewCtx, true))
	sb.WriteString(StrictReviewOutputFormat(lang))
	return sb.String()
}

func StructuredPromptContext(reviewCtx *domain.ReviewContext, includeLanguageGuidance bool) string {
	if reviewCtx == nil {
		return ""
	}

	var sb strings.Builder

	if reviewCtx.CustomPrompt != nil && strings.TrimSpace(*reviewCtx.CustomPrompt) != "" {
		sb.WriteString("Project-specific instructions:\n")
		sb.WriteString(strings.TrimSpace(*reviewCtx.CustomPrompt))
		sb.WriteString("\n\n")
	}

	if includeLanguageGuidance && strings.TrimSpace(reviewCtx.LanguageGuidelines) != "" {
		sb.WriteString("Language guidelines:\n")
		sb.WriteString(strings.TrimSpace(reviewCtx.LanguageGuidelines))
		sb.WriteString("\n\n")
	}

	if len(reviewCtx.RecentFeedbacks) > 0 {
		sb.WriteString("Recent developer feedback:\n")
		limit := min(len(reviewCtx.RecentFeedbacks), maxStructuredFeedbacks)
		for i := 0; i < limit; i++ {
			fb := reviewCtx.RecentFeedbacks[i]
			signal := "accepted"
			if fb.Signal == domain.FeedbackSignalRejected {
				signal = "rejected"
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s (%s)\n", signal, fb.CommentSummary, fb.Category))
		}
		sb.WriteByte('\n')
	}

	if len(reviewCtx.ExistingUnresolvedComments) > 0 {
		sb.WriteString("Already flagged unresolved comments (do not duplicate):\n")
		limit := min(len(reviewCtx.ExistingUnresolvedComments), maxStructuredComments)
		for i := 0; i < limit; i++ {
			c := reviewCtx.ExistingUnresolvedComments[i]
			sb.WriteString(fmt.Sprintf("- %s:%d %s\n", c.FilePath, c.LineNumber, c.Summary))
		}
		sb.WriteByte('\n')
	}

	if reviewCtx.MissingIntent {
		sb.WriteString("The MR description is empty. Do not broaden scope; stay anchored to the provided code.\n\n")
	}

	return sb.String()
}

func StrictReviewOutputFormat(lang ResponseLanguage) string {
	return fmt.Sprintf(`Output exactly:
=== FINAL REVIEW ===
{"reviews":[{"filePath":"path/to/file.go","lineNumber":42,"reviewComment":"Confirmed production-impact issue.","severity":"high","confidence":"HIGH","category":"logic","suggestion":"// optional concrete fix"}]}

Field rules:
- reviewComment must be in %s and must assert a supported issue, not ask the developer to check anything
- Allowed severity: critical, high, medium, low
- Prefer medium or higher; use low only when the issue still has real production impact
- Allowed category: security, bug, logic, performance
- suggestion is optional and should be concrete when provided
- If no candidates survive verification, output {"reviews":[]}
`, lang.Name())
}
