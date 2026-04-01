package reply

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/core/agents/replier"
	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/pkg/llm"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

type Pipeline struct {
	cfg           config.Config
	replyJobStore domain.ReplyJobStore
	repoSettings  domain.RepositorySettingsStore
	feedbackStore domain.FeedbackStore
	gitlabClient  domain.GitLabClient
	replyAgent    *replier.Agent
	reposDir      string
}

type PipelineDeps struct {
	Config        config.Config
	ReplyJobStore domain.ReplyJobStore
	RepoSettings  domain.RepositorySettingsStore
	FeedbackStore domain.FeedbackStore
	GitLabClient  domain.GitLabClient
	ReplyAgent    *replier.Agent
	ReposDir      string
}

func NewPipeline(deps PipelineDeps) *Pipeline {
	return &Pipeline{
		cfg:           deps.Config,
		replyJobStore: deps.ReplyJobStore,
		repoSettings:  deps.RepoSettings,
		feedbackStore: deps.FeedbackStore,
		gitlabClient:  deps.GitLabClient,
		replyAgent:    deps.ReplyAgent,
		reposDir:      deps.ReposDir,
	}
}

func (p *Pipeline) Execute(ctx context.Context, job *domain.ReplyJob) error {
	log := slog.With("job_id", job.ID.String(), "discussion_id", job.DiscussionID)

	p.replyJobStore.UpdateStatus(ctx, job.ID, domain.ReplyJobStatusProcessing, nil)

	discussion, err := p.gitlabClient.GetDiscussion(ctx, job.GitLabProjectID, job.MrIID, job.DiscussionID)
	if err != nil || discussion == nil {
		return p.failJob(ctx, job, "load discussion: "+fmt.Sprint(err))
	}

	threadHistory := formatThreadHistory(discussion.Notes)

	repoPath := filepath.Join(p.reposDir, fmt.Sprintf("%d", job.GitLabProjectID))
	var codeContext string
	if job.BotCommentFilePath != nil && job.BotCommentLine != nil {
		codeContext = readFileLines(repoPath, *job.BotCommentFilePath, *job.BotCommentLine)
	}

	mr, _ := p.gitlabClient.GetMR(ctx, job.GitLabProjectID, job.MrIID)
	settings, _ := p.repoSettings.GetByProjectID(ctx, job.GitLabProjectID)

	var customPrompt *string
	if settings != nil {
		customPrompt = settings.CustomPrompt
	}

	intent := ClassifyIntent(job.TriggerNoteContent)
	signal := IntentToSignal(intent)

	// For "fixed/done" claims, read latest code from disk to let LLM verify the fix
	var latestCodeContext string
	if (intent == domain.IntentAgree || intent == domain.IntentAcknowledge) &&
		job.BotCommentFilePath != nil && job.BotCommentLine != nil {
		latestCodeContext = readFileLines(repoPath, *job.BotCommentFilePath, *job.BotCommentLine)
	}

	var modelOverride *string
	if settings != nil {
		modelOverride = settings.ModelOverride
	}
	llmClient, err := llm.NewBalancedClientFromConfig(p.cfg.LLM, modelOverride)
	if err != nil {
		return p.failJob(ctx, job, "create LLM client: "+err.Error())
	}

	replyInput := replier.ReplyInput{
		Job:              job,
		MR:               mr,
		ThreadHistory:    threadHistory,
		CodeContext:      codeContext,
		LatestCodeContext: latestCodeContext,
		CustomPrompt:     customPrompt,
		Intent:           intent,
		ResponseLanguage: prompt.ParseLanguage(p.cfg.Review.ResponseLanguage),
	}

	replyText, err := p.replyAgent.GenerateReply(ctx, llmClient, replyInput)
	if err != nil {
		return p.failJob(ctx, job, "generate reply: "+err.Error())
	}

	_, err = p.gitlabClient.PostReply(ctx, job.GitLabProjectID, job.MrIID, job.DiscussionID, replyText)
	if err != nil {
		return p.failJob(ctx, job, "post reply: "+err.Error())
	}

	p.feedbackStore.UpdateSignal(ctx, job.BotCommentID, signal, job.TriggerNoteContent)

	if signal == domain.FeedbackSignalAccepted || signal == domain.FeedbackSignalRejected {
		p.repoSettings.IncrementFeedbackCount(ctx, job.GitLabProjectID, 1)
	}

	// Auto-resolve on reject/acknowledge only; agree/fixed waits for LLM verification
	if intent == domain.IntentReject || intent == domain.IntentAcknowledge {
		if err := p.gitlabClient.ResolveDiscussion(ctx, job.GitLabProjectID, job.MrIID, job.DiscussionID); err != nil {
			log.Warn("failed to auto-resolve discussion", "error", err)
		} else {
			log.Info("auto-resolved discussion", "intent", intent)
		}
	}

	p.replyJobStore.UpdateCompleted(ctx, job.ID, replyText, intent, signal)

	log.Info("reply posted", "intent", intent, "signal", signal)
	return nil
}

func (p *Pipeline) failJob(ctx context.Context, job *domain.ReplyJob, msg string) error {
	slog.Error("reply job failed", "job_id", job.ID.String(), "error", msg)
	p.replyJobStore.UpdateStatus(ctx, job.ID, domain.ReplyJobStatusFailed, &msg)
	return errors.New(msg)
}

func formatThreadHistory(notes []domain.GitLabNote) string {
	var sb strings.Builder
	for _, n := range notes {
		role := n.AuthorName
		sb.WriteString(fmt.Sprintf("[%s] %s\n\n", role, n.Body))
	}
	return sb.String()
}

func readFileLines(repoPath, filePath string, centerLine int) string {
	absPath := filepath.Join(repoPath, filePath)
	file, err := os.Open(absPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	startLine := centerLine - 20
	if startLine < 1 {
		startLine = 1
	}
	endLine := centerLine + 20

	var lines []string
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if lineNum > endLine {
			break
		}
		lines = append(lines, fmt.Sprintf("%d: %s", lineNum, scanner.Text()))
	}
	return strings.Join(lines, "\n")
}
