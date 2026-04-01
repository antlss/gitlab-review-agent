package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

// ─── read_file ──────────────────────────────────────────────────────────────────

type ReadFileTool struct {
	rootPath string
	maxKB    int
	maxLines int
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

func (t *ReadFileTool) Execute(_ context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	path, _ := input["path"].(string)
	if path == "" {
		return toolError("path is required"), nil
	}

	absPath, err := securePath(t.rootPath, path)
	if err != nil {
		return toolError(err.Error()), nil
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return toolError(fmt.Sprintf("file not found: %s", path)), nil
	}
	if info.IsDir() {
		return toolError("path is a directory, use list_dir instead"), nil
	}
	if info.Size() > int64(t.maxKB)*1024 {
		return toolError(fmt.Sprintf("file too large: %d KB (max %d KB)", info.Size()/1024, t.maxKB)), nil
	}

	file, err := os.Open(absPath)
	if err != nil {
		return toolError(err.Error()), nil
	}
	defer file.Close()

	startLine := domain.GetIntOr(input, "start_line", 1)
	endLine := domain.GetIntOr(input, "end_line", 0)

	var lines []string
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if endLine > 0 && lineNum > endLine {
			break
		}
		lines = append(lines, fmt.Sprintf("%d: %s", lineNum, scanner.Text()))
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
		// Find the diff file info
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

		// Run git diff for this file using the actual MR base..head range
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
}

func (t *SearchCodeTool) Name() string { return "search_code" }
func (t *SearchCodeTool) Description() string {
	return "Search for a pattern in the codebase using grep. Returns matching lines with file paths and line numbers."
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

	// Sanitize: reject shell-dangerous characters in pattern
	for _, ch := range []string{";", "|", "&", "$", "`", "(", ")", "{", "}", "<", ">"} {
		if strings.Contains(pattern, ch) {
			return toolError("pattern contains unsafe characters"), nil
		}
	}

	args := []string{"-rn", "--max-count", strconv.Itoa(t.maxResults)}

	// Default to case-sensitive (grep's natural behavior).
	// Only add -i when the caller explicitly sets case_sensitive=false.
	if cs, ok := input["case_sensitive"].(bool); ok && !cs {
		args = append(args, "-i")
	}

	if fp, ok := input["file_pattern"].(string); ok && fp != "" {
		args = append(args, "--include", fp)
	}

	args = append(args, pattern, ".")

	cmd := exec.CommandContext(ctx, "grep", args...)
	cmd.Dir = t.rootPath
	out, _ := cmd.CombinedOutput() // grep returns exit 1 if no matches

	content := string(out)
	if content == "" {
		content = "No matches found."
	}
	lines := strings.Split(content, "\n")
	if len(lines) > t.maxResults {
		lines = lines[:t.maxResults]
		lines = append(lines, fmt.Sprintf("... (showing first %d results)", t.maxResults))
	}
	return &domain.ToolResult{Content: strings.Join(lines, "\n")}, nil
}

// ─── read_multi_file ────────────────────────────────────────────────────────────

type ReadMultiFileTool struct {
	rootPath  string
	maxFiles  int
	perFileKB int
	maxLines  int
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

func (t *ReadMultiFileTool) Execute(_ context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
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
		absPath, err := securePath(t.rootPath, path)
		if err != nil {
			fmt.Fprintf(&result, "--- %s: %s ---\n", path, err.Error())
			continue
		}
		info, err := os.Stat(absPath)
		if err != nil {
			fmt.Fprintf(&result, "--- %s: not found ---\n", path)
			continue
		}
		if info.Size() > int64(t.perFileKB)*1024 {
			fmt.Fprintf(&result, "--- %s: too large (%d KB) ---\n", path, info.Size()/1024)
			continue
		}

		content, err := os.ReadFile(absPath)
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

func (t *ListDirTool) Execute(_ context.Context, input domain.ToolInput) (*domain.ToolResult, error) {
	path, _ := input["path"].(string)
	if path == "" {
		path = "."
	}

	absPath, err := securePath(t.rootPath, path)
	if err != nil {
		return toolError(err.Error()), nil
	}

	var result strings.Builder
	walkDir(absPath, t.rootPath, "", 0, 3, &result)

	return &domain.ToolResult{Content: result.String()}, nil
}

func walkDir(dir, root, prefix string, depth, maxDepth int, result *strings.Builder) {
	if depth >= maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for i, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		isLast := i == len(entries)-1
		connector := "├── "
		childPrefix := prefix + "│   "
		if isLast {
			connector = "└── "
			childPrefix = prefix + "    "
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		result.WriteString(prefix + connector + name + "\n")
		if entry.IsDir() {
			walkDir(filepath.Join(dir, entry.Name()), root, childPrefix, depth+1, maxDepth, result)
		}
	}
}

// ─── get_symbol_definition ──────────────────────────────────────────────────────

type GetSymbolDefinitionTool struct {
	rootPath   string
	maxResults int
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

	// Build language-specific patterns
	var patterns []string
	switch strings.ToLower(lang) {
	case "go":
		patterns = []string{
			fmt.Sprintf(`func\s+(\([^)]+\)\s+)?%s\s*\(`, symbol),
			fmt.Sprintf(`type\s+%s\s+(struct|interface)`, symbol),
			fmt.Sprintf(`var\s+%s\s+`, symbol),
			fmt.Sprintf(`const\s+%s\s+`, symbol),
		}
	case "typescript", "javascript", "ts", "js":
		patterns = []string{
			fmt.Sprintf(`(function|const|let|var|class|interface|type|enum)\s+%s`, symbol),
			fmt.Sprintf(`export\s+(default\s+)?(function|const|let|var|class|interface|type|enum)\s+%s`, symbol),
		}
	case "python":
		patterns = []string{
			fmt.Sprintf(`(def|class)\s+%s`, symbol),
		}
	case "java":
		patterns = []string{
			fmt.Sprintf(`(class|interface|enum)\s+%s`, symbol),
			fmt.Sprintf(`(public|private|protected|static).*\s+%s\s*\(`, symbol),
		}
	default:
		patterns = []string{
			fmt.Sprintf(`(func|function|def|class|interface|type|struct|enum|const|var|let)\s+%s`, symbol),
		}
	}

	var result strings.Builder
	for _, pattern := range patterns {
		cmd := exec.CommandContext(ctx, "grep", "-rn", "-E", pattern, "--max-count", "20", ".")
		cmd.Dir = t.rootPath
		out, _ := cmd.CombinedOutput()
		if len(out) > 0 {
			result.Write(out)
		}
	}

	content := result.String()
	if content == "" {
		content = fmt.Sprintf("No definition found for symbol '%s'", symbol)
	}
	return &domain.ToolResult{Content: content}, nil
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

	absPath, err := securePath(t.rootPath, path)
	if err != nil {
		return toolError(err.Error()), nil
	}

	if _, err := os.Stat(absPath); err != nil {
		return toolError(fmt.Sprintf("file not found: %s", path)), nil
	}

	ext := filepath.Ext(path)
	pattern := outlinePattern(ext)

	cmd := exec.CommandContext(ctx, "grep", "-n", "--color=never", "-E", pattern, absPath)
	out, _ := cmd.CombinedOutput() // grep returns exit 1 if no matches

	content := strings.TrimSpace(string(out))
	if content == "" {
		content = fmt.Sprintf("No top-level definitions found in %s", path)
	} else {
		lines := strings.Split(content, "\n")
		if len(lines) > t.maxResults {
			lines = lines[:t.maxResults]
			lines = append(lines, fmt.Sprintf("... (truncated at %d results)", t.maxResults))
		}
		content = strings.Join(lines, "\n")
	}
	return &domain.ToolResult{Content: content}, nil
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

func securePath(root, path string) (string, error) {
	// Prevent path traversal
	cleaned := filepath.Clean(path)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path traversal not allowed: %s", path)
	}
	abs := filepath.Join(root, cleaned)
	// Verify it's still under root. Use root+separator to prevent prefix collision:
	// e.g. root="/repos/123" must not match abs="/repos/1234/file".
	if !strings.HasPrefix(abs, root+string(filepath.Separator)) && abs != root {
		return "", fmt.Errorf("path traversal not allowed: %s", path)
	}
	return abs, nil
}

func toolError(msg string) *domain.ToolResult {
	return &domain.ToolResult{Error: &msg}
}
