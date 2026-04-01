package di

import (
	"github.com/samber/do/v2"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/domain"
	"github.com/antlss/gitlab-review-agent/internal/pkg/git"
	"github.com/antlss/gitlab-review-agent/internal/pkg/gitlab"
	"github.com/antlss/gitlab-review-agent/internal/pkg/queue"
	"github.com/antlss/gitlab-review-agent/internal/pkg/store"
)

var InfraPackage = do.Package(
	do.Lazy(provideStores),
	do.Lazy(provideGitLabClient),
	do.Lazy(provideGitManager),
	do.Lazy(provideJobQueue),
)

func provideStores(i do.Injector) (*store.Stores, error) {
	cfg := do.MustInvoke[*config.Config](i)
	stores, err := store.New(cfg.Store)
	if err != nil {
		return nil, err
	}

	// Register individual store interfaces for direct injection
	do.ProvideValue[domain.RepositorySettingsStore](i, stores.RepoSettings)
	do.ProvideValue[domain.ReviewJobStore](i, stores.ReviewJobs)
	do.ProvideValue[domain.ReplyJobStore](i, stores.ReplyJobs)
	do.ProvideValue[domain.FeedbackStore](i, stores.Feedbacks)
	do.ProvideValue[domain.ReviewRecordStore](i, stores.ReviewRecords)

	return stores, nil
}

func provideGitLabClient(i do.Injector) (domain.GitLabClient, error) {
	cfg := do.MustInvoke[*config.Config](i)
	return gitlab.NewClient(cfg.GitLab.BaseURL, cfg.GitLab.Token), nil
}

func provideGitManager(i do.Injector) (*git.Manager, error) {
	cfg := do.MustInvoke[*config.Config](i)
	return git.NewManager(cfg.Git.ReposDir, cfg.GitLab.BaseURL, cfg.GitLab.Token), nil
}

func provideJobQueue(_ do.Injector) (*queue.Queue, error) {
	return queue.New(), nil
}
