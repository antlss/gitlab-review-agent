package di

import (
	"github.com/samber/do/v2"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/pkg/logger"
)

var ConfigPackage = do.Package(
	do.Lazy(provideConfig),
)

func provideConfig(i do.Injector) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	logger.Setup(cfg.Log.Level, cfg.Log.Format)
	return cfg, nil
}
