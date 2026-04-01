package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type RepositorySettingsStore struct {
	db *sqlx.DB
}

func NewRepositorySettingsStore(db *sqlx.DB) *RepositorySettingsStore {
	return &RepositorySettingsStore{db: db}
}

func (s *RepositorySettingsStore) GetByProjectID(ctx context.Context, projectID int64) (*domain.RepositorySettings, error) {
	var rs domain.RepositorySettings
	err := s.db.GetContext(ctx, &rs,
		`SELECT * FROM repository_settings WHERE gitlab_project_id = ?`, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get repository settings: %w", err)
	}
	return &rs, nil
}

func (s *RepositorySettingsStore) GetOrCreate(ctx context.Context, projectID int64, projectPath string) (*domain.RepositorySettings, error) {
	existing, err := s.GetByProjectID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	now := time.Now()
	settings := &domain.RepositorySettings{
		ID:              uuid.New(),
		GitLabProjectID: projectID,
		ProjectPath:     projectPath,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO repository_settings (
			id, gitlab_project_id, project_path,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)`,
		settings.ID.String(), projectID, projectPath,
		now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("auto-create repository settings: %w", err)
	}

	return s.GetByProjectID(ctx, projectID)
}

func (s *RepositorySettingsStore) Upsert(ctx context.Context, settings *domain.RepositorySettings) error {
	now := time.Now()
	if settings.ID == (uuid.UUID{}) {
		settings.ID = uuid.New()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO repository_settings (
			id, gitlab_project_id, project_path,
			model_override, language, framework, custom_prompt,
			exclude_patterns, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gitlab_project_id) DO UPDATE SET
			project_path = excluded.project_path,
			is_archived = 0,
			updated_at = ?`,
		settings.ID.String(), settings.GitLabProjectID, settings.ProjectPath,
		settings.ModelOverride, settings.Language,
		settings.Framework, settings.CustomPrompt, settings.ExcludePatterns,
		now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upsert repository settings: %w", err)
	}
	return nil
}

func (s *RepositorySettingsStore) IncrementFeedbackCount(ctx context.Context, projectID int64, delta int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE repository_settings SET feedback_count = feedback_count + ?, updated_at = ?
		 WHERE gitlab_project_id = ?`, delta, time.Now().Format(time.RFC3339), projectID)
	if err != nil {
		return fmt.Errorf("increment feedback count: %w", err)
	}
	return nil
}

func (s *RepositorySettingsStore) ResetFeedbackCount(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE repository_settings SET feedback_count = 0, updated_at = ?
		 WHERE gitlab_project_id = ?`, time.Now().Format(time.RFC3339), projectID)
	if err != nil {
		return fmt.Errorf("reset feedback count: %w", err)
	}
	return nil
}

func (s *RepositorySettingsStore) UpdateCustomPrompt(ctx context.Context, projectID int64, prompt string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE repository_settings SET custom_prompt = ?, updated_at = ?
		 WHERE gitlab_project_id = ?`, prompt, time.Now().Format(time.RFC3339), projectID)
	if err != nil {
		return fmt.Errorf("update custom prompt: %w", err)
	}
	return nil
}

func (s *RepositorySettingsStore) ListEnabled(ctx context.Context) ([]*domain.RepositorySettings, error) {
	var results []*domain.RepositorySettings
	err := s.db.SelectContext(ctx, &results,
		`SELECT * FROM repository_settings WHERE is_archived = 0`)
	if err != nil {
		return nil, fmt.Errorf("list enabled repos: %w", err)
	}
	return results, nil
}

func (s *RepositorySettingsStore) MarkArchived(ctx context.Context, projectID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE repository_settings SET is_archived = 1, updated_at = ?
		 WHERE gitlab_project_id = ?`, time.Now().Format(time.RFC3339), projectID)
	if err != nil {
		return fmt.Errorf("mark archived: %w", err)
	}
	return nil
}
