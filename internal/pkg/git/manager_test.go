package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoRecloneReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(t *testing.T, repoPath string)
		reason string
	}{
		{
			name:   "missing git dir",
			setup:  func(_ *testing.T, _ string) {},
			reason: "missing_git_dir",
		},
		{
			name: "normal clone",
			setup: func(t *testing.T, repoPath string) {
				t.Helper()
				writeGitConfig(t, repoPath, `[remote "origin"]
	url = https://example.com/group/project.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`)
			},
			reason: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repoPath := t.TempDir()
			tt.setup(t, repoPath)

			if got := repoRecloneReason(repoPath); got != tt.reason {
				t.Fatalf("repoRecloneReason() = %q, want %q", got, tt.reason)
			}
		})
	}
}

func writeGitConfig(t *testing.T, repoPath, config string) {
	t.Helper()

	gitDir := filepath.Join(repoPath, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", gitDir, err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatalf("WriteFile(config): %v", err)
	}
}
