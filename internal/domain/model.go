package domain

import (
	"time"

	"github.com/google/uuid"
)

// ─── Core domain models ─────────────────────────────────────────────────────────

type RepositorySettings struct {
	ID              uuid.UUID `db:"id" json:"id"`
	GitLabProjectID int64     `db:"gitlab_project_id" json:"gitlabProjectId"`
	ProjectPath     string    `db:"project_path" json:"projectPath"`
	ModelOverride   *string   `db:"model_override" json:"modelOverride,omitempty"`
	Language        *string   `db:"language" json:"language,omitempty"`
	Framework       *string   `db:"framework" json:"framework,omitempty"`
	CustomPrompt    *string   `db:"custom_prompt" json:"customPrompt,omitempty"`
	ExcludePatterns *string   `db:"exclude_patterns" json:"excludePatterns,omitempty"`
	FeedbackCount   int       `db:"feedback_count" json:"feedbackCount"`
	IsArchived      bool      `db:"is_archived" json:"isArchived"`
	CreatedAt       time.Time `db:"created_at" json:"createdAt"`
	UpdatedAt       time.Time `db:"updated_at" json:"updatedAt"`
}

func (r *RepositorySettings) ExcludePatternList() []string {
	if r.ExcludePatterns == nil || *r.ExcludePatterns == "" {
		return nil
	}
	return ParseCSV(*r.ExcludePatterns)
}

type ReviewJob struct {
	ID                      uuid.UUID       `db:"id" json:"id"`
	GitLabProjectID         int64           `db:"gitlab_project_id" json:"gitlabProjectId"`
	MrIID                   int64           `db:"mr_iid" json:"mrIid"`
	HeadSHA                 string          `db:"head_sha" json:"headSha"`
	BaseSHA                 *string         `db:"base_sha" json:"baseSha,omitempty"`
	TargetBranch            string          `db:"target_branch" json:"targetBranch"`
	SourceBranch            string          `db:"source_branch" json:"sourceBranch"`
	IsForcePush             bool            `db:"is_force_push" json:"isForcePush"`
	DryRun                  bool            `db:"dry_run" json:"dryRun"`
	TriggerSource           TriggerSource   `db:"trigger_source" json:"triggerSource"`
	Status                  ReviewJobStatus `db:"status" json:"status"`
	ModelUsed               *string         `db:"model_used" json:"modelUsed,omitempty"`
	RepoModelOverride       *string         `db:"repo_model_override" json:"repoModelOverride,omitempty"`
	RepoLanguage            *string         `db:"repo_language" json:"repoLanguage,omitempty"`
	RepoFramework           *string         `db:"repo_framework" json:"repoFramework,omitempty"`
	RepoExcludePatterns     *string         `db:"repo_exclude_patterns" json:"repoExcludePatterns,omitempty"`
	AIOutputRaw             *string         `db:"ai_output_raw" json:"aiOutputRaw,omitempty"`
	AIOutputParsed          []byte          `db:"ai_output_parsed" json:"aiOutputParsed,omitempty"` // JSONB stored as bytes
	IterationsUsed          *int            `db:"iterations_used" json:"iterationsUsed,omitempty"`
	TokensEstimated         *int            `db:"tokens_estimated" json:"tokensEstimated,omitempty"`
	TotalCommentsPosted     *int            `db:"total_comments_posted" json:"totalCommentsPosted,omitempty"`
	TotalCommentsSuppressed *int            `db:"total_comments_suppressed" json:"totalCommentsSuppressed,omitempty"`
	QueuedAt                time.Time       `db:"queued_at" json:"queuedAt"`
	StartedAt               *time.Time      `db:"started_at" json:"startedAt,omitempty"`
	CompletedAt             *time.Time      `db:"completed_at" json:"completedAt,omitempty"`
	ErrorMessage            *string         `db:"error_message" json:"errorMessage,omitempty"`
	CreatedAt               time.Time       `db:"created_at" json:"createdAt"`
	UpdatedAt               time.Time       `db:"updated_at" json:"updatedAt"`
}

func (j *ReviewJob) ExcludePatternList() []string {
	if j.RepoExcludePatterns == nil || *j.RepoExcludePatterns == "" {
		return nil
	}
	return ParseCSV(*j.RepoExcludePatterns)
}

type ReplyJob struct {
	ID                 uuid.UUID       `db:"id" json:"id"`
	GitLabProjectID    int64           `db:"gitlab_project_id" json:"gitlabProjectId"`
	MrIID              int64           `db:"mr_iid" json:"mrIid"`
	DiscussionID       string          `db:"discussion_id" json:"discussionId"`
	TriggerNoteID      int64           `db:"trigger_note_id" json:"triggerNoteId"`
	TriggerNoteContent string          `db:"trigger_note_content" json:"triggerNoteContent"`
	TriggerNoteAuthor  string          `db:"trigger_note_author" json:"triggerNoteAuthor"`
	BotCommentID       int64           `db:"bot_comment_id" json:"botCommentId"`
	BotCommentContent  string          `db:"bot_comment_content" json:"botCommentContent"`
	BotCommentFilePath *string         `db:"bot_comment_file_path" json:"botCommentFilePath,omitempty"`
	BotCommentLine     *int            `db:"bot_comment_line" json:"botCommentLine,omitempty"`
	Status             ReplyJobStatus  `db:"status" json:"status"`
	ReplyContent       *string         `db:"reply_content" json:"replyContent,omitempty"`
	IntentClassified   *ReplyIntent    `db:"intent_classified" json:"intentClassified,omitempty"`
	FeedbackSignal     *FeedbackSignal `db:"feedback_signal" json:"feedbackSignal,omitempty"`
	ErrorMessage       *string         `db:"error_message" json:"errorMessage,omitempty"`
	QueuedAt           time.Time       `db:"queued_at" json:"queuedAt"`
	StartedAt          *time.Time      `db:"started_at" json:"startedAt,omitempty"`
	CompletedAt        *time.Time      `db:"completed_at" json:"completedAt,omitempty"`
	CreatedAt          time.Time       `db:"created_at" json:"createdAt"`
	UpdatedAt          time.Time       `db:"updated_at" json:"updatedAt"`
}

type ReviewFeedback struct {
	ID                 uuid.UUID        `db:"id" json:"id"`
	GitLabProjectID    int64            `db:"gitlab_project_id" json:"gitlabProjectId"`
	ReviewJobID        *uuid.UUID       `db:"review_job_id" json:"reviewJobId,omitempty"`
	GitLabDiscussionID string           `db:"gitlab_discussion_id" json:"gitlabDiscussionId"`
	GitLabNoteID       int64            `db:"gitlab_note_id" json:"gitlabNoteId"`
	FilePath           *string          `db:"file_path" json:"filePath,omitempty"`
	LineNumber         *int             `db:"line_number" json:"lineNumber,omitempty"`
	Category           *CommentCategory `db:"category" json:"category,omitempty"`
	CommentSummary     *string          `db:"comment_summary" json:"commentSummary,omitempty"`
	Language           *string          `db:"language" json:"language,omitempty"`
	Signal             *FeedbackSignal  `db:"signal" json:"signal,omitempty"`
	SignalReplyContent *string          `db:"signal_reply_content" json:"signalReplyContent,omitempty"`
	ModelUsed          *string          `db:"model_used" json:"modelUsed,omitempty"`
	ConsolidatedAt     *time.Time       `db:"consolidated_at" json:"consolidatedAt,omitempty"`
	CreatedAt          time.Time        `db:"created_at" json:"createdAt"`
	UpdatedAt          time.Time        `db:"updated_at" json:"updatedAt"`
}

type ReviewRecord struct {
	ID              uuid.UUID `db:"id" json:"id"`
	GitLabProjectID int64     `db:"gitlab_project_id" json:"gitlabProjectId"`
	MrIID           int64     `db:"mr_iid" json:"mrIid"`
	ReviewJobID     uuid.UUID `db:"review_job_id" json:"reviewJobId"`
	HeadSHA         string    `db:"head_sha" json:"headSha"`
	ReviewedFiles   []byte    `db:"reviewed_files" json:"reviewedFiles"` // JSONB
	CommentsPosted  int       `db:"comments_posted" json:"commentsPosted"`
	CreatedAt       time.Time `db:"created_at" json:"createdAt"`
}

// ─── Transfer objects ────────────────────────────────────────────────────────────

type ParsedComment struct {
	FilePath           string          `json:"filePath"`
	LineNumber         int             `json:"lineNumber"`
	ReviewComment      string          `json:"reviewComment"`
	Confidence         string          `json:"confidence"` // "HIGH" | "MEDIUM" | "LOW"
	Severity           CommentSeverity `json:"severity"`   // "critical" | "high" | "medium" | "low"
	Category           CommentCategory `json:"category"`
	Suggestion         string          `json:"suggestion,omitempty"` // concrete code fix suggestion
	GitLabNoteID       *int64          `json:"gitlabNoteId,omitempty"`
	GitLabDiscussionID *string         `json:"gitlabDiscussionId,omitempty"`
	Suppressed         bool            `json:"suppressed,omitempty"`
	DropReason         string          `json:"dropReason,omitempty"`
}

type DiffFile struct {
	Path         string   `json:"path"`
	OldPath      string   `json:"oldPath"`
	Status       string   `json:"status"` // "A" | "M" | "D" | "R"
	LinesAdded   int      `json:"linesAdded"`
	LinesRemoved int      `json:"linesRemoved"`
	RiskScore    float64  `json:"riskScore"`
	RiskTier     RiskTier `json:"riskTier"`
	AddedLines   []int    `json:"addedLines"`
}

type ReviewContext struct {
	MRTitle       string
	MRDescription string
	MissingIntent bool

	CustomPrompt    *string
	RecentFeedbacks []FeedbackSnippet

	ExistingUnresolvedComments []ExistingComment
	BotUnresolvedComments     []BotUnresolvedComment

	DetectedLanguage   string
	DetectedFramework  string
	LanguageGuidelines string
}

type FeedbackSnippet struct {
	Category       CommentCategory
	CommentSummary string
	Signal         FeedbackSignal
	CreatedAt      time.Time
}

type ExistingComment struct {
	FilePath   string
	LineNumber int
	Summary    string
}

type BotUnresolvedComment struct {
	DiscussionID string
	FilePath     string
	LineNumber   int
}

// ─── GitLab types ────────────────────────────────────────────────────────────────

type GitLabMR struct {
	IID            int64
	Title          string
	Description    string
	SourceBranch   string
	TargetBranch   string
	HeadSHA        string
	WebURL         string
	AuthorUsername string
}

type GitLabDiscussion struct {
	ID    string
	Notes []GitLabNote
}

type GitLabNote struct {
	ID         int64
	AuthorID   int64
	AuthorName string
	Body       string
	Resolvable bool
	Resolved   bool
	Position   *GitLabNotePosition
	CreatedAt  time.Time
}

type GitLabNotePosition struct {
	FilePath string
	NewLine  int
	OldLine  int
}

type GitLabMRFile struct {
	OldPath     string
	NewPath     string
	NewFile     bool
	DeletedFile bool
	RenamedFile bool
}

type GitLabProject struct {
	ID         int64
	PathWithNS string
}

type PostInlineCommentRequest struct {
	ProjectID int64
	MrIID     int64
	Body      string
	FilePath  string
	NewLine   int
	BaseSHA   string
	HeadSHA   string
	StartSHA  string
}

type PostCommentResponse struct {
	NoteID       int64
	DiscussionID string
}

// ─── Queue types ────────────────────────────────────────────────────────────────

type QueueJob struct {
	Type       QueueJobType `json:"type"`
	JobID      uuid.UUID    `json:"jobId"`
	ProjectID  int64        `json:"projectId"`
	EnqueuedAt time.Time    `json:"enqueuedAt"`
}

// ─── LLM types ──────────────────────────────────────────────────────────────────

type ChatMessage struct {
	Role       string // "system" | "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

type ToolCall struct {
	ID        string
	Name      string
	InputJSON string
}

type ChatRequest struct {
	Model       string
	Messages    []ChatMessage
	Tools       []ToolDefinition
	MaxTokens   int
	Temperature float64
}

type ChatResponse struct {
	Content    string
	ToolCalls  []ToolCall
	StopReason string // "stop" | "tool_use" | "max_tokens"
	Usage      TokenUsage
}

type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ─── Tool types ─────────────────────────────────────────────────────────────────

type ToolInput map[string]any

type ToolResult struct {
	Content  string
	IsCached bool
	Error    *string
}

