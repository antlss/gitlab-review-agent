package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

// ─── read_file ──────────────────────────────────────────────────────────────────

type ReadFileTool struct {
	rootPath string
	maxKB    int
	maxLines int
	headSHA  string
	gitEnv   []string
}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Read a file's contents. Use start_line and end_line to read specific sections. Files larger than the limit will be truncated."
}
func (t *ReadFileTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":       map[string]any{"type": "string", "description": "File path relative to repo root"},
			"start_line": map[string]any{"type": "integer", "description": "Start line (1-based, optional)"},
			"end_line":   map[string]any{"type": "integer", "description": "End line (1-based, optional)"},
		},
		"required": []string{"path"},
	}
}

func (t *ReadFileTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	path, _ := input["path"].(string)
	if path == "" {
		return toolError("path is required"), nil
	}

	repoPath, err := cleanRepoPath(t.rootPath, path)
	if err != nil {
		return toolError(err.Error()), nil
	}

	size, err := gitBlobSize(ctx, t.rootPath, t.headSHA, repoPath, t.gitEnv)
	if err != nil {
		return toolError(fmt.Sprintf("file not found: %s", path)), nil
	}
	if size > int64(t.maxKB)*1024 {
		return toolError(fmt.Sprintf("file too large: %d KB (max %d KB)", size/1024, t.maxKB)), nil
	}

	content, err := gitShowFile(ctx, t.rootPath, t.headSHA, repoPath, t.gitEnv)
	if err != nil {
		return toolError(err.Error()), nil
	}

	startLine := domain.GetIntOr(input, "start_line", 1)
	endLine := domain.GetIntOr(input, "end_line", 0)
	allLines := strings.Split(string(content), "\n")

	lines := make([]string, 0, min(len(allLines), t.maxLines))
	for i, line := range allLines {
		lineNum := i + 1
		if lineNum < startLine {
			continue
		}
		if endLine > 0 && lineNum > endLine {
			break
		}
		lines = append(lines, fmt.Sprintf("%d: %s", lineNum, line))
		if len(lines) >= t.maxLines {
			lines = append(lines, fmt.Sprintf("... (truncated at %d lines)", t.maxLines))
			break
		}
	}

	return &domain.ToolResult{Content: strings.Join(lines, "\n")}, nil
}

// ─── get_multi_diff ─────────────────────────────────────────────────────────────

type GetMultiDiffTool struct {
	rootPath  string
	diffFiles []domain.DiffFile
	maxFiles  int
	maxKB     int
	baseSHA   string
	headSHA   string
	gitEnv    []string // inherited git environment for token/config injection
}

func (t *GetMultiDiffTool) Name() string { return "get_multi_diff" }
func (t *GetMultiDiffTool) Description() string {
	return "Get the unified diff for one or more files in the current MR. Returns the actual diff content showing changes."
}
func (t *GetMultiDiffTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "File paths to get diffs for"},
		},
		"required": []string{"paths"},
	}
}

func (t *GetMultiDiffTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	pathsRaw, _ := input["paths"].([]any)
	if len(pathsRaw) == 0 {
		return toolError("paths is required"), nil
	}
	if len(pathsRaw) > t.maxFiles {
		return toolError(fmt.Sprintf("too many files: %d (max %d)", len(pathsRaw), t.maxFiles)), nil
	}

	var result strings.Builder
	for _, p := range pathsRaw {
		path, _ := p.(string)
		if path == "" {
			continue
		}
		var found bool
		for _, df := range t.diffFiles {
			if df.Path == path {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(&result, "--- %s: not in MR diff ---\n", path)
			continue
		}

		cmd := exec.CommandContext(ctx, "git", "diff", t.baseSHA+".."+t.headSHA, "--", path)
		cmd.Dir = t.rootPath
		if len(t.gitEnv) > 0 {
			cmd.Env = t.gitEnv
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Fprintf(&result, "--- %s: error getting diff ---\n", path)
			continue
		}
		fmt.Fprintf(&result, "--- %s ---\n%s\n", path, string(out))
	}

	content := result.String()
	if len(content) > t.maxKB*1024 {
		content = content[:t.maxKB*1024] + "\n... (truncated)"
	}
	return &domain.ToolResult{Content: content}, nil
}

// ─── search_code ────────────────────────────────────────────────────────────────

type SearchCodeTool struct {
	rootPath   string
	maxResults int
	headSHA    string
	gitEnv     []string
}

func (t *SearchCodeTool) Name() string { return "search_code" }
func (t *SearchCodeTool) Description() string {
	return "Search for a pattern in the codebase using git grep. Returns matching lines with file paths and line numbers."
}
func (t *SearchCodeTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern":        map[string]any{"type": "string", "description": "Search pattern (grep-compatible regex)"},
			"file_pattern":   map[string]any{"type": "string", "description": "File glob pattern to filter (e.g., '*.go')"},
			"case_sensitive": map[string]any{"type": "boolean", "description": "Case sensitive search (default true)"},
		},
		"required": []string{"pattern"},
	}
}

func (t *SearchCodeTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	pattern, _ := input["pattern"].(string)
	if pattern == "" {
		return toolError("pattern is required"), nil
	}

	for _, ch := range []string{";", "|", "&", "$", "`", "(", ")", "{", "}", "<", ">"} {
		if strings.Contains(pattern, ch) {
			return toolError("pattern contains unsafe characters"), nil
		}
	}

	caseSensitive := true
	if cs, ok := input["case_sensitive"].(bool); ok {
		caseSensitive = cs
	}

	args := []string{"grep", "-n", "-E", "--max-count", strconv.Itoa(t.maxResults)}
	if !caseSensitive {
		args = append(args, "-i")
	}
	args = append(args, "-e", pattern, t.headSHA)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = t.rootPath
	if len(t.gitEnv) > 0 {
		cmd.Env = t.gitEnv
	}
	out, _ := cmd.CombinedOutput()

	content := normalizeGitGrepOutput(string(out), t.headSHA)
	if fp, ok := input["file_pattern"].(string); ok && fp != "" {
		content = filterGitGrepOutputByPattern(content, fp)
	}
	content = limitOutputLines(strings.TrimSpace(content), t.maxResults, "... (showing first %d results)")
	if content == "" {
		content = "No matches found."
	}
	return &domain.ToolResult{Content: content}, nil
}

// ─── read_multi_file ────────────────────────────────────────────────────────────

type ReadMultiFileTool struct {
	rootPath  string
	maxFiles  int
	perFileKB int
	maxLines  int
	headSHA   string
	gitEnv    []string
}

func (t *ReadMultiFileTool) Name() string { return "read_multi_file" }
func (t *ReadMultiFileTool) Description() string {
	return "Read multiple files at once. More efficient than multiple read_file calls."
}
func (t *ReadMultiFileTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "File paths to read"},
		},
		"required": []string{"paths"},
	}
}

func (t *ReadMultiFileTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	pathsRaw, _ := input["paths"].([]any)
	if len(pathsRaw) == 0 {
		return toolError("paths is required"), nil
	}
	if len(pathsRaw) > t.maxFiles {
		return toolError(fmt.Sprintf("too many files: %d (max %d)", len(pathsRaw), t.maxFiles)), nil
	}

	var result strings.Builder
	for _, p := range pathsRaw {
		path, _ := p.(string)
		if path == "" {
			continue
		}
		repoPath, err := cleanRepoPath(t.rootPath, path)
		if err != nil {
			fmt.Fprintf(&result, "--- %s: %s ---\n", path, err.Error())
			continue
		}

		size, err := gitBlobSize(ctx, t.rootPath, t.headSHA, repoPath, t.gitEnv)
		if err != nil {
			fmt.Fprintf(&result, "--- %s: not found ---\n", path)
			continue
		}
		if size > int64(t.perFileKB)*1024 {
			fmt.Fprintf(&result, "--- %s: too large (%d KB) ---\n", path, size/1024)
			continue
		}

		content, err := gitShowFile(ctx, t.rootPath, t.headSHA, repoPath, t.gitEnv)
		if err != nil {
			fmt.Fprintf(&result, "--- %s: read error ---\n", path)
			continue
		}

		lines := strings.Split(string(content), "\n")
		if len(lines) > t.maxLines {
			lines = lines[:t.maxLines]
			lines = append(lines, fmt.Sprintf("... (truncated at %d lines)", t.maxLines))
		}
		fmt.Fprintf(&result, "--- %s ---\n%s\n\n", path, strings.Join(lines, "\n"))
	}

	return &domain.ToolResult{Content: result.String()}, nil
}

// ─── list_dir ───────────────────────────────────────────────────────────────────

type ListDirTool struct {
	rootPath string
	headSHA  string
	gitEnv   []string
}

func (t *ListDirTool) Name() string { return "list_dir" }
func (t *ListDirTool) Description() string {
	return "List directory contents in a tree view, up to depth 3. Useful for understanding project structure."
}
func (t *ListDirTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "Directory path relative to repo root (default '.')"},
		},
	}
}

func (t *ListDirTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	path, _ := input["path"].(string)
	if path == "" {
		path = "."
	}

	prefix, err := cleanRepoDir(t.rootPath, path)
	if err != nil {
		return toolError(err.Error()), nil
	}

	files, err := gitListFiles(ctx, t.rootPath, t.headSHA, prefix, t.gitEnv)
	if err != nil {
		return toolError(err.Error()), nil
	}
	if len(files) == 0 {
		return &domain.ToolResult{Content: "(empty)"}, nil
	}
	return &domain.ToolResult{Content: renderTree(files, prefix, 3)}, nil
}

// ─── get_symbol_definition ──────────────────────────────────────────────────────

type GetSymbolDefinitionTool struct {
	rootPath   string
	maxResults int
	headSHA    string
	gitEnv     []string
}

func (t *GetSymbolDefinitionTool) Name() string { return "get_symbol_definition" }
func (t *GetSymbolDefinitionTool) Description() string {
	return "Search for a symbol definition (function, class, struct, interface, type) across the codebase. Language-aware patterns."
}
func (t *GetSymbolDefinitionTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol":   map[string]any{"type": "string", "description": "Symbol name to find"},
			"language": map[string]any{"type": "string", "description": "Programming language (go, typescript, python, java, rust)"},
		},
		"required": []string{"symbol"},
	}
}

func (t *GetSymbolDefinitionTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	symbol, _ := input["symbol"].(string)
	if symbol == "" {
		return toolError("symbol is required"), nil
	}

	lang, _ := input["language"].(string)
	patterns := definitionPatterns(symbol, lang)

	var lines []string
	for _, pattern := range patterns {
		cmd := exec.CommandContext(ctx, "git", "grep", "-n", "-E", "--max-count", strconv.Itoa(t.maxResults), "-e", pattern, t.headSHA)
		cmd.Dir = t.rootPath
		if len(t.gitEnv) > 0 {
			cmd.Env = t.gitEnv
		}
		out, _ := cmd.CombinedOutput()
		for _, line := range strings.Split(normalizeGitGrepOutput(string(out), t.headSHA), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lines = append(lines, line)
			if len(lines) >= t.maxResults {
				return &domain.ToolResult{Content: strings.Join(lines, "\n")}, nil
			}
		}
	}

	if len(lines) == 0 {
		return &domain.ToolResult{Content: fmt.Sprintf("No definition found for symbol '%s'", symbol)}, nil
	}
	return &domain.ToolResult{Content: strings.Join(lines, "\n")}, nil
}

// ─── get_git_log ────────────────────────────────────────────────────────────────

type GetGitLogTool struct {
	rootPath string
	baseSHA  string
	headSHA  string
}

func (t *GetGitLogTool) Name() string { return "get_git_log" }
func (t *GetGitLogTool) Description() string {
	return "Get the commit history for this MR (commits between base and head). Shows commit messages and authors to understand the intent of the changes."
}
func (t *GetGitLogTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"max_commits": map[string]any{"type": "integer", "description": "Maximum number of commits to show (default 20)"},
		},
	}
}

func (t *GetGitLogTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	maxCommits := domain.GetIntOr(input, "max_commits", 20)
	if maxCommits <= 0 || maxCommits > 100 {
		maxCommits = 20
	}

	cmd := exec.CommandContext(ctx, "git", "log",
		"--no-merges",
		fmt.Sprintf("--max-count=%d", maxCommits),
		"--format=%h %s (%an, %ar)",
		t.baseSHA+".."+t.headSHA,
	)
	cmd.Dir = t.rootPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return toolError("git log failed: " + string(out)), nil
	}

	content := strings.TrimSpace(string(out))
	if content == "" {
		content = "No commits found between base and head."
	}
	return &domain.ToolResult{Content: content}, nil
}

// ─── get_file_outline ───────────────────────────────────────────────────────────

type GetFileOutlineTool struct {
	rootPath   string
	maxResults int
	headSHA    string
	gitEnv     []string
}

func (t *GetFileOutlineTool) Name() string { return "get_file_outline" }
func (t *GetFileOutlineTool) Description() string {
	return "Get a structural outline of a file (top-level functions, types, classes) without reading full content. More token-efficient than read_file for understanding file structure."
}
func (t *GetFileOutlineTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "File path relative to repo root"},
		},
		"required": []string{"path"},
	}
}

func (t *GetFileOutlineTool) Execute(ctx context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	path, _ := input["path"].(string)
	if path == "" {
		return toolError("path is required"), nil
	}

	repoPath, err := cleanRepoPath(t.rootPath, path)
	if err != nil {
		return toolError(err.Error()), nil
	}

	content, err := gitShowFile(ctx, t.rootPath, t.headSHA, repoPath, t.gitEnv)
	if err != nil {
		return toolError(fmt.Sprintf("file not found: %s", path)), nil
	}

	re := regexp.MustCompile(outlinePattern(filepath.Ext(repoPath)))
	var lines []string
	for i, line := range strings.Split(string(content), "\n") {
		if re.MatchString(line) {
			lines = append(lines, fmt.Sprintf("%d:%s", i+1, line))
			if len(lines) >= t.maxResults {
				lines = append(lines, fmt.Sprintf("... (truncated at %d results)", t.maxResults))
				break
			}
		}
	}

	if len(lines) == 0 {
		return &domain.ToolResult{Content: fmt.Sprintf("No top-level definitions found in %s", path)}, nil
	}
	return &domain.ToolResult{Content: strings.Join(lines, "\n")}, nil
}

func outlinePattern(ext string) string {
	switch ext {
	case ".go":
		return `^(func|type|var|const)\b`
	case ".ts", ".tsx":
		return `^(export\s+)?(async\s+)?(function|class|interface|type|enum|const)\b`
	case ".js", ".jsx":
		return `^(export\s+)?(async\s+)?(function|class|const)\b`
	case ".py":
		return `^(def|class|async def)\s+`
	case ".java":
		return `^\s*(public|private|protected|static).*\s+(class|interface|enum|void)\b`
	case ".rs":
		return `^(pub\s+)?(fn|struct|enum|trait|impl|type|const|mod)\b`
	case ".rb":
		return `^(def|class|module)\s+`
	case ".php":
		return `^(function|class|interface|trait|abstract)\s+`
	case ".cs":
		return `^\s*(public|private|protected|internal|static).*\s+(class|interface|struct|enum|void)\b`
	default:
		return `^(func|function|def|class|interface|type|struct|enum)\b`
	}
}

// ─── save_note ──────────────────────────────────────────────────────────────────

type SaveNoteTool struct {
	acc *NoteAccumulator
}

func (t *SaveNoteTool) Name() string { return "save_note" }
func (t *SaveNoteTool) Description() string {
	return "Save a finding or important insight to persistent memory. Notes survive context compression. Use this to record: potential bugs found, important context about a file, or inter-file relationships you want to remember across iterations."
}
func (t *SaveNoteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"note": map[string]any{"type": "string", "description": "The finding or insight to remember (e.g., 'auth.go:42 — missing token expiry check', 'UserService depends on CacheService for auth bypass risk')"},
		},
		"required": []string{"note"},
	}
}

func (t *SaveNoteTool) Execute(_ context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	note, _ := input["note"].(string)
	if note == "" {
		return toolError("note is required"), nil
	}
	t.acc.Add(note)
	return &domain.ToolResult{Content: "Note saved."}, nil
}

// ─── Helpers ────────────────────────────────────────────────────────────────────

func cleanRepoPath(root, path string) (string, error) {
	abs, err := securePath(root, path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func cleanRepoDir(root, path string) (string, error) {
	rel, err := cleanRepoPath(root, path)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", nil
	}
	return strings.TrimSuffix(rel, "/"), nil
}

func gitCommand(ctx context.Context, rootPath string, gitEnv []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = rootPath
	if len(gitEnv) > 0 {
		cmd.Env = gitEnv
	}
	return cmd.CombinedOutput()
}

func gitShowFile(ctx context.Context, rootPath, sha, path string, gitEnv []string) ([]byte, error) {
	return gitCommand(ctx, rootPath, gitEnv, "show", fmt.Sprintf("%s:%s", sha, path))
}

func gitBlobSize(ctx context.Context, rootPath, sha, path string, gitEnv []string) (int64, error) {
	out, err := gitCommand(ctx, rootPath, gitEnv, "cat-file", "-s", fmt.Sprintf("%s:%s", sha, path))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

func gitListFiles(ctx context.Context, rootPath, sha, dir string, gitEnv []string) ([]string, error) {
	args := []string{"ls-tree", "-r", "--name-only", sha}
	if dir != "" {
		args = append(args, dir)
	}
	out, err := gitCommand(ctx, rootPath, gitEnv, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func normalizeGitGrepOutput(content, sha string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	prefix := sha + ":"
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, prefix)
	}
	return strings.Join(lines, "\n")
}

func filterGitGrepOutputByPattern(content, pattern string) string {
	if content == "" || pattern == "" {
		return content
	}
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		if matchesFilePattern(pattern, parts[0]) {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func matchesFilePattern(pattern, path string) bool {
	if matched, _ := filepath.Match(pattern, path); matched {
		return true
	}
	if matched, _ := filepath.Match(pattern, filepath.Base(path)); matched {
		return true
	}
	return false
}

func limitOutputLines(content string, maxLines int, suffixFormat string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, fmt.Sprintf(suffixFormat, maxLines))
	}
	return strings.Join(lines, "\n")
}

func definitionPatterns(symbol, lang string) []string {
	switch strings.ToLower(lang) {
	case "go":
		return []string{
			fmt.Sprintf(`func\s+(\([^)]+\)\s+)?%s\s*\(`, symbol),
			fmt.Sprintf(`type\s+%s\s+(struct|interface)`, symbol),
			fmt.Sprintf(`var\s+%s\s+`, symbol),
			fmt.Sprintf(`const\s+%s\s+`, symbol),
		}
	case "typescript", "javascript", "ts", "js":
		return []string{
			fmt.Sprintf(`(function|const|let|var|class|interface|type|enum)\s+%s`, symbol),
			fmt.Sprintf(`export\s+(default\s+)?(function|const|let|var|class|interface|type|enum)\s+%s`, symbol),
		}
	case "python":
		return []string{
			fmt.Sprintf(`(def|class)\s+%s`, symbol),
		}
	case "java":
		return []string{
			fmt.Sprintf(`(class|interface|enum)\s+%s`, symbol),
			fmt.Sprintf(`(public|private|protected|static).*\s+%s\s*\(`, symbol),
		}
	default:
		return []string{
			fmt.Sprintf(`(func|function|def|class|interface|type|struct|enum|const|var|let)\s+%s`, symbol),
		}
	}
}

type treeNode struct {
	name     string
	isDir    bool
	children map[string]*treeNode
}

func renderTree(files []string, prefix string, maxDepth int) string {
	root := &treeNode{isDir: true, children: make(map[string]*treeNode)}
	for _, file := range files {
		rel := strings.TrimPrefix(file, prefix)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		node := root
		for i, part := range parts {
			isDir := i < len(parts)-1
			child, ok := node.children[part]
			if !ok {
				child = &treeNode{name: part, isDir: isDir, children: make(map[string]*treeNode)}
				node.children[part] = child
			}
			node = child
		}
	}

	var sb strings.Builder
	renderTreeNode(&sb, root, "", 0, maxDepth)
	return strings.TrimSpace(sb.String())
}

func renderTreeNode(sb *strings.Builder, node *treeNode, prefix string, depth, maxDepth int) {
	if depth >= maxDepth {
		return
	}
	keys := make([]string, 0, len(node.children))
	for name := range node.children {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for i, name := range keys {
		child := node.children[name]
		connector := "├── "
		nextPrefix := prefix + "│   "
		if i == len(keys)-1 {
			connector = "└── "
			nextPrefix = prefix + "    "
		}
		display := child.name
		if child.isDir {
			display += "/"
		}
		sb.WriteString(prefix + connector + display + "\n")
		if child.isDir {
			renderTreeNode(sb, child, nextPrefix, depth+1, maxDepth)
		}
	}
}

func securePath(root, path string) (string, error) {
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path traversal not allowed: %s", path)
	}
	abs := filepath.Join(root, cleaned)
	if !strings.HasPrefix(abs, root+string(filepath.Separator)) && abs != root {
		return "", fmt.Errorf("path traversal not allowed: %s", path)
	}
	return abs, nil
}

func toolError(msg string) *domain.ToolResult {
	return &domain.ToolResult{Error: &msg}
}
