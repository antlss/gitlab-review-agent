package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Server ServerConfig
	Store  StoreConfig
	GitLab GitLabConfig
	Git    GitConfig
	LLM    LLMConfig
	Worker WorkerConfig
	Review ReviewConfig
	Tool   ToolConfig
	Cron   CronConfig
	Log    LogConfig
}

type GitConfig struct {
	ReposDir string
}

type ServerConfig struct {
	Port string
	Host string
}

// StoreConfig holds the configuration for the storage backend.
// Driver selects the backend: "postgres", "sqlite", or "file".
type StoreConfig struct {
	Driver       string // "postgres" | "sqlite" | "file"
	PostgresURL  string
	MaxOpenConns int
	MaxIdleConns int
	SQLitePath   string // path to SQLite database file
	DataDir      string // base directory for file-based storage
}

type GitLabConfig struct {
	BaseURL       string
	Token         string
	WebhookSecret string
	BotUserID     int64
}

type LLMConfig struct {
	DefaultProvider      string   // "openai" | "anthropic" | "google"
	DefaultModel         string
	OpenAIAPIKey         string   // primary key (first from OpenAIAPIKeys)
	OpenAIAPIKeys        []string // all keys for load balancing
	OpenAIBaseURL        string
	AnthropicAPIKey      string   // primary key (first from AnthropicAPIKeys)
	AnthropicAPIKeys     []string // all keys for load balancing
	GoogleAPIKey         string   // primary key (first from GoogleAPIKeys)
	GoogleAPIKeys        []string // all keys for load balancing
	ContextWindowSizes   map[string]int
	CompressionThreshold float64
}

type WorkerConfig struct {
	PoolSize       int
	PollIntervalMs int
}

type ReviewConfig struct {
	MaxFilesBeforeSample int
	SampleFileCount      int
	LargePRAction        string // "sample" | "block"
	PreloadDiffThreshold int    // max file count to pre-inject ALL diffs into user message (default 20)
	PreloadDiffMaxKB     int    // max total KB of pre-injected diff content (default 100)
	ResponseLanguage     string // "en" | "vi" | "ja" — language for AI-generated GitLab comments/replies
	TriggerLabel         string // MR label that triggers review (default "ai-review")
	ChunkSize            int    // target files per chunk for map-reduce review (default 10)
	MaxParallelChunks    int    // max concurrent chunk reviews (default 5)
	ChunkThreshold       int    // file count above which chunking is enabled (default 12)
}

type ToolConfig struct {
	ReadFileMaxKB          int
	SearchMaxResults       int
	MultiDiffMaxFiles      int
	MultiDiffMaxKB         int
	ReadMultiFileMaxFiles  int
	ReadMultiFilePerFileKB int
	ToolResultMaxLines     int
	BaseSHA                string   `json:"-"` // Set per-job, not from env
	HeadSHA                string   `json:"-"` // Set per-job, not from env
	GitEnv                 []string `json:"-"` // Set per-job, git environment for token injection
}

type CronConfig struct {
	FeedbackConsolidateSchedule   string
	FeedbackConsolidateMinCount   int
	FeedbackConsolidateMinAgeDays int
	FeedbackCustomPromptMaxWords  int
}

type LogConfig struct {
	Level  string
	Format string
}

var defaultContextWindows = map[string]int{
	// OpenAI
	"gpt-4o":       128000,
	"gpt-4o-mini":  128000,
	"gpt-4.1":      1047576,
	"gpt-4.1-mini": 1047576,
	"o1":           200000,
	"o3-mini":      200000,
	// Anthropic
	"claude-sonnet":              200000,
	"claude-3-5-sonnet-20241022": 200000,
	"claude-3-7-sonnet-20250219": 200000,
	"claude-sonnet-4-5":          200000,
	"claude-opus-4-5":            200000,
	"claude-haiku-4-5-20251001":  200000,
	// Google
	"gemini-pro":             1000000,
	"gemini-1.5-pro":         2000000,
	"gemini-1.5-flash":       1000000,
	"gemini-2.0-flash-exp":   1000000,
	"gemini-2.5-pro-preview": 1000000,
}

func Load() (*Config, error) {
	cfg := &Config{}

	cfg.Server.Port = envOrDefault("SERVER_PORT", "8080")
	cfg.Server.Host = envOrDefault("SERVER_HOST", "0.0.0.0")

	cfg.Store.Driver = strings.ToLower(envOrDefault("STORE_DRIVER", "file"))
	switch cfg.Store.Driver {
	case "postgres":
		cfg.Store.PostgresURL = os.Getenv("DATABASE_URL")
		if cfg.Store.PostgresURL == "" {
			return nil, fmt.Errorf("DATABASE_URL is required when STORE_DRIVER=postgres")
		}
		cfg.Store.MaxOpenConns = envIntOrDefault("DATABASE_MAX_OPEN_CONNS", 25)
		cfg.Store.MaxIdleConns = envIntOrDefault("DATABASE_MAX_IDLE_CONNS", 5)
	case "sqlite":
		cfg.Store.SQLitePath = envOrDefault("SQLITE_PATH", "data/review-agent.db")
	case "file":
		cfg.Store.DataDir = envOrDefault("STORE_DATA_DIR", "data")
	default:
		return nil, fmt.Errorf("unsupported STORE_DRIVER: %s (must be postgres, sqlite, or file)", cfg.Store.Driver)
	}

	cfg.GitLab.BaseURL = os.Getenv("GITLAB_BASE_URL")
	if cfg.GitLab.BaseURL == "" {
		return nil, fmt.Errorf("GITLAB_BASE_URL is required")
	}
	cfg.GitLab.Token = os.Getenv("GITLAB_TOKEN")
	if cfg.GitLab.Token == "" {
		return nil, fmt.Errorf("GITLAB_TOKEN is required")
	}
	cfg.GitLab.WebhookSecret = os.Getenv("GITLAB_WEBHOOK_SECRET")
	if cfg.GitLab.WebhookSecret == "" {
		return nil, fmt.Errorf("GITLAB_WEBHOOK_SECRET is required")
	}
	cfg.GitLab.BotUserID = int64(envIntOrDefault("GITLAB_BOT_USER_ID", 0))
	if cfg.GitLab.BotUserID == 0 {
		return nil, fmt.Errorf("GITLAB_BOT_USER_ID is required")
	}

	cfg.Git.ReposDir = envOrDefault("GIT_REPOS_DIR", "resource/repos")

	cfg.LLM.DefaultProvider = envOrDefault("LLM_DEFAULT_PROVIDER", "openai")
	cfg.LLM.DefaultModel = envOrDefault("LLM_DEFAULT_MODEL", "gpt-4o")
	cfg.LLM.OpenAIAPIKeys = parseMultiKeys("OPENAI_API_KEY", "OPENAI_API_KEYS")
	cfg.LLM.OpenAIAPIKey = firstOrEmpty(cfg.LLM.OpenAIAPIKeys)
	cfg.LLM.OpenAIBaseURL = envOrDefault("OPENAI_BASE_URL", "https://api.openai.com/v1")
	cfg.LLM.AnthropicAPIKeys = parseMultiKeys("ANTHROPIC_API_KEY", "ANTHROPIC_API_KEYS")
	cfg.LLM.AnthropicAPIKey = firstOrEmpty(cfg.LLM.AnthropicAPIKeys)
	cfg.LLM.GoogleAPIKeys = parseMultiKeys("GOOGLE_API_KEY", "GOOGLE_API_KEYS")
	cfg.LLM.GoogleAPIKey = firstOrEmpty(cfg.LLM.GoogleAPIKeys)
	cfg.LLM.ContextWindowSizes = defaultContextWindows
	cfg.LLM.CompressionThreshold = envFloatOrDefault("LLM_CONTEXT_COMPRESSION_THRESHOLD", 0.55)

	cfg.Worker.PoolSize = envIntOrDefault("WORKER_POOL_SIZE", 5)
	cfg.Worker.PollIntervalMs = envIntOrDefault("WORKER_POLL_INTERVAL_MS", 500)

	cfg.Review.MaxFilesBeforeSample = envIntOrDefault("REVIEW_MAX_FILES_BEFORE_SAMPLE", 150)
	cfg.Review.SampleFileCount = envIntOrDefault("REVIEW_SAMPLE_FILE_COUNT", 30)
	cfg.Review.LargePRAction = envOrDefault("REVIEW_LARGE_PR_ACTION", "sample")
	cfg.Review.PreloadDiffThreshold = envIntOrDefault("REVIEW_PRELOAD_DIFF_THRESHOLD", 20)
	cfg.Review.PreloadDiffMaxKB = envIntOrDefault("REVIEW_PRELOAD_DIFF_MAX_KB", 100)
	cfg.Review.ResponseLanguage = envOrDefault("AI_RESPONSE_LANGUAGE", "en")
	cfg.Review.TriggerLabel = envOrDefault("REVIEW_TRIGGER_LABEL", "ai-review")
	cfg.Review.ChunkSize = envIntOrDefault("REVIEW_CHUNK_SIZE", 10)
	cfg.Review.MaxParallelChunks = envIntOrDefault("REVIEW_MAX_PARALLEL_CHUNKS", 5)
	cfg.Review.ChunkThreshold = envIntOrDefault("REVIEW_CHUNK_THRESHOLD", 12)

	cfg.Tool.ReadFileMaxKB = envIntOrDefault("TOOL_READ_FILE_MAX_KB", 30)
	cfg.Tool.SearchMaxResults = envIntOrDefault("TOOL_SEARCH_MAX_RESULTS", 20)
	cfg.Tool.MultiDiffMaxFiles = envIntOrDefault("TOOL_MULTI_DIFF_MAX_FILES", 15)
	cfg.Tool.MultiDiffMaxKB = envIntOrDefault("TOOL_MULTI_DIFF_MAX_KB", 60)
	cfg.Tool.ReadMultiFileMaxFiles = envIntOrDefault("TOOL_READ_MULTI_FILE_MAX_FILES", 8)
	cfg.Tool.ReadMultiFilePerFileKB = envIntOrDefault("TOOL_READ_MULTI_FILE_PER_FILE_MAX_KB", 15)
	cfg.Tool.ToolResultMaxLines = envIntOrDefault("TOOL_RESULT_MAX_LINES", 60)

	cfg.Cron.FeedbackConsolidateSchedule = envOrDefault("CRON_FEEDBACK_CONSOLIDATE_SCHEDULE", "0 2 * * 1")
	cfg.Cron.FeedbackConsolidateMinCount = envIntOrDefault("FEEDBACK_CONSOLIDATE_MIN_COUNT", 30)
	cfg.Cron.FeedbackConsolidateMinAgeDays = envIntOrDefault("FEEDBACK_CONSOLIDATE_MIN_AGE_DAYS", 14)
	cfg.Cron.FeedbackCustomPromptMaxWords = envIntOrDefault("FEEDBACK_CUSTOM_PROMPT_MAX_WORDS", 800)

	cfg.Log.Level = strings.ToLower(envOrDefault("LOG_LEVEL", "info"))
	cfg.Log.Format = strings.ToLower(envOrDefault("LOG_FORMAT", "json"))

	return cfg, nil
}

func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

// parseMultiKeys collects API keys from two env vars: the single-key var and the multi-key var.
// OPENAI_API_KEY=key1 and OPENAI_API_KEYS=key2,key3 → [key1, key2, key3]
// Deduplicates and trims whitespace.
func parseMultiKeys(singleEnv, multiEnv string) []string {
	seen := make(map[string]bool)
	var keys []string

	add := func(k string) {
		k = strings.TrimSpace(k)
		if k != "" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}

	add(os.Getenv(singleEnv))

	if multi := os.Getenv(multiEnv); multi != "" {
		for _, k := range strings.Split(multi, ",") {
			add(k)
		}
	}

	return keys
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envIntOrDefault(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("invalid int config, using default", "key", key, "value", v, "default", defaultVal)
		return defaultVal
	}
	return n
}

func envFloatOrDefault(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		slog.Warn("invalid float config, using default", "key", key, "value", v, "default", defaultVal)
		return defaultVal
	}
	return f
}
