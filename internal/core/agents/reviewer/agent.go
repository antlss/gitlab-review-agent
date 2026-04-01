package reviewer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/pkg/tools"
	"github.com/antlss/gitlab-review-agent/internal/shared"
)

const (
	agentMaxTokens     = 4096
	compressKeepRecent = 4
)

type Agent struct {
}

func NewAgent() *Agent {
	return &Agent{}
}

type AgentInput struct {
	Job                  *shared.ReviewJob
	ReviewCtx            *shared.ReviewContext
	FilteredFiles        []shared.DiffFile
	DiffStatFormatted    string
	MaxIterations        int
	SoftWarnAt           int
	CompressionThreshold float64
	Registry             *tools.Registry
	LLMClient            shared.LLMClient
	PreloadedDiffs       string
	AllDiffsPreloaded    bool
	ResponseLanguage     prompt.ResponseLanguage
}

type AgentResult struct {
	RawOutput       string
	IterationsUsed  int
	TokensEstimated int
	StopReason      string
}

func (a *Agent) Run(ctx context.Context, input AgentInput) (*AgentResult, error) {
	systemPrompt := a.buildSystemPrompt(input)
	userMessage := a.buildUserMessage(input)

	messages := []shared.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	}

	billedTokens := 0
	lastInputTokens := 0
	contextWindowSize := input.LLMClient.ContextWindowSize()

	for iteration := 0; iteration < input.MaxIterations; iteration++ {
		slog.Info("agent iteration started", "iteration", iteration+1, "max_iterations", input.MaxIterations, "messages_count", len(messages))

		if iteration == input.SoftWarnAt {
			messages = append(messages, shared.ChatMessage{
				Role:    "user",
				Content: prompt.BudgetWarning(iteration, input.MaxIterations),
			})
		}

		req := shared.ChatRequest{
			Model:     input.LLMClient.ModelName(),
			Messages:  messages,
			Tools:     input.Registry.Definitions(),
			MaxTokens: agentMaxTokens,
		}

		resp, err := input.LLMClient.Chat(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("LLM call failed at iteration %d: %w", iteration, err)
		}

		slog.Info("agent LLM responded", "iteration", iteration+1, "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens, "tool_calls", len(resp.ToolCalls), "stop_reason", resp.StopReason)

		billedTokens += resp.Usage.InputTokens + resp.Usage.OutputTokens
		lastInputTokens = resp.Usage.InputTokens

		if strings.Contains(resp.Content, "=== FINAL REVIEW ===") {
			raw := strings.SplitN(resp.Content, "=== FINAL REVIEW ===", 2)[1]
			return &AgentResult{
				RawOutput:       strings.TrimSpace(raw),
				IterationsUsed:  iteration + 1,
				TokensEstimated: billedTokens,
				StopReason:      "final_review_signal",
			}, nil
		}

		if len(resp.ToolCalls) > 0 {
			messages = append(messages, shared.ChatMessage{
				Role:      "assistant",
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			})

			type execResult struct {
				callID  string
				content string
			}
			results := make([]execResult, len(resp.ToolCalls))
			var wg sync.WaitGroup
			for i, tc := range resp.ToolCalls {
				wg.Add(1)
				go func(i int, tc shared.ToolCall) {
					defer wg.Done()
					var toolInput shared.ToolInput
					if err := json.Unmarshal([]byte(tc.InputJSON), &toolInput); err != nil {
						slog.Warn("failed to unmarshal tool input",
							"tool", tc.Name, "error", err)
					}

					slog.Debug("agent executing tool", "tool", tc.Name, "input", tc.InputJSON)

					result, err := input.Registry.Execute(ctx, tc.Name, toolInput)
					var content string
					if err != nil {
						content = "ERROR: " + err.Error()
					} else if result.Error != nil {
						content = "ERROR: " + *result.Error
					} else {
						content = result.Content
					}
					results[i] = execResult{callID: tc.ID, content: content}
				}(i, tc)
			}
			wg.Wait()

			for _, r := range results {
				messages = append(messages, shared.ChatMessage{
					Role:       "tool",
					Content:    r.content,
					ToolCallID: r.callID,
				})
			}
			if float64(lastInputTokens)/float64(contextWindowSize) > input.CompressionThreshold {
				messages = a.compressContext(messages, input.Registry.Notes)
			}

			slog.Debug("tool execution completed", "iteration", iteration+1, "results_count", len(results))
			continue
		}

		if resp.Content != "" {
			messages = append(messages, shared.ChatMessage{
				Role:    "assistant",
				Content: resp.Content,
			})
		}
		messages = append(messages, shared.ChatMessage{
			Role:    "user",
			Content: prompt.AgentNudge,
		})
	}

	messages = append(messages, shared.ChatMessage{
		Role:    "user",
		Content: prompt.BudgetExhausted(input.MaxIterations),
	})

	req := shared.ChatRequest{
		Model:     input.LLMClient.ModelName(),
		Messages:  messages,
		MaxTokens: agentMaxTokens,
	}
	resp, err := input.LLMClient.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("force output LLM call failed: %w", err)
	}

	raw := resp.Content
	if idx := strings.Index(raw, "=== FINAL REVIEW ==="); idx >= 0 {
		raw = strings.TrimSpace(raw[idx+len("=== FINAL REVIEW ==="):])
	}

	return &AgentResult{
		RawOutput:       raw,
		IterationsUsed:  input.MaxIterations,
		TokensEstimated: billedTokens + resp.Usage.OutputTokens,
		StopReason:      "budget_exhausted",
	}, nil
}

func (a *Agent) buildSystemPrompt(input AgentInput) string {
	var sb strings.Builder

	sb.WriteString(prompt.ReviewerCoreRules)
	sb.WriteString(prompt.ReviewerLanguageRule(input.ResponseLanguage))

	if input.AllDiffsPreloaded {
		sb.WriteString(prompt.ReviewerToolsAllPreloaded)
	} else if input.PreloadedDiffs != "" {
		sb.WriteString(prompt.ReviewerToolsSomePreloaded)
	} else {
		sb.WriteString(prompt.ReviewerToolsNone)
	}

	sb.WriteString(prompt.ReviewerEfficiency)

	if input.ReviewCtx.CustomPrompt != nil && *input.ReviewCtx.CustomPrompt != "" {
		sb.WriteString("## Project-Specific Instructions\n")
		sb.WriteString(*input.ReviewCtx.CustomPrompt)
		sb.WriteString("\n\n")
	}

	if input.ReviewCtx.LanguageGuidelines != "" {
		sb.WriteString("## Language Guidelines\n")
		sb.WriteString(input.ReviewCtx.LanguageGuidelines)
		sb.WriteString("\n")
	}

	if len(input.ReviewCtx.RecentFeedbacks) > 0 {
		sb.WriteString("## Learn from Past Feedback\n")
		for _, fb := range input.ReviewCtx.RecentFeedbacks {
			signal := "accepted"
			if fb.Signal == shared.FeedbackSignalRejected {
				signal = "rejected"
			}
			sb.WriteString(fmt.Sprintf("- [%s] %s (%s)\n", signal, fb.CommentSummary, fb.Category))
		}
		sb.WriteString("\n")
	}

	if len(input.ReviewCtx.ExistingUnresolvedComments) > 0 {
		sb.WriteString("## Already Flagged (do NOT duplicate)\n")
		for _, c := range input.ReviewCtx.ExistingUnresolvedComments {
			sb.WriteString(fmt.Sprintf("- %s:%d — %s\n", c.FilePath, c.LineNumber, c.Summary))
		}
		sb.WriteString("\n")
	}

	if input.ReviewCtx.MissingIntent {
		sb.WriteString("NOTE: This MR has no description. Review more carefully as intent is unknown.\n\n")
	}

	sb.WriteString(prompt.ReviewerOutputFormat(input.ResponseLanguage))

	return sb.String()
}

func (a *Agent) buildUserMessage(input AgentInput) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## MR: %s\n\n", input.ReviewCtx.MRTitle))

	if input.ReviewCtx.MRDescription != "" {
		sb.WriteString("**Description:**\n")
		desc := input.ReviewCtx.MRDescription
		if len(desc) > 500 {
			desc = desc[:500] + "..."
		}
		sb.WriteString(desc)
		sb.WriteString("\n\n")
	}

	sb.WriteString("## Changes (Risk-Ranked)\n")
	sb.WriteString(input.DiffStatFormatted)
	sb.WriteString("\n")

	if input.PreloadedDiffs != "" {
		if input.AllDiffsPreloaded {
			sb.WriteString(prompt.ReviewerAllDiffsHeader)
			sb.WriteString(input.PreloadedDiffs)
			sb.WriteString(prompt.ReviewerAllDiffsFooter)
			sb.WriteString(prompt.ReviewerAllDiffsInstruction)
		} else {
			sb.WriteString(prompt.ReviewerHighRiskDiffsHeader)
			sb.WriteString(input.PreloadedDiffs)
			sb.WriteString(prompt.ReviewerHighRiskDiffsFooter)
			sb.WriteString(prompt.ReviewerHighRiskDiffsInstruction)
		}
	} else {
		sb.WriteString(prompt.ReviewerNoDiffsInstruction)
	}

	return sb.String()
}

func (a *Agent) compressContext(messages []shared.ChatMessage, notes *tools.NoteAccumulator) []shared.ChatMessage {
	// Separate system message and conversation
	var systemMsg shared.ChatMessage
	var conversation []shared.ChatMessage
	for _, m := range messages {
		if m.Role == "system" {
			systemMsg = m
		} else {
			conversation = append(conversation, m)
		}
	}

	// conversation[0] is the first user message containing preloaded diffs and MR
	// context — it must never be dropped since the agent needs it to stay anchored
	// to the actual diff content. We always preserve it and keep (compressKeepRecent-1)
	// of the most recent messages alongside it.
	if len(conversation) == 0 {
		return messages
	}

	firstUserMsg := conversation[0]
	rest := conversation[1:]

	keepRecent := compressKeepRecent - 1 // one slot is used by firstUserMsg
	if len(rest) <= keepRecent {
		// Nothing to drop — conversation is short enough
		return messages
	}

	dropped := rest[:len(rest)-keepRecent]
	recent := rest[len(rest)-keepRecent:]

	seen := make(map[string]bool)
	var toolsUsed []string
	for _, m := range dropped {
		for _, tc := range m.ToolCalls {
			if !seen[tc.Name] {
				toolsUsed = append(toolsUsed, tc.Name)
				seen[tc.Name] = true
			}
		}
	}

	var summaryParts []string
	if len(toolsUsed) > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("[%d prior messages compacted. Tools already called: %s.]", len(dropped), strings.Join(toolsUsed, ", ")))
	} else {
		summaryParts = append(summaryParts, fmt.Sprintf("[%d prior messages compacted.]", len(dropped)))
	}

	var notesSummary string
	if notes != nil {
		notesSummary = notes.Summary()
	}
	if notesSummary != "" {
		summaryParts = append(summaryParts, "Saved notes (do NOT re-investigate these):\n"+notesSummary)
	}

	summaryParts = append(summaryParts, "Continue investigation. The original MR diffs remain in the first message above.")
	summary := strings.Join(summaryParts, "\n")

	// Layout: system → first-user (diffs) → compaction-summary → recent messages
	compressed := []shared.ChatMessage{
		systemMsg,
		firstUserMsg,
		{Role: "user", Content: summary},
	}
	compressed = append(compressed, recent...)

	slog.Info("context compressed",
		"old_len", len(messages),
		"new_len", len(compressed),
		"dropped", len(dropped),
		"tools_summarized", strings.Join(toolsUsed, ", "),
		"notes_saved", notesSummary != "",
	)
	return compressed
}
