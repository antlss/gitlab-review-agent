package reply

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/core/agents/replier"
	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/domain"
	"github.com/antlss/gitlab-review-agent/internal/pkg/llm"
)

type replyGenerator interface {
	GenerateReply(ctx context.Context, llmClient domain.LLMClient, input replier.ReplyInput) (string, error)
}

type repoSyncer interface {
	AcquireGitLock(ctx context.Context, projectID int64) error
	ReleaseGitLock(ctx context.Context, projectID int64)
	FetchAndCheckout(ctx context.Context, projectID int64, projectPath string, mrIID int64, targetBranch, headSHA string) error
	ReadFileAtSHA(ctx context.Context, projectID int64, sha, filePath string) ([]byte, error)
}

type Pipeline struct {
	cfg           config.Config
	replyJobStore domain.ReplyJobStore
	repoSettings  domain.RepositorySettingsStore
	feedbackStore domain.FeedbackStore
	gitlabClient  domain.GitLabClient
	replyAgent    replyGenerator
	repoManager   repoSyncer
}

type PipelineDeps struct {
	Config        config.Config
	ReplyJobStore domain.ReplyJobStore
	RepoSettings  domain.RepositorySettingsStore
	FeedbackStore domain.FeedbackStore
	GitLabClient  domain.GitLabClient
	ReplyAgent    replyGenerator
	RepoManager   repoSyncer
}

func NewPipeline(deps PipelineDeps) *Pipeline {
	return &Pipeline{
		cfg:           deps.Config,
		replyJobStore: deps.ReplyJobStore,
		repoSettings:  deps.RepoSettings,
		feedbackStore: deps.FeedbackStore,
		gitlabClient:  deps.GitLabClient,
		replyAgent:    deps.ReplyAgent,
		repoManager:   deps.RepoManager,
	}
}

func (p *Pipeline) Execute(ctx context.Context, job *domain.ReplyJob) error {
	log := slog.With("job_id", job.ID.String(), "discussion_id", job.DiscussionID)

	if err := p.replyJobStore.UpdateStatus(ctx, job.ID, domain.ReplyJobStatusProcessing, nil); err != nil {
		return fmt.Errorf("mark reply job processing: %w", err)
	}

	discussion, err := p.gitlabClient.GetDiscussion(ctx, job.GitLabProjectID, job.MrIID, job.DiscussionID)
	if err != nil || discussion == nil {
		return p.failJob(ctx, job, "load discussion: "+fmt.Sprint(err))
	}

	threadHistory := formatThreadHistory(discussion.Notes)
	intent := ClassifyIntent(job.TriggerNoteContent)
	signal := IntentToSignal(intent)

	mr, err := p.gitlabClient.GetMR(ctx, job.GitLabProjectID, job.MrIID)
	if err != nil {
		return p.failJob(ctx, job, "load MR: "+err.Error())
	}

	settings, err := p.repoSettings.GetByProjectID(ctx, job.GitLabProjectID)
	if err != nil {
		log.Warn("failed to load repo settings", "error", err)
	}

	var customPrompt *string
	if settings != nil {
		customPrompt = settings.CustomPrompt
	}

	var codeContext string
	var latestCodeContext string
	if job.BotCommentFilePath != nil && job.BotCommentLine != nil {
		if err := p.syncToLatestMRHead(ctx, job, mr, settings); err != nil {
			return p.failJob(ctx, job, "sync latest repository state: "+err.Error())
		}

		codeContext = p.readFileLinesAtSHA(ctx, job.GitLabProjectID, mr.HeadSHA, *job.BotCommentFilePath, *job.BotCommentLine)
		if intent == domain.IntentAgree || intent == domain.IntentAcknowledge {
			latestCodeContext = codeContext
		}
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
		Job:               job,
		MR:                mr,
		ThreadHistory:     threadHistory,
		CodeContext:       codeContext,
		LatestCodeContext: latestCodeContext,
		CustomPrompt:      customPrompt,
		Intent:            intent,
		ResponseLanguage:  prompt.ParseLanguage(p.cfg.Review.ResponseLanguage),
	}

	replyText, err := p.replyAgent.GenerateReply(ctx, llmClient, replyInput)
	if err != nil {
		return p.failJob(ctx, job, "generate reply: "+err.Error())
	}

	_, err = p.gitlabClient.PostReply(ctx, job.GitLabProjectID, job.MrIID, job.DiscussionID, replyText)
	if err != nil {
		return p.failJob(ctx, job, "post reply: "+err.Error())
	}

	feedback, err := p.feedbackStore.GetByNoteID(ctx, job.BotCommentID)
	if err != nil {
		log.Warn("failed to load feedback for thread state", "bot_comment_id", job.BotCommentID, "error", err)
	}
	beforeState, afterState := domain.ComputeThreadState(nil, intent)
	if feedback != nil {
		beforeState, afterState = domain.ComputeThreadState(feedback.ThreadState, intent)
	}

	if err := p.feedbackStore.UpdateSignal(ctx, job.BotCommentID, signal, job.TriggerNoteContent, afterState); err != nil {
		log.Warn("failed to update feedback signal", "bot_comment_id", job.BotCommentID, "error", err)
	}

	if signal == domain.FeedbackSignalAccepted || signal == domain.FeedbackSignalRejected {
		if err := p.repoSettings.IncrementFeedbackCount(ctx, job.GitLabProjectID, 1); err != nil {
			log.Warn("failed to increment feedback count", "error", err)
		}
	}

	if err := p.replyJobStore.UpdateCompleted(ctx, job.ID, replyText, intent, signal, beforeState, afterState); err != nil {
		log.Error("reply posted but failed to persist completion; manual reconciliation required",
			"error", err)
		return nil
	}

	job.ThreadStateBefore = domain.Ptr(beforeState)
	job.ThreadStateAfter = domain.Ptr(afterState)

	log.Info("reply posted", "intent", intent, "signal", signal)
	return nil
}

func (p *Pipeline) failJob(ctx context.Context, job *domain.ReplyJob, msg string) error {
	slog.Error("reply job failed", "job_id", job.ID.String(), "error", msg)
	if err := p.replyJobStore.UpdateStatus(ctx, job.ID, domain.ReplyJobStatusFailed, &msg); err != nil {
		slog.Error("failed to persist reply job failure", "job_id", job.ID.String(), "store_error", err)
	}
	return errors.New(msg)
}

func (p *Pipeline) syncToLatestMRHead(
	ctx context.Context,
	job *domain.ReplyJob,
	mr *domain.GitLabMR,
	settings *domain.RepositorySettings,
) error {
	if p.repoManager == nil {
		return fmt.Errorf("repo manager is not configured")
	}
	if mr == nil || mr.HeadSHA == "" {
		return fmt.Errorf("MR head SHA is unavailable")
	}

	projectPath := ""
	if settings != nil {
		projectPath = settings.ProjectPath
	}
	if projectPath == "" {
		project, err := p.gitlabClient.GetProject(ctx, job.GitLabProjectID)
		if err != nil {
			return fmt.Errorf("load project: %w", err)
		}
		projectPath = project.PathWithNS
	}
	if projectPath == "" {
		return fmt.Errorf("project path is unavailable")
	}

	if err := p.repoManager.AcquireGitLock(ctx, job.GitLabProjectID); err != nil {
		return err
	}
	defer p.repoManager.ReleaseGitLock(ctx, job.GitLabProjectID)

	if err := p.repoManager.FetchAndCheckout(ctx, job.GitLabProjectID, projectPath, job.MrIID, mr.TargetBranch, mr.HeadSHA); err != nil {
		return err
	}
	return nil
}

func (p *Pipeline) readFileLinesAtSHA(ctx context.Context, projectID int64, sha, filePath string, centerLine int) string {
	content, err := p.repoManager.ReadFileAtSHA(ctx, projectID, sha, filePath)
	if err != nil {
		return ""
	}

	startLine := max(centerLine-20, 1)
	endLine := centerLine + 20
	allLines := strings.Split(string(content), "\n")
	lines := make([]string, 0, min(len(allLines), endLine-startLine+1))
	for i, line := range allLines {
		lineNum := i + 1
		if lineNum < startLine {
			continue
		}
		if lineNum > endLine {
			break
		}
		lines = append(lines, fmt.Sprintf("%d: %s", lineNum, line))
	}
	return strings.Join(lines, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func formatThreadHistory(notes []domain.GitLabNote) string {
	var sb strings.Builder
	for _, n := range notes {
		role := n.AuthorName
		sb.WriteString(fmt.Sprintf("[%s] %s\n\n", role, n.Body))
	}
	return sb.String()
}
