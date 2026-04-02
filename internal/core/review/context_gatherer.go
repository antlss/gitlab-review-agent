package review

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/antlss/gitlab-review-agent/internal/core/prompt"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

type ContextGatherer struct {
	gitlabClient  domain.GitLabClient
	repoSettings  domain.RepositorySettingsStore
	feedbackStore domain.FeedbackStore
	botUserID     int64
}

func NewContextGatherer(
	gitlabClient domain.GitLabClient,
	repoSettings domain.RepositorySettingsStore,
	feedbackStore domain.FeedbackStore,
	botUserID int64,
) *ContextGatherer {
	return &ContextGatherer{
		gitlabClient:  gitlabClient,
		repoSettings:  repoSettings,
		feedbackStore: feedbackStore,
		botUserID:     botUserID,
	}
}

// Gather collects all review context concurrently.
func (g *ContextGatherer) Gather(ctx context.Context, job *domain.ReviewJob, diffFiles []domain.DiffFile) (*domain.ReviewContext, error) {
	var (
		mrInfo      *domain.GitLabMR
		settings    *domain.RepositorySettings
		feedbacks   []*domain.ReviewFeedback
		discussions []domain.GitLabDiscussion
	)

	eg, gCtx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		mr, err := g.gitlabClient.GetMR(gCtx, job.GitLabProjectID, job.MrIID)
		if err != nil {
			return fmt.Errorf("get MR info: %w", err)
		}
		mrInfo = mr
		return nil
	})

	eg.Go(func() error {
		s, err := g.repoSettings.GetByProjectID(gCtx, job.GitLabProjectID)
		if err != nil {
			return fmt.Errorf("get repo settings: %w", err)
		}
		settings = s

		fb, err := g.feedbackStore.ListRecentByProject(gCtx, job.GitLabProjectID, 5)
		if err != nil {
			slog.Warn("failed to load feedbacks", "error", err)
		}
		feedbacks = fb
		return nil
	})

	eg.Go(func() error {
		discs, err := g.gitlabClient.GetMRDiscussions(gCtx, job.GitLabProjectID, job.MrIID)
		if err != nil {
			slog.Warn("failed to load discussions", "error", err)
			return nil
		}
		discussions = discs
		return nil
	})

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	rc := &domain.ReviewContext{}

	if mrInfo != nil {
		rc.MRTitle = mrInfo.Title
		rc.MRDescription = mrInfo.Description
		rc.MissingIntent = strings.TrimSpace(mrInfo.Description) == ""
	}

	if settings != nil {
		rc.CustomPrompt = settings.CustomPrompt
	}

	for _, fb := range feedbacks {
		if fb.Signal == nil || fb.CommentSummary == nil {
			continue
		}
		rc.RecentFeedbacks = append(rc.RecentFeedbacks, domain.FeedbackSnippet{
			Category:       domain.DerefCategory(fb.Category),
			CommentSummary: *fb.CommentSummary,
			Signal:         *fb.Signal,
			CreatedAt:      fb.CreatedAt,
		})
	}

	for _, d := range discussions {
		if len(d.Notes) == 0 {
			continue
		}
		firstNote := d.Notes[0]
		if firstNote.Resolved || !firstNote.Resolvable {
			continue
		}
		ec := domain.ExistingComment{
			Summary:             domain.Truncate(firstNote.Body, 100),
			ContentHash:         buildContentHash(firstNote.Body),
			SemanticFingerprint: buildSemanticFingerprint(domain.CommentCategory(""), firstNote.Body),
		}
		if firstNote.Position != nil {
			ec.FilePath = firstNote.Position.FilePath
			ec.LineNumber = firstNote.Position.NewLine
			ec.LocationFingerprint = buildLocationFingerprint(ec.FilePath, ec.LineNumber, domain.CommentCategory(""))
		}
		rc.ExistingUnresolvedComments = append(rc.ExistingUnresolvedComments, ec)

		if firstNote.AuthorID == g.botUserID && firstNote.Position != nil {
			rc.BotUnresolvedComments = append(rc.BotUnresolvedComments, domain.BotUnresolvedComment{
				DiscussionID: d.ID,
				FilePath:     firstNote.Position.FilePath,
				LineNumber:   firstNote.Position.NewLine,
			})
		}
	}

	rc.DetectedLanguage = detectLanguage(diffFiles, job.RepoLanguage)
	rc.DetectedFramework = detectFramework(diffFiles, job.RepoFramework)
	rc.LanguageGuidelines = prompt.BuildLanguageGuidelines(rc.DetectedLanguage, rc.DetectedFramework)

	return rc, nil
}

func detectLanguage(files []domain.DiffFile, override *string) string {
	if override != nil && *override != "" {
		return *override
	}

	counts := make(map[string]int)
	for _, f := range files {
		ext := filepath.Ext(f.Path)
		switch ext {
		case ".go":
			counts["go"]++
		case ".ts", ".tsx":
			counts["typescript"]++
		case ".js", ".jsx":
			counts["javascript"]++
		case ".py":
			counts["python"]++
		case ".java":
			counts["java"]++
		case ".rs":
			counts["rust"]++
		case ".rb":
			counts["ruby"]++
		}
	}

	best, bestCount := "", 0
	for lang, count := range counts {
		if count > bestCount {
			best = lang
			bestCount = count
		}
	}
	return best
}

func detectFramework(files []domain.DiffFile, override *string) string {
	if override != nil && *override != "" {
		return *override
	}

	for _, f := range files {
		path := strings.ToLower(f.Path)
		if strings.Contains(path, "next.config") {
			return "nextjs"
		}
		if strings.Contains(path, "django") || strings.Contains(path, "manage.py") {
			return "django"
		}
		if strings.Contains(path, "gin") {
			return "gin"
		}
	}
	return ""
}
