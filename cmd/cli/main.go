package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"golang.org/x/sync/semaphore"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/core/agents/reviewer"
	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/core/review"
	"github.com/antlss/gitlab-review-agent/internal/pkg/git"
	"github.com/antlss/gitlab-review-agent/internal/pkg/gitlab"
	"github.com/antlss/gitlab-review-agent/internal/pkg/logger"
	"github.com/antlss/gitlab-review-agent/internal/pkg/store"
	"github.com/antlss/gitlab-review-agent/internal/shared"
)

func main() {
	_ = godotenv.Load()

	rootCmd := &cobra.Command{
		Use:   "review-agent",
		Short: "AI Code Review Agent CLI",
	}

	rootCmd.AddCommand(reviewCmd())
	rootCmd.AddCommand(cloneAllCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func reviewCmd() *cobra.Command {
	var projectID int64
	var mrID int64
	var model string

	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review a merge request with interactive comment selection",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			logger.Setup(cfg.Log.Level, cfg.Log.Format)

			stores, err := store.New(cfg.Store)
			if err != nil {
				return fmt.Errorf("store: %w", err)
			}
			defer stores.Close()

			ctx := context.Background()

			gitlabClient := gitlab.NewClient(cfg.GitLab.BaseURL, cfg.GitLab.Token)
			project, err := gitlabClient.GetProject(ctx, projectID)
			if err != nil {
				return fmt.Errorf("fetch project info: %w", err)
			}

			mr, err := gitlabClient.GetMR(ctx, projectID, mrID)
			if err != nil {
				return fmt.Errorf("fetch MR info: %w", err)
			}
			settings, err := stores.RepoSettings.GetOrCreate(ctx, projectID, project.PathWithNS)
			if err != nil {
				return fmt.Errorf("get or create repo settings: %w", err)
			}

			job := &shared.ReviewJob{
				ID:                uuid.New(),
				GitLabProjectID:   projectID,
				MrIID:             mrID,
				HeadSHA:           mr.HeadSHA,
				TargetBranch:      mr.TargetBranch,
				SourceBranch:      mr.SourceBranch,
				TriggerSource:     shared.TriggerSourceCLI,
				Status:            shared.ReviewJobStatusPending,
				DryRun:            true,
				RepoModelOverride: settings.ModelOverride,
				RepoLanguage:      settings.Language,
				RepoFramework:     settings.Framework,
				QueuedAt:          time.Now(),
			}

			if model != "" {
				job.RepoModelOverride = &model
			}

			if err := stores.ReviewJobs.Create(ctx, job); err != nil {
				return fmt.Errorf("create job: %w", err)
			}

			fmt.Printf("Review job: %s\n", job.ID)
			fmt.Printf("MR: %s !%d (%s → %s)\n", project.PathWithNS, mrID, mr.SourceBranch, mr.TargetBranch)
			fmt.Println("Running review pipeline...")

			gitManager := git.NewManager(cfg.Git.ReposDir, cfg.GitLab.BaseURL, cfg.GitLab.Token)
			gatherer := review.NewContextGatherer(gitlabClient, stores.RepoSettings, stores.Feedbacks, cfg.GitLab.BotUserID)
			reviewAgent := reviewer.NewAgent()

			reviewPipeline := review.NewPipeline(review.PipelineDeps{
				Config:        *cfg,
				JobStore:      stores.ReviewJobs,
				RepoSettings:  stores.RepoSettings,
				RecordStore:   stores.ReviewRecords,
				FeedbackStore: stores.Feedbacks,
				GitLabClient:  gitlabClient,
				GitManager:    gitManager,
				Gatherer:      gatherer,
				Agent:         reviewAgent,
			})

			if err := reviewPipeline.Execute(ctx, job); err != nil {
				fmt.Printf("\nPipeline failed: %v\n", err)
				return err
			}

			updatedJob, err := stores.ReviewJobs.GetByID(ctx, job.ID)
			if err != nil {
				return fmt.Errorf("fetch updated job: %w", err)
			}

			switch updatedJob.Status {
			case shared.ReviewJobStatusFailed, shared.ReviewJobStatusParseFailed:
				fmt.Printf("\nFailed: %s\n", shared.DerefStr(updatedJob.ErrorMessage))
				return nil
			case shared.ReviewJobStatusSkippedSize:
				fmt.Printf("\nSkipped: %s\n", updatedJob.Status)
				return nil
			}

			var comments []shared.ParsedComment
			if len(updatedJob.AIOutputParsed) > 0 {
				if err := json.Unmarshal(updatedJob.AIOutputParsed, &comments); err != nil {
					fmt.Printf("\nFailed to parse comments: %v\n", err)
					return nil
				}
			}

			var actionable []shared.ParsedComment
			suppressed := 0
			for _, c := range comments {
				if c.Suppressed {
					suppressed++
					continue
				}
				actionable = append(actionable, c)
			}

			fmt.Printf("\n%s\n", strings.Repeat("─", 60))
			if len(actionable) == 0 {
				fmt.Printf("No issues found. (suppressed: %d)\n", suppressed)
				return nil
			}

			fmt.Printf("Found %d issues", len(actionable))
			if suppressed > 0 {
				fmt.Printf(" (+ %d suppressed)", suppressed)
			}
			fmt.Println()
			fmt.Println(strings.Repeat("─", 60))

			for i, c := range actionable {
				printComment(i+1, &c)
			}

			fmt.Println(strings.Repeat("─", 60))
			fmt.Println("Options:")
			fmt.Println("  a  - Post ALL comments to GitLab")
			fmt.Println("  n  - Post NONE (exit)")
			fmt.Println("  1,3,5 or 1-3,5 - Post selected comments")
			fmt.Print("\nYour choice: ")

			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))

			if input == "" || input == "n" || input == "q" {
				fmt.Println("No comments posted.")
				return nil
			}

			var selectedIndices []int
			if input == "a" || input == "all" {
				for i := range actionable {
					selectedIndices = append(selectedIndices, i)
				}
			} else {
				selectedIndices, err = parseSelection(input, len(actionable))
				if err != nil {
					fmt.Printf("Invalid selection: %v\n", err)
					return nil
				}
			}

			if len(selectedIndices) == 0 {
				fmt.Println("No comments selected.")
				return nil
			}

			baseSHA := shared.DerefStr(updatedJob.BaseSHA)
			if baseSHA == "" {
				fmt.Println("Error: base SHA not available")
				return nil
			}

			lang := prompt.ParseLanguage(cfg.Review.ResponseLanguage)
			fmt.Printf("\nPosting %d comments...\n", len(selectedIndices))
			posted := 0
			for _, idx := range selectedIndices {
				c := &actionable[idx]
				body := review.FormatComment(c, lang)
				resp, err := gitlabClient.PostInlineComment(ctx, shared.PostInlineCommentRequest{
					ProjectID: projectID,
					MrIID:     mrID,
					Body:      body,
					FilePath:  c.FilePath,
					NewLine:   c.LineNumber,
					BaseSHA:   baseSHA,
					HeadSHA:   updatedJob.HeadSHA,
					StartSHA:  baseSHA,
				})
				if err != nil {
					fmt.Printf("  ✗ [%d] %s:%d — %v\n", idx+1, c.FilePath, c.LineNumber, err)
					continue
				}

				fb := &shared.ReviewFeedback{
					GitLabProjectID:    projectID,
					ReviewJobID:        &updatedJob.ID,
					GitLabDiscussionID: resp.DiscussionID,
					GitLabNoteID:       resp.NoteID,
					FilePath:           &c.FilePath,
					LineNumber:         &c.LineNumber,
					Category:           &c.Category,
					CommentSummary:     shared.StrPtr(shared.Truncate(c.ReviewComment, 200)),
				}
				stores.Feedbacks.Create(ctx, fb)

				fmt.Printf("  ✓ [%d] %s:%d\n", idx+1, c.FilePath, c.LineNumber)
				posted++
			}

			fmt.Printf("\nDone! Posted %d/%d comments.\n", posted, len(selectedIndices))
			return nil
		},
	}

	cmd.Flags().Int64Var(&projectID, "project-id", 0, "GitLab project ID")
	cmd.Flags().Int64Var(&mrID, "mr-id", 0, "Merge request IID")
	cmd.Flags().StringVar(&model, "model", "", "Override model")
	cmd.MarkFlagRequired("project-id")
	cmd.MarkFlagRequired("mr-id")

	return cmd
}

func cloneAllCmd() *cobra.Command {
	var concurrency int
	var cloneTimeout time.Duration
	var outputDir string
	var skipExisting bool
	var minAccessLevel int

	cmd := &cobra.Command{
		Use:   "clone-all",
		Short: "Clone all accessible GitLab repositories into the repos directory",
		Long: `Fetches every GitLab project the token can access and clones it locally.
Repositories that already exist are updated with git fetch instead of re-cloned.
Useful to pre-warm the local repo cache before starting the review server.

Access levels: 10=guest, 20=reporter, 30=developer, 40=maintainer, 50=owner`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			logger.Setup(cfg.Log.Level, cfg.Log.Format)

			reposDir := cfg.Git.ReposDir
			if outputDir != "" {
				reposDir = outputDir
			}
			if err := os.MkdirAll(reposDir, 0o755); err != nil {
				return fmt.Errorf("create repos dir: %w", err)
			}

			ctx := context.Background()
			gitlabClient := gitlab.NewClient(cfg.GitLab.BaseURL, cfg.GitLab.Token)

			fmt.Printf("Fetching project list from %s", cfg.GitLab.BaseURL)
			if minAccessLevel > 0 {
				fmt.Printf(" (min access level %d)", minAccessLevel)
			}
			fmt.Println("...")

			projects, err := gitlabClient.ListAllProjects(ctx, minAccessLevel)
			if err != nil {
				return fmt.Errorf("list projects: %w", err)
			}
			fmt.Printf("Found %d projects. Cloning with concurrency=%d, per-repo timeout=%s\n\n",
				len(projects), concurrency, cloneTimeout)

			gitManager := git.NewManager(reposDir, cfg.GitLab.BaseURL, cfg.GitLab.Token)

			sem := semaphore.NewWeighted(int64(concurrency))
			var wg sync.WaitGroup

			var (
				nCloned  atomic.Int64
				nFetched atomic.Int64
				nSkipped atomic.Int64
				nFailed  atomic.Int64
				failMu   sync.Mutex
				failures []string
			)

			for _, proj := range projects {
				wg.Add(1)
				go func(p gitlab.ProjectBasic) {
					defer wg.Done()

					// Acquire semaphore slot (respect global ctx for shutdown)
					if acquireErr := sem.Acquire(ctx, 1); acquireErr != nil {
						nFailed.Add(1)
						fmt.Printf("  [FAIL] %s: %v\n", p.PathWithNamespace, acquireErr)
						return
					}
					defer sem.Release(1)

					// Skip-existing: don't even fetch if .git already present
					if skipExisting {
						gitDir := filepath.Join(gitManager.RepoPath(p.ID), ".git")
						if _, statErr := os.Stat(gitDir); statErr == nil {
							nSkipped.Add(1)
							fmt.Printf("  [SKIP] %s\n", p.PathWithNamespace)
							return
						}
					}

					repoCtx, cancel := context.WithTimeout(ctx, cloneTimeout)
					defer cancel()

					cloned, opErr := gitManager.CloneOrFetch(repoCtx, p.ID, p.HTTPURLToRepo)
					if opErr != nil {
						nFailed.Add(1)
						msg := fmt.Sprintf("%s: %v", p.PathWithNamespace, opErr)
						failMu.Lock()
						failures = append(failures, msg)
						failMu.Unlock()
						fmt.Printf("  [FAIL] %s\n", msg)
						return
					}
					if cloned {
						nCloned.Add(1)
						fmt.Printf("  [NEW]  %s\n", p.PathWithNamespace)
					} else {
						nFetched.Add(1)
						fmt.Printf("  [OK]   %s\n", p.PathWithNamespace)
					}
				}(proj)
			}

			wg.Wait()

			// ── Summary ──────────────────────────────────────────────────────────
			fmt.Println()
			fmt.Println(strings.Repeat("─", 60))
			fmt.Printf("Done. cloned=%d  fetched=%d  skipped=%d  failed=%d\n",
				nCloned.Load(), nFetched.Load(), nSkipped.Load(), nFailed.Load())

			if len(failures) > 0 {
				fmt.Printf("\nFailed repos (%d):\n", len(failures))
				for _, f := range failures {
					fmt.Printf("  • %s\n", f)
				}
				return fmt.Errorf("%d repo(s) failed", len(failures))
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&concurrency, "concurrency", 3, "Number of parallel clone/fetch operations")
	cmd.Flags().DurationVar(&cloneTimeout, "clone-timeout", 30*time.Minute, "Per-repo operation timeout (increase for very large repos)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Override repos directory (default: GIT_REPOS_DIR from config)")
	cmd.Flags().BoolVar(&skipExisting, "skip-existing", false, "Skip repos that already have a local clone (do not fetch updates)")
	cmd.Flags().IntVar(&minAccessLevel, "min-access-level", 0, "Minimum access level filter (0=all, 20=reporter, 30=developer, 40=maintainer, 50=owner)")
	cmd.SilenceUsage = true

	return cmd
}

func printComment(index int, c *shared.ParsedComment) {
	fmt.Printf("\n[%d] %s:%d\n", index, c.FilePath, c.LineNumber)
	fmt.Printf("    Severity: %-8s  Category: %s  Confidence: %s\n",
		strings.ToUpper(string(c.Severity)), strings.ToUpper(string(c.Category)), c.Confidence)
	wrapped := wrapText(c.ReviewComment, 76)
	for _, line := range strings.Split(wrapped, "\n") {
		fmt.Printf("    %s\n", line)
	}
}

func wrapText(text string, width int) string {
	var sb strings.Builder
	for _, paragraph := range strings.Split(text, "\n") {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		words := strings.Fields(paragraph)
		lineLen := 0
		for i, w := range words {
			if i > 0 && lineLen+1+len(w) > width {
				sb.WriteString("\n")
				lineLen = 0
			} else if i > 0 {
				sb.WriteString(" ")
				lineLen++
			}
			sb.WriteString(w)
			lineLen += len(w)
		}
	}
	return sb.String()
}


// parseSelection parses input like "1,3,5" or "1-3,5" or "2-4" into 0-based indices.
func parseSelection(input string, max int) ([]int, error) {
	seen := make(map[int]bool)
	var result []int

	parts := strings.Split(input, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			rangeParts := strings.SplitN(part, "-", 2)
			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[0])
			}
			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", rangeParts[1])
			}
			if start < 1 || end > max || start > end {
				return nil, fmt.Errorf("range %d-%d out of bounds (1-%d)", start, end, max)
			}
			for i := start; i <= end; i++ {
				if !seen[i-1] {
					result = append(result, i-1)
					seen[i-1] = true
				}
			}
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", part)
			}
			if n < 1 || n > max {
				return nil, fmt.Errorf("number %d out of bounds (1-%d)", n, max)
			}
			if !seen[n-1] {
				result = append(result, n-1)
				seen[n-1] = true
			}
		}
	}

	return result, nil
}
