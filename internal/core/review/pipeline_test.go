package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antlss/gitlab-review-agent/internal/domain"
	"github.com/antlss/gitlab-review-agent/internal/pkg/git"
)

type stubReviewRecordStore struct {
	record *domain.ReviewRecord
}

func (s stubReviewRecordStore) GetLastCompleted(_ context.Context, _, _ int64) (*domain.ReviewRecord, error) {
	return s.record, nil
}

func (s stubReviewRecordStore) Upsert(_ context.Context, _ *domain.ReviewRecord) error {
	return nil
}

func TestDetermineBaseSHAFallsBackWhenPreviousReviewHeadWasForcePushedAway(t *testing.T) {
	t.Helper()

	reposDir := t.TempDir()
	projectID := int64(123)
	repoPath := filepath.Join(reposDir, "123")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	runGit(t, repoPath, "init")
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")

	writeFile(t, repoPath, "service.txt", "base\n")
	runGit(t, repoPath, "add", "service.txt")
	runGit(t, repoPath, "commit", "-m", "base")
	runGit(t, repoPath, "branch", "-M", "main")
	targetSHA := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	runGit(t, repoPath, "update-ref", "refs/remotes/origin/main", targetSHA)

	writeFile(t, repoPath, "service.txt", "reviewed\n")
	runGit(t, repoPath, "add", "service.txt")
	runGit(t, repoPath, "commit", "-m", "reviewed")
	reviewedSHA := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	runGit(t, repoPath, "checkout", "--orphan", "rewrite")
	runGit(t, repoPath, "rm", "-rf", ".")
	writeFile(t, repoPath, "service.txt", "rewritten history\n")
	runGit(t, repoPath, "add", "service.txt")
	runGit(t, repoPath, "commit", "-m", "rewritten")
	currentSHA := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	pipeline := &Pipeline{
		recordStore: stubReviewRecordStore{
			record: &domain.ReviewRecord{HeadSHA: reviewedSHA},
		},
		gitManager: git.NewManager(reposDir, "", ""),
	}

	job := &domain.ReviewJob{
		GitLabProjectID: projectID,
		MrIID:           7,
		HeadSHA:         currentSHA,
		TargetBranch:    "main",
	}

	baseSHA, err := pipeline.determineBaseSHA(context.Background(), job, false)
	if err != nil {
		t.Fatalf("determineBaseSHA() error = %v", err)
	}
	if baseSHA != targetSHA {
		t.Fatalf("determineBaseSHA() = %s, want %s", baseSHA, targetSHA)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

func writeFile(t *testing.T, repoPath, relPath, content string) {
	t.Helper()

	path := filepath.Join(repoPath, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", relPath, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", relPath, err)
	}
}
