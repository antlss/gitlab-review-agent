package domain

import (
	"context"

	"github.com/google/uuid"
)

// ─── Interfaces ─────────────────────────────────────────────────────────────────

type RepositorySettingsStore interface {
	GetByProjectID(ctx context.Context, projectID int64) (*RepositorySettings, error)
	GetOrCreate(ctx context.Context, projectID int64, projectPath string) (*RepositorySettings, error)
	Upsert(ctx context.Context, settings *RepositorySettings) error
	IncrementFeedbackCount(ctx context.Context, projectID int64, delta int) error
	ResetFeedbackCount(ctx context.Context, projectID int64) error
	UpdateCustomPrompt(ctx context.Context, projectID int64, prompt string) error
	ListEnabled(ctx context.Context) ([]*RepositorySettings, error)
	MarkArchived(ctx context.Context, projectID int64) error
}

type ReviewJobStore interface {
	Create(ctx context.Context, job *ReviewJob) error
	GetByID(ctx context.Context, id uuid.UUID) (*ReviewJob, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status ReviewJobStatus, errMsg *string) error
	UpdateBaseSHA(ctx context.Context, id uuid.UUID, baseSHA string) error
	UpdateAIOutput(ctx context.Context, id uuid.UUID, raw string, parsed []ParsedComment, iterations, tokens int) error
	UpdateCompleted(ctx context.Context, id uuid.UUID, posted, suppressed int) error
	UpdateModelUsed(ctx context.Context, id uuid.UUID, model string) error
	ExistsPendingOrCompleted(ctx context.Context, projectID, mrIID int64, headSHA string, withinMinutes int) (bool, error)
	ListByProject(ctx context.Context, projectID int64, limit int) ([]*ReviewJob, error)
	ListStale(ctx context.Context, olderThanMinutes int) ([]*ReviewJob, error)
}

type ReplyJobStore interface {
	Create(ctx context.Context, job *ReplyJob) error
	GetByID(ctx context.Context, id uuid.UUID) (*ReplyJob, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status ReplyJobStatus, errMsg *string) error
	UpdateCompleted(ctx context.Context, id uuid.UUID, reply string, intent ReplyIntent, signal FeedbackSignal) error
}

type FeedbackStore interface {
	Create(ctx context.Context, feedback *ReviewFeedback) error
	GetByNoteID(ctx context.Context, noteID int64) (*ReviewFeedback, error)
	UpdateSignal(ctx context.Context, noteID int64, signal FeedbackSignal, replyContent string) error
	ListForConsolidation(ctx context.Context, projectID int64, minAgeDays int) ([]*ReviewFeedback, error)
	MarkConsolidated(ctx context.Context, ids []uuid.UUID) error
	ListRecentByProject(ctx context.Context, projectID int64, limit int) ([]*ReviewFeedback, error)
}

type ReviewRecordStore interface {
	GetLastCompleted(ctx context.Context, projectID, mrIID int64) (*ReviewRecord, error)
	Upsert(ctx context.Context, record *ReviewRecord) error
}

type GitLabClient interface {
	GetMR(ctx context.Context, projectID, mrIID int64) (*GitLabMR, error)
	GetProject(ctx context.Context, projectID int64) (*GitLabProject, error)
	ListMRFiles(ctx context.Context, projectID, mrIID int64) ([]GitLabMRFile, error)
	GetMRDiscussions(ctx context.Context, projectID, mrIID int64) ([]GitLabDiscussion, error)
	GetDiscussion(ctx context.Context, projectID, mrIID int64, discussionID string) (*GitLabDiscussion, error)
	PostInlineComment(ctx context.Context, req PostInlineCommentRequest) (*PostCommentResponse, error)
	PostThreadComment(ctx context.Context, projectID, mrIID int64, body string) (*PostCommentResponse, error)
	PostReply(ctx context.Context, projectID, mrIID int64, discussionID string, body string) (*PostCommentResponse, error)
	ResolveDiscussion(ctx context.Context, projectID, mrIID int64, discussionID string) error
}

type LLMClient interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	ModelName() string
	ContextWindowSize() int
	// ClientCount returns the number of underlying API key clients.
	// Single-provider clients return 1; BalancedClient returns the key count.
	// Used by the pipeline to set adaptive chunk concurrency.
	ClientCount() int
}

type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Execute(ctx context.Context, input ToolInput) (*ToolResult, error)
}
