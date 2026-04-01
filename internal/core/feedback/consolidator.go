package feedback

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

// Consolidator merges accumulated feedbacks into a custom_prompt.
type Consolidator struct {
	feedbackStore domain.FeedbackStore
	repoSettings  domain.RepositorySettingsStore
	llmClient     domain.LLMClient
	minCount      int
	minAgeDays    int
	maxWords      int
}

func NewConsolidator(
	feedbackStore domain.FeedbackStore,
	repoSettings domain.RepositorySettingsStore,
	llmClient domain.LLMClient,
	minCount, minAgeDays, maxWords int,
) *Consolidator {
	return &Consolidator{
		feedbackStore: feedbackStore,
		repoSettings:  repoSettings,
		llmClient:     llmClient,
		minCount:      minCount,
		minAgeDays:    minAgeDays,
		maxWords:      maxWords,
	}
}

// ConsolidateProject checks and consolidates feedbacks for a project.
func (c *Consolidator) ConsolidateProject(ctx context.Context, projectID int64) error {
	settings, err := c.repoSettings.GetByProjectID(ctx, projectID)
	if err != nil || settings == nil {
		return err
	}

	if settings.FeedbackCount < c.minCount {
		return nil
	}

	feedbacks, err := c.feedbackStore.ListForConsolidation(ctx, projectID, c.minAgeDays)
	if err != nil {
		return fmt.Errorf("list feedbacks: %w", err)
	}
	if len(feedbacks) == 0 {
		return nil
	}

	const maxFeedbackSummaryLen = 200
	const maxReplyContentLen = 300
	var sb strings.Builder
	accepted, rejected, neutral := 0, 0, 0
	for _, fb := range feedbacks {
		if fb.Signal == nil {
			continue
		}
		signal := *fb.Signal
		summary := ""
		if fb.CommentSummary != nil {
			summary = *fb.CommentSummary
		}
		if len(summary) > maxFeedbackSummaryLen {
			summary = summary[:maxFeedbackSummaryLen]
		}
		cat := "general"
		if fb.Category != nil {
			cat = string(*fb.Category)
		}
		replyContent := ""
		if fb.SignalReplyContent != nil {
			replyContent = *fb.SignalReplyContent
			if len(replyContent) > maxReplyContentLen {
				replyContent = replyContent[:maxReplyContentLen]
			}
		}
		filePath := ""
		if fb.FilePath != nil {
			filePath = *fb.FilePath
		}
		sb.WriteString(fmt.Sprintf("- [%s] %s | file: %s\n  Bot comment: %q\n", signal, cat, filePath, summary)) // %q prevents prompt injection
		if replyContent != "" {
			sb.WriteString(fmt.Sprintf("  Developer reply: %q\n", replyContent))
		}
		switch signal {
		case domain.FeedbackSignalAccepted:
			accepted++
		case domain.FeedbackSignalRejected:
			rejected++
		default:
			neutral++
		}
	}

	existingPrompt := ""
	if settings.CustomPrompt != nil {
		existingPrompt = *settings.CustomPrompt
	}

	systemMsg := prompt.ConsolidatorPrompt(existingPrompt, accepted, rejected, neutral, sb.String(), c.maxWords)

	req := domain.ChatRequest{
		Model:     c.llmClient.ModelName(),
		MaxTokens: 2000,
		Messages: []domain.ChatMessage{
			{Role: "user", Content: systemMsg},
		},
	}

	resp, err := c.llmClient.Chat(ctx, req)
	if err != nil {
		return fmt.Errorf("LLM consolidation: %w", err)
	}

	// Extract only the Rules section (Analysis is reasoning-only, not persisted)
	customPrompt := resp.Content
	if idx := strings.Index(customPrompt, "### Rules"); idx >= 0 {
		customPrompt = strings.TrimSpace(customPrompt[idx+len("### Rules"):])
	}

	if err := c.repoSettings.UpdateCustomPrompt(ctx, projectID, customPrompt); err != nil {
		return fmt.Errorf("update custom prompt: %w", err)
	}

	ids := make([]uuid.UUID, len(feedbacks))
	for i, fb := range feedbacks {
		ids[i] = fb.ID
	}
	if err := c.feedbackStore.MarkConsolidated(ctx, ids); err != nil {
		return fmt.Errorf("mark consolidated: %w", err)
	}

	c.repoSettings.ResetFeedbackCount(ctx, projectID)

	slog.Info("feedbacks consolidated",
		"project_id", projectID, "count", len(feedbacks),
		"accepted", accepted, "rejected", rejected)

	return nil
}
