package review

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/core/agents/reviewer"
	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/domain"
	"github.com/antlss/gitlab-review-agent/internal/pkg/git"
	"github.com/antlss/gitlab-review-agent/internal/pkg/llm"
)

type Pipeline struct {
	cfg           config.Config
	jobStore      domain.ReviewJobStore
	repoSettings  domain.RepositorySettingsStore
	recordStore   domain.ReviewRecordStore
	feedbackStore domain.FeedbackStore
	gitlabClient  domain.GitLabClient
	gitManager    *git.Manager
	gatherer      *ContextGatherer
	agent         *reviewer.Agent
}

type PipelineDeps struct {
	Config        config.Config
	JobStore      domain.ReviewJobStore
	RepoSettings  domain.RepositorySettingsStore
	RecordStore   domain.ReviewRecordStore
	FeedbackStore domain.FeedbackStore
	GitLabClient  domain.GitLabClient
	GitManager    *git.Manager
	Gatherer      *ContextGatherer
	Agent         *reviewer.Agent
}

func NewPipeline(deps PipelineDeps) *Pipeline {
	return &Pipeline{
		cfg:           deps.Config,
		jobStore:      deps.JobStore,
		repoSettings:  deps.RepoSettings,
		recordStore:   deps.RecordStore,
		feedbackStore: deps.FeedbackStore,
		gitlabClient:  deps.GitLabClient,
		gitManager:    deps.GitManager,
		gatherer:      deps.Gatherer,
		agent:         deps.Agent,
	}
}

func (p *Pipeline) Execute(ctx context.Context, job *domain.ReviewJob) error {
	log := slog.With("job_id", job.ID.String(), "project_id", job.GitLabProjectID, "mr_iid", job.MrIID)

	if err := p.jobStore.UpdateStatus(ctx, job.ID, domain.ReviewJobStatusReviewing, nil); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	settings, err := p.repoSettings.GetByProjectID(ctx, job.GitLabProjectID)
	if err != nil {
		return p.failJob(ctx, job, "get repo settings: "+err.Error())
	}
	projectPath := ""
	if settings != nil {
		projectPath = settings.ProjectPath
	}
	if err := p.acquireAndFetch(ctx, job, projectPath); err != nil {
		return p.failJob(ctx, job, err.Error())
	}
	log.Debug("git checkout completed", "head_sha", job.HeadSHA, "project_path", projectPath)

	// Determine base SHA: use previous review's HeadSHA for incremental, or target branch
	fullRecheck := job.IsForcePush
	baseSHA, err := p.determineBaseSHA(ctx, job, fullRecheck)
	if err != nil {
		return p.failJob(ctx, job, "determine base SHA: "+err.Error())
	}
	log.Info("base SHA determined", "base_sha", baseSHA, "force_push", fullRecheck)
	if err := p.jobStore.UpdateBaseSHA(ctx, job.ID, baseSHA); err != nil {
		log.Warn("failed to update base SHA", "error", err)
	}

	diffFiles, err := p.gitManager.Diff(ctx, job.GitLabProjectID, baseSHA, job.HeadSHA)
	if err != nil {
		return p.failJob(ctx, job, "git diff: "+err.Error())
	}

	excludePatterns := append(DefaultExcludePatterns(), job.ExcludePatternList()...)
	var filteredFiles []domain.DiffFile
	for i := range diffFiles {
		f := &diffFiles[i]
		if f.Status == "D" {
			continue
		}
		if ShouldExclude(f.Path, excludePatterns) {
			continue
		}
		ScoreRisk(f)
		filteredFiles = append(filteredFiles, *f)
	}

	log.Info("diff files filtered", "total", len(diffFiles), "filtered", len(filteredFiles))

	if len(filteredFiles) > p.cfg.Review.MaxFilesBeforeSample {
		if p.cfg.Review.LargePRAction == "block" {
			msg := fmt.Sprintf("MR has %d files (max %d). Skipping review.", len(filteredFiles), p.cfg.Review.MaxFilesBeforeSample)
			if _, err := p.gitlabClient.PostThreadComment(ctx, job.GitLabProjectID, job.MrIID, msg); err != nil {
				log.Warn("failed to post skip comment", "error", err)
			}
			return p.jobStore.UpdateStatus(ctx, job.ID, domain.ReviewJobStatusSkippedSize, nil)
		}
		slices.SortFunc(filteredFiles, func(a, b domain.DiffFile) int {
			return cmp.Compare(b.RiskScore, a.RiskScore) // descending
		})
		if len(filteredFiles) > p.cfg.Review.SampleFileCount {
			filteredFiles = filteredFiles[:p.cfg.Review.SampleFileCount]
		}
	}

	slices.SortFunc(filteredFiles, func(a, b domain.DiffFile) int {
		return cmp.Compare(b.RiskScore, a.RiskScore) // descending
	})

	reviewCtx, err := p.gatherer.Gather(ctx, job, filteredFiles)
	if err != nil {
		return p.failJob(ctx, job, "context gathering: "+err.Error())
	}

	llmClient, err := llm.NewBalancedClientFromConfig(p.cfg.LLM, job.RepoModelOverride)
	if err != nil {
		return p.failJob(ctx, job, "create LLM client: "+err.Error())
	}
	if err := p.jobStore.UpdateModelUsed(ctx, job.ID, llmClient.ModelName()); err != nil {
		log.Warn("failed to update model used", "error", err)
	}

	lang := prompt.ParseLanguage(p.cfg.Review.ResponseLanguage)

	// Review all sampled files in a single unified pass so the model keeps full
	// cross-file context instead of relying on chunk boundaries.
	var aggregated *aggregatedResult
	aggregated, err = p.executeSingle(ctx, job, filteredFiles, reviewCtx, llmClient, baseSHA, lang)
	if err != nil {
		return p.failJob(ctx, job, "agent: "+err.Error())
	}

	comments := ValidateAndFilter(aggregated.parsed, filteredFiles, reviewCtx.ExistingUnresolvedComments)
	if err := p.jobStore.UpdateAIOutput(ctx, job.ID, aggregated.rawOutput, comments, aggregated.totalIterations, aggregated.totalTokens); err != nil {
		log.Warn("failed to update AI output", "error", err)
	}

	if job.DryRun {
		return p.jobStore.UpdateCompleted(ctx, job.ID, 0, 0)
	}

	if err := p.jobStore.UpdateStatus(ctx, job.ID, domain.ReviewJobStatusPosting, nil); err != nil {
		log.Warn("failed to update status to POSTING", "error", err)
	}
	posted, suppressed := 0, 0
	for i := range comments {
		c := &comments[i]
		if c.Suppressed {
			suppressed++
			continue
		}

		body := FormatComment(c, lang)
		resp, err := p.gitlabClient.PostInlineComment(ctx, domain.PostInlineCommentRequest{
			ProjectID: job.GitLabProjectID,
			MrIID:     job.MrIID,
			Body:      body,
			FilePath:  c.FilePath,
			NewLine:   c.LineNumber,
			BaseSHA:   baseSHA,
			HeadSHA:   job.HeadSHA,
			StartSHA:  baseSHA,
		})
		if err != nil {
			log.Warn("failed to post comment", "file", c.FilePath, "line", c.LineNumber, "error", err)
			continue
		}

		c.GitLabNoteID = &resp.NoteID
		c.GitLabDiscussionID = &resp.DiscussionID
		posted++

		fb := &domain.ReviewFeedback{
			GitLabProjectID:    job.GitLabProjectID,
			ReviewJobID:        &job.ID,
			GitLabDiscussionID: resp.DiscussionID,
			GitLabNoteID:       resp.NoteID,
			FilePath:           &c.FilePath,
			LineNumber:         &c.LineNumber,
			Category:           &c.Category,
			CommentSummary:     domain.StrPtr(domain.Truncate(c.ReviewComment, 200)),
			Language:           domain.StrPtr(reviewCtx.DetectedLanguage),
			ModelUsed:          domain.StrPtr(llmClient.ModelName()),
		}
		if err := p.feedbackStore.Create(ctx, fb); err != nil {
			log.Warn("failed to create feedback", "error", err)
		}
	}

	// Auto-resolve previous bot threads where the flagged line was modified in this diff
	resolved := p.autoResolveFixedThreads(ctx, job, reviewCtx.BotUnresolvedComments, filteredFiles)
	if resolved > 0 {
		log.Info("auto-resolved fixed threads", "count", resolved)
	}

	summary := buildSummaryComment(posted, suppressed, len(comments), resolved, aggregated, llmClient.ModelName(), lang)
	if _, err := p.gitlabClient.PostThreadComment(ctx, job.GitLabProjectID, job.MrIID, summary); err != nil {
		log.Warn("failed to post summary comment", "error", err)
	}

	if err := p.jobStore.UpdateCompleted(ctx, job.ID, posted, suppressed); err != nil {
		log.Warn("failed to update job completed", "error", err)
	}

	reviewedFiles := extractFilePaths(filteredFiles)
	filesJSON, err := json.Marshal(reviewedFiles)
	if err != nil {
		log.Warn("failed to marshal reviewed files", "error", err)
		filesJSON = []byte("[]")
	}
	if err := p.recordStore.Upsert(ctx, &domain.ReviewRecord{
		GitLabProjectID: job.GitLabProjectID,
		MrIID:           job.MrIID,
		ReviewJobID:     job.ID,
		HeadSHA:         job.HeadSHA,
		ReviewedFiles:   filesJSON,
		CommentsPosted:  posted,
	}); err != nil {
		log.Warn("failed to upsert review record", "error", err)
	}

	if err := p.repoSettings.IncrementFeedbackCount(ctx, job.GitLabProjectID, posted); err != nil {
		log.Warn("failed to increment feedback count", "error", err)
	}

	log.Info("review completed", "posted", posted, "suppressed", suppressed,
		"iterations", aggregated.totalIterations, "chunks", aggregated.chunksUsed)
	return nil
}

// aggregatedResult holds merged results from one or more agent chunks.
type aggregatedResult struct {
	parsed          *ParsedOutput
	rawOutput       string
	totalIterations int
	totalTokens     int
	chunksUsed      int
	stopReason      string
}

// executeSingle runs the original single-agent review for small MRs.
func (p *Pipeline) executeSingle(
	ctx context.Context,
	job *domain.ReviewJob,
	filteredFiles []domain.DiffFile,
	reviewCtx *domain.ReviewContext,
	llmClient domain.LLMClient,
	baseSHA string,
	lang prompt.ResponseLanguage,
) (*aggregatedResult, error) {
	log := slog.With("job_id", job.ID.String())

	log.Info("single-pass structured review", "files", len(filteredFiles), "passes", 2)

	result, err := p.runStructuredChunkReview(ctx, job, filteredFiles, reviewCtx, llmClient, baseSHA, lang)
	if err != nil {
		return nil, err
	}

	if result.parsed == nil {
		p.jobStore.UpdateAIOutput(ctx, job.ID, result.rawOutput, nil, result.llmCalls, result.tokensEstimated)
		return nil, fmt.Errorf("structured review returned no parsed output")
	}

	return &aggregatedResult{
		parsed:          result.parsed,
		rawOutput:       result.rawOutput,
		totalIterations: result.llmCalls,
		totalTokens:     result.tokensEstimated,
		chunksUsed:      1,
		stopReason:      result.stopReason,
	}, nil
}

// executeChunked splits files into domain-grouped chunks and reviews them in parallel.
func (p *Pipeline) executeChunked(
	ctx context.Context,
	job *domain.ReviewJob,
	filteredFiles []domain.DiffFile,
	reviewCtx *domain.ReviewContext,
	llmClient domain.LLMClient,
	baseSHA string,
	lang prompt.ResponseLanguage,
) (*aggregatedResult, error) {
	log := slog.With("job_id", job.ID.String())

	chunks := ChunkFiles(filteredFiles, p.cfg.Review.ChunkSize)
	log.Info("chunked review started", "total_files", len(filteredFiles), "chunks", len(chunks))

	type chunkResult struct {
		result *reviewer.AgentResult
		parsed *ParsedOutput
		err    error
	}

	results := make([]chunkResult, len(chunks))
	var wg sync.WaitGroup

	// Adaptive parallelism: scale with key count so we never exceed API capacity.
	// Cap at the configured maximum to avoid overwhelming small instances.
	maxParallel := max(1, min(llmClient.ClientCount()*2, p.cfg.Review.MaxParallelChunks))
	sem := make(chan struct{}, maxParallel)

	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, chunkFiles []domain.DiffFile) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Error("chunk review panicked", "chunk", idx+1, "panic", r)
					results[idx] = chunkResult{err: fmt.Errorf("chunk panicked: %v", r)}
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()

			chunkLog := log.With("chunk", idx+1, "chunk_files", len(chunkFiles))
			chunkLog.Info("chunk review started", "files", extractFilePaths(chunkFiles))

			chunkLog.Info("chunk structured review", "passes", 2)

			result, err := p.runStructuredChunkReview(ctx, job, chunkFiles, reviewCtx, llmClient, baseSHA, lang)
			if err != nil {
				chunkLog.Error("chunk review failed", "error", err)
				results[idx] = chunkResult{err: err}
				return
			}

			chunkLog.Info("chunk review completed",
				"iterations", result.llmCalls, "findings", len(result.parsed.Reviews))
			results[idx] = chunkResult{
				parsed: result.parsed,
				result: &reviewer.AgentResult{
					RawOutput:       result.rawOutput,
					IterationsUsed:  result.llmCalls,
					TokensEstimated: result.tokensEstimated,
					StopReason:      result.stopReason,
				},
			}
		}(i, chunk)
	}

	wg.Wait()

	// Merge all chunk results
	var allReviews []RawReview
	var allRawOutputs []string
	totalIterations := 0
	totalTokens := 0
	failedChunks := 0

	for i, cr := range results {
		if cr.err != nil {
			log.Warn("chunk failed, skipping", "chunk", i+1, "error", cr.err)
			failedChunks++
			continue
		}
		if cr.parsed != nil {
			allReviews = append(allReviews, cr.parsed.Reviews...)
		}
		if cr.result != nil {
			allRawOutputs = append(allRawOutputs, cr.result.RawOutput)
			totalIterations += cr.result.IterationsUsed
			totalTokens += cr.result.TokensEstimated
		}
	}

	if failedChunks == len(chunks) {
		return nil, fmt.Errorf("all %d chunks failed", failedChunks)
	}

	log.Info("chunked review completed",
		"total_findings", len(allReviews),
		"total_iterations", totalIterations,
		"total_tokens", totalTokens,
		"failed_chunks", failedChunks,
	)

	stopReason := "chunked_complete"
	if failedChunks > 0 {
		stopReason = fmt.Sprintf("chunked_partial_%d_failed", failedChunks)
	}

	return &aggregatedResult{
		parsed:          &ParsedOutput{Reviews: allReviews},
		rawOutput:       strings.Join(allRawOutputs, "\n---\n"),
		totalIterations: totalIterations,
		totalTokens:     totalTokens,
		chunksUsed:      len(chunks),
		stopReason:      stopReason,
	}, nil
}

// preloadDiffsForFiles preloads all diffs for a set of files, respecting size limits.
func (p *Pipeline) preloadDiffsForFiles(ctx context.Context, projectID int64, files []domain.DiffFile, baseSHA, headSHA string) (string, bool) {
	preloadMaxBytes := p.cfg.Review.PreloadDiffMaxKB * 1024
	content, included := p.computePreloadedDiffs(ctx, projectID, files, baseSHA, headSHA, preloadMaxBytes)
	allPreloaded := included == len(files)
	return content, allPreloaded
}

// acquireAndFetch wraps git lock acquisition + fetch/checkout in a single
// function so defer correctly scopes the lock release to only the git operations.
func (p *Pipeline) acquireAndFetch(ctx context.Context, job *domain.ReviewJob, projectPath string) error {
	if err := p.gitManager.AcquireGitLock(ctx, job.GitLabProjectID); err != nil {
		return err
	}
	defer p.gitManager.ReleaseGitLock(ctx, job.GitLabProjectID)
	return p.gitManager.FetchAndCheckout(ctx, job.GitLabProjectID, projectPath, job.MrIID, job.TargetBranch, job.HeadSHA)
}

func (p *Pipeline) determineBaseSHA(ctx context.Context, job *domain.ReviewJob, fullRecheck bool) (string, error) {
	if fullRecheck {
		return p.gitManager.RevParse(ctx, job.GitLabProjectID, p.gitManager.TargetBranchRef(job.TargetBranch))
	}

	record, err := p.recordStore.GetLastCompleted(ctx, job.GitLabProjectID, job.MrIID)
	if err != nil {
		return "", err
	}
	if record == nil {
		return p.gitManager.RevParse(ctx, job.GitLabProjectID, p.gitManager.TargetBranchRef(job.TargetBranch))
	}

	if !p.gitManager.SHAExists(ctx, job.GitLabProjectID, record.HeadSHA) {
		slog.Info("incremental base SHA not found, using target branch",
			"project_id", job.GitLabProjectID, "mr_iid", job.MrIID)
		return p.gitManager.RevParse(ctx, job.GitLabProjectID, p.gitManager.TargetBranchRef(job.TargetBranch))
	}

	ancestor, err := p.gitManager.IsAncestor(ctx, job.GitLabProjectID, record.HeadSHA, job.HeadSHA)
	if err != nil {
		return "", fmt.Errorf("check incremental ancestry: %w", err)
	}
	if ancestor {
		return record.HeadSHA, nil
	}

	slog.Info("incremental base SHA is not an ancestor, using target branch",
		"project_id", job.GitLabProjectID, "mr_iid", job.MrIID)
	return p.gitManager.RevParse(ctx, job.GitLabProjectID, "origin/"+job.TargetBranch)
}

// autoResolveFixedThreads resolves previous bot comment threads where the
// flagged file+line was modified in the current diff (likely fixed by new commit).
func (p *Pipeline) autoResolveFixedThreads(ctx context.Context, job *domain.ReviewJob, botComments []domain.BotUnresolvedComment, diffFiles []domain.DiffFile) int {
	if len(botComments) == 0 {
		return 0
	}

	// Build set of modified lines per file
	modifiedLines := make(map[string]map[int]bool)
	for _, f := range diffFiles {
		if len(f.AddedLines) == 0 {
			continue
		}
		lineSet := make(map[int]bool, len(f.AddedLines))
		for _, ln := range f.AddedLines {
			lineSet[ln] = true
		}
		modifiedLines[f.Path] = lineSet
	}

	resolved := 0
	for _, bc := range botComments {
		lines, ok := modifiedLines[bc.FilePath]
		if !ok {
			continue
		}
		if !lines[bc.LineNumber] {
			continue
		}
		if err := p.gitlabClient.ResolveDiscussion(ctx, job.GitLabProjectID, job.MrIID, bc.DiscussionID); err != nil {
			slog.Warn("failed to auto-resolve discussion", "discussion_id", bc.DiscussionID, "error", err)
			continue
		}
		resolved++
	}
	return resolved
}

func (p *Pipeline) failJob(ctx context.Context, job *domain.ReviewJob, msg string) error {
	slog.Error("review job failed", "job_id", job.ID.String(), "error", msg)
	if err := p.jobStore.UpdateStatus(ctx, job.ID, domain.ReviewJobStatusFailed, &msg); err != nil {
		slog.Error("failed to mark job as failed in store", "job_id", job.ID.String(), "store_error", err)
	}
	return errors.New(msg)
}

func formatDiffStat(files []domain.DiffFile) string {
	var sb strings.Builder
	for _, f := range files {
		icon := "🟢"
		switch f.RiskTier {
		case domain.RiskHigh:
			icon = "🔴"
		case domain.RiskMedium:
			icon = "🟡"
		}
		fmt.Fprintf(&sb, "%s %s (+%d/-%d) [%s]\n", icon, f.Path, f.LinesAdded, f.LinesRemoved, f.RiskTier)
	}
	return sb.String()
}

func FormatComment(c *domain.ParsedComment, lang prompt.ResponseLanguage) string {
	badge := SeverityBadge(c.Severity)
	comment := fmt.Sprintf("%s **[%s]** %s", badge, strings.ToUpper(string(c.Category)), c.ReviewComment)
	if c.Suggestion != "" {
		comment += fmt.Sprintf("\n\n💡 **%s**\n```suggestion\n%s\n```", prompt.SuggestionLabel(lang), c.Suggestion)
	}
	return comment
}

func SeverityBadge(s domain.CommentSeverity) string {
	switch s {
	case domain.SeverityCritical:
		return "🔴 `CRITICAL`"
	case domain.SeverityHigh:
		return "🟠 `HIGH`"
	case domain.SeverityMedium:
		return "🟡 `MEDIUM`"
	default:
		return "🔵 `LOW`"
	}
}

func buildSummaryComment(posted, suppressed, total, resolved int, result *aggregatedResult, model string, lang prompt.ResponseLanguage) string {
	var sb strings.Builder
	sb.WriteString("## AI Review Summary\n\n")

	if total == 0 {
		sb.WriteString(prompt.SummaryLGTM())
	} else if posted == 0 && suppressed > 0 {
		sb.WriteString(prompt.SummaryAllFiltered(lang, suppressed))
	} else {
		sb.WriteString(prompt.SummaryPostedCount(lang, posted))
		if suppressed > 0 {
			sb.WriteString(prompt.SummaryFilteredCount(lang, suppressed))
		}
		sb.WriteString("\n")
	}

	if resolved > 0 {
		sb.WriteString(prompt.SummaryAutoResolved(lang, resolved))
	}

	fmt.Fprintf(&sb, "- **Model:** %s\n", model)
	if result.chunksUsed > 1 {
		fmt.Fprintf(&sb, "- **Chunks:** %d (parallel map-reduce)\n", result.chunksUsed)
	}
	fmt.Fprintf(&sb, "- **Iterations:** %d (stop: %s)\n", result.totalIterations, result.stopReason)

	if posted > 0 {
		sb.WriteString(prompt.SummaryReplyHint(lang))
	}
	return sb.String()
}

func extractFilePaths(files []domain.DiffFile) []string {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	return paths
}

func (p *Pipeline) computePreloadedDiffs(ctx context.Context, projectID int64, files []domain.DiffFile, baseSHA, headSHA string, maxBytes int) (string, int) {
	if len(files) == 0 || maxBytes <= 0 {
		return "", 0
	}
	var sb strings.Builder
	totalBytes := 0
	included := 0
	for _, f := range files {
		raw, err := p.gitManager.DiffFile(ctx, projectID, baseSHA, headSHA, f.Path)
		if err != nil || len(raw) == 0 {
			continue
		}
		// Compact the diff: strip metadata, remove pure-deletion hunks, reduce
		// context lines to 2.  This cuts token usage by 40-60 % on typical MRs.
		compacted := CompactDiff(raw, 2)
		if compacted == "" {
			// Diff became empty after compaction (e.g. file was only deletions).
			// Still count as included so allPreloaded is accurate.
			included++
			continue
		}
		header := fmt.Sprintf("--- %s ---\n", f.Path)
		entry := header + compacted + "\n"
		if totalBytes+len(entry) > maxBytes {
			fmt.Fprintf(&sb, "--- %s: (diff omitted — size limit reached) ---\n", f.Path)
			continue
		}
		sb.WriteString(entry)
		totalBytes += len(entry)
		included++
	}
	return sb.String(), included
}
