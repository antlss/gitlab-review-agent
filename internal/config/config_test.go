package config

import "testing"

func TestLoadAllowsCLIModesWithoutWebhookSettings(t *testing.T) {
	t.Setenv("GITLAB_BASE_URL", "https://gitlab.example.com")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_WEBHOOK_SECRET", "")
	t.Setenv("GITLAB_BOT_USER_ID", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if err := cfg.ValidateForReview(); err == nil {
		t.Fatal("ValidateForReview() expected error, got nil")
	}
	if err := cfg.ValidateForServer(); err == nil {
		t.Fatal("ValidateForServer() expected error, got nil")
	}
}

func TestValidateForServerRequiresWebhookSettings(t *testing.T) {
	t.Setenv("GITLAB_BASE_URL", "https://gitlab.example.com")
	t.Setenv("GITLAB_TOKEN", "token")
	t.Setenv("GITLAB_WEBHOOK_SECRET", "secret")
	t.Setenv("GITLAB_BOT_USER_ID", "42")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if err := cfg.ValidateForReview(); err != nil {
		t.Fatalf("ValidateForReview() error = %v", err)
	}
	if err := cfg.ValidateForServer(); err != nil {
		t.Fatalf("ValidateForServer() error = %v", err)
	}
}
