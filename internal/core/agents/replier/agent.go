package replier

import (
	"context"
	"fmt"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

const (
	replyMaxTokens   = 400
	replyTemperature = 0.3
)

type Agent struct {
}

func NewAgent() *Agent {
	return &Agent{}
}

type ReplyInput struct {
	Job              *domain.ReplyJob
	MR               *domain.GitLabMR
	ThreadHistory    string
	CodeContext      string
	LatestCodeContext string
	CustomPrompt     *string
	Intent           domain.ReplyIntent
	DetectedLang     string
	ResponseLanguage prompt.ResponseLanguage
}

// GenerateReply makes a single LLM call to generate a reply.
func (a *Agent) GenerateReply(ctx context.Context, llmClient domain.LLMClient, input ReplyInput) (string, error) {
	systemPrompt := a.buildSystemPrompt(input)
	userMessage := a.buildUserMessage(input)

	req := domain.ChatRequest{
		Model:       llmClient.ModelName(),
		MaxTokens:   replyMaxTokens,
		Temperature: replyTemperature,
		Messages: []domain.ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
	}

	resp, err := llmClient.Chat(ctx, req)
	if err != nil {
		return "", fmt.Errorf("reply LLM call: %w", err)
	}

	return resp.Content, nil
}

func (a *Agent) buildSystemPrompt(input ReplyInput) string {
	base := prompt.ReplierSystemPrompt(input.ResponseLanguage)
	if input.CustomPrompt != nil && *input.CustomPrompt != "" {
		base += "\n## Project-Specific Instructions\n" + *input.CustomPrompt + "\n"
	}
	return base
}

func (a *Agent) buildUserMessage(input ReplyInput) string {
	var sb strings.Builder

	if input.MR != nil {
		sb.WriteString(fmt.Sprintf("## MR Context\nTitle: %s\n", input.MR.Title))
		desc := input.MR.Description
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
		if desc != "" {
			sb.WriteString(fmt.Sprintf("Description: %s\n", desc))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Original Review Comment\n")
	if input.Job.BotCommentFilePath != nil {
		sb.WriteString(fmt.Sprintf("File: %s", *input.Job.BotCommentFilePath))
		if input.Job.BotCommentLine != nil {
			sb.WriteString(fmt.Sprintf(":%d", *input.Job.BotCommentLine))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("Comment: %s\n\n", input.Job.BotCommentContent))

	if input.CodeContext != "" {
		sb.WriteString("## Relevant Code (at review time)\n```\n")
		sb.WriteString(input.CodeContext)
		sb.WriteString("\n```\n\n")
	}

	if input.LatestCodeContext != "" && input.LatestCodeContext != input.CodeContext {
		sb.WriteString("## Latest Code (HEAD) — use this to verify if the issue was actually fixed\n```\n")
		sb.WriteString(input.LatestCodeContext)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("## Discussion Thread\n")
	sb.WriteString(input.ThreadHistory)
	sb.WriteString("\n\n")

	sb.WriteString(fmt.Sprintf("## Developer's Reply (respond to this)\n%s\n\n", input.Job.TriggerNoteContent))
	sb.WriteString(fmt.Sprintf("## Detected Intent\nThe developer appears to be: %s\n", input.Intent))

	return sb.String()
}
