package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/shared"
)

// NoteAccumulator stores notes saved by the agent during a review session.
// Notes survive context compression and act as persistent memory across iterations.
type NoteAccumulator struct {
	mu    sync.Mutex
	notes []string
}

func (n *NoteAccumulator) Add(note string) {
	n.mu.Lock()
	n.notes = append(n.notes, note)
	n.mu.Unlock()
}

// Summary returns all saved notes joined by newline, or empty string if none.
func (n *NoteAccumulator) Summary() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.notes) == 0 {
		return ""
	}
	return strings.Join(n.notes, "\n")
}

type Registry struct {
	rootPath  string
	diffFiles []shared.DiffFile
	tools     map[string]shared.Tool
	cache     map[string]*shared.ToolResult
	cacheMu   sync.RWMutex
	config    config.ToolConfig
	Notes     *NoteAccumulator // persistent notes that survive context compression
}

func NewRegistry(rootPath string, diffFiles []shared.DiffFile, cfg config.ToolConfig) *Registry {
	acc := &NoteAccumulator{}
	r := &Registry{
		rootPath:  rootPath,
		diffFiles: diffFiles,
		tools:     make(map[string]shared.Tool),
		cache:     make(map[string]*shared.ToolResult),
		config:    cfg,
		Notes:     acc,
	}
	r.Register(&ReadFileTool{rootPath: rootPath, maxKB: cfg.ReadFileMaxKB, maxLines: cfg.ToolResultMaxLines})
	r.Register(&GetMultiDiffTool{rootPath: rootPath, diffFiles: diffFiles, maxFiles: cfg.MultiDiffMaxFiles, maxKB: cfg.MultiDiffMaxKB, baseSHA: cfg.BaseSHA, headSHA: cfg.HeadSHA, gitEnv: cfg.GitEnv})
	r.Register(&SearchCodeTool{rootPath: rootPath, maxResults: cfg.SearchMaxResults})
	r.Register(&ReadMultiFileTool{rootPath: rootPath, maxFiles: cfg.ReadMultiFileMaxFiles, perFileKB: cfg.ReadMultiFilePerFileKB, maxLines: cfg.ToolResultMaxLines})
	r.Register(&ListDirTool{rootPath: rootPath})
	r.Register(&GetSymbolDefinitionTool{rootPath: rootPath, maxResults: cfg.SearchMaxResults})
	r.Register(&GetGitLogTool{rootPath: rootPath, baseSHA: cfg.BaseSHA, headSHA: cfg.HeadSHA})
	r.Register(&GetFileOutlineTool{rootPath: rootPath, maxResults: cfg.SearchMaxResults})
	r.Register(&SaveNoteTool{acc: acc})
	return r
}

func (r *Registry) Register(tool shared.Tool) {
	r.tools[tool.Name()] = tool
}

// Definitions returns all tool definitions for the LLM.
func (r *Registry) Definitions() []shared.ToolDefinition {
	var defs []shared.ToolDefinition
	for _, t := range r.tools {
		defs = append(defs, shared.ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return defs
}

// Execute runs a tool by name, with dedup caching.
func (r *Registry) Execute(ctx context.Context, name string, input shared.ToolInput) (*shared.ToolResult, error) {
	tool, ok := r.tools[name]
	if !ok {
		errStr := fmt.Sprintf("unknown tool: %s", name)
		return &shared.ToolResult{Error: &errStr}, nil
	}

	// Cache key = sha256(name + json(input))
	cacheKey := r.cacheKey(name, input)

	r.cacheMu.RLock()
	if cached, ok := r.cache[cacheKey]; ok {
		r.cacheMu.RUnlock()
		result := *cached
		result.IsCached = true
		return &result, nil
	}
	r.cacheMu.RUnlock()

	result, err := tool.Execute(ctx, input)
	if err != nil {
		return nil, err
	}

	r.cacheMu.Lock()
	r.cache[cacheKey] = result
	r.cacheMu.Unlock()

	return result, nil
}

func (r *Registry) cacheKey(name string, input shared.ToolInput) string {
	data, _ := json.Marshal(input)
	h := sha256.Sum256(append([]byte(name+":"), data...))
	return fmt.Sprintf("%x", h)
}
