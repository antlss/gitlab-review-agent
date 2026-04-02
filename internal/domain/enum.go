package domain

// ─── Enums ──────────────────────────────────────────────────────────────────────

type ReviewJobStatus string

const (
	ReviewJobStatusPending         ReviewJobStatus = "PENDING"
	ReviewJobStatusReviewing       ReviewJobStatus = "REVIEWING"
	ReviewJobStatusPosting         ReviewJobStatus = "POSTING"
	ReviewJobStatusCompleted       ReviewJobStatus = "COMPLETED"
	ReviewJobStatusFailed          ReviewJobStatus = "FAILED"
	ReviewJobStatusParseFailed     ReviewJobStatus = "PARSE_FAILED"
	ReviewJobStatusSkippedSize ReviewJobStatus = "SKIPPED_SIZE"
)

type ReplyJobStatus string

const (
	ReplyJobStatusPending    ReplyJobStatus = "PENDING"
	ReplyJobStatusProcessing ReplyJobStatus = "PROCESSING"
	ReplyJobStatusCompleted  ReplyJobStatus = "COMPLETED"
	ReplyJobStatusFailed     ReplyJobStatus = "FAILED"
)

type FeedbackSignal string

const (
	FeedbackSignalAccepted FeedbackSignal = "ACCEPTED"
	FeedbackSignalRejected FeedbackSignal = "REJECTED"
	FeedbackSignalNeutral  FeedbackSignal = "NEUTRAL"
)

type TriggerSource string

const (
	TriggerSourceWebhook    TriggerSource = "webhook"
	TriggerSourceCLI        TriggerSource = "cli"
	TriggerSourceQueueRetry TriggerSource = "queue_retry"
)

type CommentCategory string

const (
	CategorySecurity    CommentCategory = "security"
	CategoryBug         CommentCategory = "bug"
	CategoryLogic       CommentCategory = "logic"
	CategoryPerformance CommentCategory = "performance"
	CategoryNaming      CommentCategory = "naming"
	CategoryStyle       CommentCategory = "style"
)

type CommentSeverity string

const (
	SeverityCritical CommentSeverity = "critical"
	SeverityHigh     CommentSeverity = "high"
	SeverityMedium   CommentSeverity = "medium"
	SeverityLow      CommentSeverity = "low"
)

type ReplyIntent string

const (
	IntentAgree       ReplyIntent = "agree"
	IntentReject      ReplyIntent = "reject"
	IntentQuestion    ReplyIntent = "question"
	IntentDiscuss     ReplyIntent = "discuss"
	IntentAcknowledge ReplyIntent = "acknowledge"
)

type ThreadState string

const (
	ThreadStateOpen                ThreadState = "open"
	ThreadStateAcknowledged        ThreadState = "acknowledged"
	ThreadStatePendingVerification ThreadState = "pending_verification"
	ThreadStateResolved            ThreadState = "resolved"
	ThreadStateDismissed           ThreadState = "dismissed"
	ThreadStateSuperseded          ThreadState = "superseded"
)

type RiskTier string

const (
	RiskHigh   RiskTier = "HIGH"
	RiskMedium RiskTier = "MEDIUM"
	RiskLow    RiskTier = "LOW"
)

type QueueJobType string

const (
	QueueJobTypeReview QueueJobType = "review"
	QueueJobTypeReply  QueueJobType = "reply"
)

const (
	DefaultPromptVersion    = "review-prompt-v1"
	DefaultPolicyVersion    = "review-policy-v1"
	DefaultModelPlanVersion = "review-model-plan-v1"
)
