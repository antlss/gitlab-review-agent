package cron

import (
	"context"
	"log/slog"

	"github.com/antlss/gitlab-review-agent/internal/core/feedback"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

type FeedbackConsolidatorJob struct {
	repoSettings domain.RepositorySettingsStore
	consolidator *feedback.Consolidator
}

func NewFeedbackConsolidatorJob(
	repoSettings domain.RepositorySettingsStore,
	consolidator *feedback.Consolidator,
) *FeedbackConsolidatorJob {
	return &FeedbackConsolidatorJob{
		repoSettings: repoSettings,
		consolidator: consolidator,
	}
}

func (j *FeedbackConsolidatorJob) Run() {
	ctx := context.Background()

	repos, err := j.repoSettings.ListEnabled(ctx)
	if err != nil {
		slog.Error("list enabled repos", "error", err)
		return
	}

	consolidated := 0
	for _, repo := range repos {
		if err := j.consolidator.ConsolidateProject(ctx, repo.GitLabProjectID); err != nil {
			slog.Error("consolidate project", "project_id", repo.GitLabProjectID, "error", err)
			continue
		}
		consolidated++
	}

	slog.Info("feedback consolidation completed", "repos_processed", consolidated)
}
