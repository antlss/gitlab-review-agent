// Package store provides a factory for creating store instances
// based on the configured storage driver.
package store

import (
	"fmt"
	"io"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/pkg/store/file"
	"github.com/antlss/gitlab-review-agent/internal/pkg/store/postgres"
	"github.com/antlss/gitlab-review-agent/internal/pkg/store/sqlite"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

// Stores aggregates all store instances and a Close function.
type Stores struct {
	RepoSettings  domain.RepositorySettingsStore
	ReviewJobs    domain.ReviewJobStore
	ReplyJobs     domain.ReplyJobStore
	Feedbacks     domain.FeedbackStore
	ReviewRecords domain.ReviewRecordStore
	closer        io.Closer // underlying DB connection (if any)
}

// Close releases any underlying resources (e.g., database connections).
func (s *Stores) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}

// New creates all store instances based on the storage driver in cfg.
func New(cfg config.StoreConfig) (*Stores, error) {
	switch cfg.Driver {
	case "postgres":
		return newPostgresStores(cfg)
	case "sqlite":
		return newSQLiteStores(cfg)
	case "file":
		return newFileStores(cfg)
	default:
		return nil, fmt.Errorf("unsupported store driver: %s", cfg.Driver)
	}
}

func newPostgresStores(cfg config.StoreConfig) (*Stores, error) {
	db, err := postgres.Connect(cfg.PostgresURL, cfg.MaxOpenConns, cfg.MaxIdleConns)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}

	return &Stores{
		RepoSettings:  postgres.NewRepositorySettingsStore(db),
		ReviewJobs:    postgres.NewReviewJobStore(db),
		ReplyJobs:     postgres.NewReplyJobStore(db),
		Feedbacks:     postgres.NewFeedbackStore(db),
		ReviewRecords: postgres.NewReviewRecordStore(db),
		closer:        db,
	}, nil
}

func newSQLiteStores(cfg config.StoreConfig) (*Stores, error) {
	db, err := sqlite.Connect(cfg.SQLitePath)
	if err != nil {
		return nil, fmt.Errorf("sqlite connect: %w", err)
	}

	return &Stores{
		RepoSettings:  sqlite.NewRepositorySettingsStore(db),
		ReviewJobs:    sqlite.NewReviewJobStore(db),
		ReplyJobs:     sqlite.NewReplyJobStore(db),
		Feedbacks:     sqlite.NewFeedbackStore(db),
		ReviewRecords: sqlite.NewReviewRecordStore(db),
		closer:        db,
	}, nil
}

func newFileStores(cfg config.StoreConfig) (*Stores, error) {
	repoSettings, err := file.NewRepositorySettingsStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	reviewJobs, err := file.NewReviewJobStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	replyJobs, err := file.NewReplyJobStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	feedbacks, err := file.NewFeedbackStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	reviewRecords, err := file.NewReviewRecordStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	return &Stores{
		RepoSettings:  repoSettings,
		ReviewJobs:    reviewJobs,
		ReplyJobs:     replyJobs,
		Feedbacks:     feedbacks,
		ReviewRecords: reviewRecords,
	}, nil
}
