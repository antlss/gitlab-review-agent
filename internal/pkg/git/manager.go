package git

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/antlss/gitlab-review-agent/internal/shared"
)

const (
	gitLockTimeout = 2 * time.Minute
	cloneMaxRetries = 2
	cloneRetryDelay = 3 * time.Second
)

type Manager struct {
	reposDir    string
	gitlabURL   string
	gitlabToken string
	lockMu      sync.Mutex
	gitLocks    map[int64]bool
}

func NewManager(reposDir, gitlabURL, gitlabToken string) *Manager {
	return &Manager{
		reposDir:    reposDir,
		gitlabURL:   gitlabURL,
		gitlabToken: gitlabToken,
		gitLocks:    make(map[int64]bool),
	}
}

func (m *Manager) RepoPath(projectID int64) string {
	return filepath.Join(m.reposDir, fmt.Sprintf("%d", projectID))
}

// AcquireGitLock acquires an in-memory lock for git operations on a project.
func (m *Manager) AcquireGitLock(ctx context.Context, projectID int64) error {
	deadline := time.Now().Add(gitLockTimeout)
	backoff := 200 * time.Millisecond

	for time.Now().Before(deadline) {
		m.lockMu.Lock()
		if !m.gitLocks[projectID] {
			m.gitLocks[projectID] = true
			m.lockMu.Unlock()
			return nil
		}
		m.lockMu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Second)
	}
	return fmt.Errorf("git_lock_timeout for project %d", projectID)
}

// ReleaseGitLock releases the git lock.
func (m *Manager) ReleaseGitLock(_ context.Context, projectID int64) {
	m.lockMu.Lock()
	delete(m.gitLocks, projectID)
	m.lockMu.Unlock()
}

// FetchAndCheckout clones or fetches the repo, then checks out the given SHA.
func (m *Manager) FetchAndCheckout(ctx context.Context, projectID int64, projectPath, headSHA string) error {
	repoPath := m.RepoPath(projectID)

	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		cloneURL := fmt.Sprintf("%s/%s.git", m.gitlabURL, projectPath)
		if err := m.cloneWithRetry(ctx, cloneURL, repoPath); err != nil {
			return fmt.Errorf("git clone: %w", err)
		}
	} else {
		if err := m.runGit(ctx, repoPath, "fetch", "origin", "--prune"); err != nil {
			// Retry once
			if err2 := m.runGit(ctx, repoPath, "fetch", "origin", "--prune"); err2 != nil {
				return fmt.Errorf("git fetch failed after retry: %w", err2)
			}
		}
	}

	// Verify head SHA exists
	if err := m.runGit(ctx, repoPath, "cat-file", "-t", headSHA); err != nil {
		// Retry fetch
		_ = m.runGit(ctx, repoPath, "fetch", "origin", "--prune")
		if err2 := m.runGit(ctx, repoPath, "cat-file", "-t", headSHA); err2 != nil {
			return fmt.Errorf("sha_not_found: %s", headSHA)
		}
	}

	// Checkout
	if err := m.runGit(ctx, repoPath, "checkout", "--force", headSHA); err != nil {
		return fmt.Errorf("checkout_failed: %w", err)
	}

	return nil
}

// IsAncestor checks if beforeSHA is an ancestor of headSHA (force-push detection).
func (m *Manager) IsAncestor(ctx context.Context, projectID int64, beforeSHA, headSHA string) (bool, error) {
	repoPath := m.RepoPath(projectID)
	err := m.runGit(ctx, repoPath, "merge-base", "--is-ancestor", beforeSHA, headSHA)
	if err != nil {
		// Non-zero exit means not an ancestor
		return false, nil
	}
	return true, nil
}

// RevParse resolves a ref to a SHA.
func (m *Manager) RevParse(ctx context.Context, projectID int64, ref string) (string, error) {
	repoPath := m.RepoPath(projectID)
	out, err := m.runGitOutput(ctx, repoPath, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(out), nil
}

// SHAExists checks if a SHA exists in the repo.
func (m *Manager) SHAExists(ctx context.Context, projectID int64, sha string) bool {
	repoPath := m.RepoPath(projectID)
	return m.runGit(ctx, repoPath, "cat-file", "-t", sha) == nil
}

// Diff returns diff files between base and head.
func (m *Manager) Diff(ctx context.Context, projectID int64, baseSHA, headSHA string) ([]shared.DiffFile, error) {
	repoPath := m.RepoPath(projectID)

	// name-status
	nameStatus, err := m.runGitOutput(ctx, repoPath, "diff", "--name-status", baseSHA+".."+headSHA)
	if err != nil {
		return nil, fmt.Errorf("diff name-status: %w", err)
	}

	// numstat
	numstat, err := m.runGitOutput(ctx, repoPath, "diff", "--numstat", baseSHA+".."+headSHA)
	if err != nil {
		return nil, fmt.Errorf("diff numstat: %w", err)
	}

	// Parse name-status
	statusMap := make(map[string]string)
	oldPathMap := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(nameStatus), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status := string(parts[0][0]) // First char: A, M, D, R
		path := parts[len(parts)-1]
		statusMap[path] = status
		if status == "R" && len(parts) >= 3 {
			oldPathMap[parts[2]] = parts[1]
		}
	}

	// Parse numstat
	statMap := make(map[string][2]int) // added, removed
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		removed, _ := strconv.Atoi(parts[1])
		path := parts[2]
		statMap[path] = [2]int{added, removed}
	}

	var files []shared.DiffFile
	for path, status := range statusMap {
		stats := statMap[path]
		oldPath := path
		if op, ok := oldPathMap[path]; ok {
			oldPath = op
		}

		addedLines, _ := m.getAddedLines(ctx, repoPath, baseSHA, headSHA, path)

		files = append(files, shared.DiffFile{
			Path:         path,
			OldPath:      oldPath,
			Status:       status,
			LinesAdded:   stats[0],
			LinesRemoved: stats[1],
			AddedLines:   addedLines,
		})
	}

	return files, nil
}

// DiffFile returns the raw diff output for a single file between base and head.
func (m *Manager) DiffFile(ctx context.Context, projectID int64, baseSHA, headSHA, filePath string) (string, error) {
	repoPath := m.RepoPath(projectID)
	return m.runGitOutput(ctx, repoPath, "diff", baseSHA+".."+headSHA, "--", filePath)
}

// getAddedLines returns line numbers of added lines in a file diff.
func (m *Manager) getAddedLines(ctx context.Context, repoPath, baseSHA, headSHA, filePath string) ([]int, error) {
	out, err := m.runGitOutput(ctx, repoPath, "diff", "-U0", baseSHA+".."+headSHA, "--", filePath)
	if err != nil {
		return nil, err
	}

	var lines []int
	re := regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		matches := re.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		start, _ := strconv.Atoi(matches[1])
		count := 1
		if matches[2] != "" {
			count, _ = strconv.Atoi(matches[2])
		}
		for i := 0; i < count; i++ {
			lines = append(lines, start+i)
		}
	}
	return lines, nil
}

// CloneOrFetch clones the repository if absent, or fetches updates if it
// already exists locally. It is designed for bulk pre-warm operations; the
// caller should set an appropriate per-repo deadline on ctx.
//
// Returns cloned=true when a fresh clone was performed, false when an
// existing repo was updated (or was already up to date after a failed fetch).
func (m *Manager) CloneOrFetch(ctx context.Context, projectID int64, cloneURL string) (cloned bool, err error) {
	repoPath := m.RepoPath(projectID)
	gitDir := filepath.Join(repoPath, ".git")

	if _, statErr := os.Stat(gitDir); statErr == nil {
		// Repo exists — fetch updates with retry
		if fetchErr := m.fetchWithRetry(ctx, repoPath); fetchErr != nil {
			return false, fmt.Errorf("fetch: %w", fetchErr)
		}
		return false, nil
	}

	// If the directory exists but has no .git, it is corrupt/partial — remove it
	if _, statErr := os.Stat(repoPath); statErr == nil {
		_ = os.RemoveAll(repoPath)
	}

	if cloneErr := m.cloneWithRetry(ctx, cloneURL, repoPath); cloneErr != nil {
		return false, fmt.Errorf("clone: %w", cloneErr)
	}
	return true, nil
}

// fetchWithRetry runs git fetch with exponential backoff to tolerate transient
// network failures during bulk operations.
func (m *Manager) fetchWithRetry(ctx context.Context, repoPath string) error {
	const maxRetries = 3
	backoff := 5 * time.Second
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := m.runGit(ctx, repoPath, "fetch", "origin", "--prune"); err != nil {
			lastErr = err
			slog.Warn("git fetch failed, retrying",
				"attempt", attempt,
				"max_retries", maxRetries,
				"path", repoPath,
				"error", err,
			)
			if attempt < maxRetries {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
				backoff = min(backoff*3, 45*time.Second)
			}
			continue
		}
		return nil
	}
	return lastErr
}

// cloneWithRetry clones a repo with retry logic and cleanup on failure.
// Uses --filter=blob:none for partial clone (downloads blobs on demand) to
// reduce initial transfer size and avoid HTTP transport failures on large repos.
func (m *Manager) cloneWithRetry(ctx context.Context, cloneURL, repoPath string) error {
	var lastErr error
	for attempt := 1; attempt <= cloneMaxRetries; attempt++ {
		if err := m.runGit(ctx, "", "clone", "--no-checkout", "--filter=blob:none", cloneURL, repoPath); err != nil {
			lastErr = err
			// Clean up partial clone directory to avoid corrupted state
			_ = os.RemoveAll(repoPath)
			slog.Warn("git clone failed, retrying",
				"attempt", attempt,
				"max_retries", cloneMaxRetries,
				"error", err,
			)
			if attempt < cloneMaxRetries {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(cloneRetryDelay):
				}
			}
			continue
		}
		return nil
	}
	return lastErr
}

// gitEnv returns environment variables for git commands that inject the GitLab
// token, http buffer, and HTTP/1.1 settings via GIT_CONFIG environment variables.
// HTTP/1.1 is forced to avoid HTTP/2 stream errors with reverse proxies.
func (m *Manager) gitEnv() []string {
	if m.gitlabToken == "" {
		return append(os.Environ(),
			"GIT_CONFIG_COUNT=2",
			"GIT_CONFIG_KEY_0=http.postBuffer",
			"GIT_CONFIG_VALUE_0=524288000",
			"GIT_CONFIG_KEY_1=http.version",
			"GIT_CONFIG_VALUE_1=HTTP/1.1",
		)
	}
	return append(os.Environ(),
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		fmt.Sprintf("GIT_CONFIG_VALUE_0=PRIVATE-TOKEN: %s", m.gitlabToken),
		"GIT_CONFIG_KEY_1=http.postBuffer",
		"GIT_CONFIG_VALUE_1=524288000",
		"GIT_CONFIG_KEY_2=http.version",
		"GIT_CONFIG_VALUE_2=HTTP/1.1",
	)
}

func (m *Manager) runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = m.gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), string(out), err)
	}
	return nil
}

func (m *Manager) runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = m.gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), string(out), err)
	}
	return string(out), nil
}
