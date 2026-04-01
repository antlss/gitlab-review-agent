package webhook

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/antlss/gitlab-review-agent/internal/pkg/queue"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

type Handler struct {
	webhookSecret      string
	botUserID          int64
	reviewTriggerLabel string
	repoSettings       domain.RepositorySettingsStore
	reviewJobStore     domain.ReviewJobStore
	replyJobStore      domain.ReplyJobStore
	gitlabClient       domain.GitLabClient
	queue              *queue.Queue
	// serverCtx is cancelled when the server begins shutdown, bounding all
	// background goroutines to the server lifecycle.
	serverCtx context.Context
	wg        sync.WaitGroup
}

type HandlerDeps struct {
	WebhookSecret      string
	BotUserID          int64
	ReviewTriggerLabel string
	RepoSettings       domain.RepositorySettingsStore
	ReviewJobStore     domain.ReviewJobStore
	ReplyJobStore      domain.ReplyJobStore
	GitLabClient       domain.GitLabClient
	Queue              *queue.Queue
	// ServerCtx should be a context cancelled when the server shuts down.
	ServerCtx context.Context
}

func NewHandler(deps HandlerDeps) *Handler {
	ctx := deps.ServerCtx
	if ctx == nil {
		ctx = context.Background()
	}
	return &Handler{
		webhookSecret:      deps.WebhookSecret,
		botUserID:          deps.BotUserID,
		reviewTriggerLabel: deps.ReviewTriggerLabel,
		repoSettings:       deps.RepoSettings,
		reviewJobStore:     deps.ReviewJobStore,
		replyJobStore:      deps.ReplyJobStore,
		gitlabClient:       deps.GitLabClient,
		queue:              deps.Queue,
		serverCtx:          ctx,
	}
}

// Shutdown waits for all in-flight background goroutines to finish.
// Call this during graceful shutdown after stopping the HTTP server.
func (h *Handler) Shutdown() {
	h.wg.Wait()
}

func (h *Handler) HandleGitLabEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	if !h.validateSignature(r.Header.Get("X-Gitlab-Token")) {
		slog.Warn("invalid webhook signature", "ip", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var baseEvent struct {
		ObjectKind string `json:"object_kind"`
	}
	if err := json.Unmarshal(body, &baseEvent); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		switch baseEvent.ObjectKind {
		case "merge_request":
			h.handleMREvent(h.serverCtx, body)
		case "note":
			h.handleNoteEvent(h.serverCtx, body)
		}
	}()
}

func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Warn("failed to write health response", "error", err)
	}
}

// ─── MR Event ───────────────────────────────────────────────────────────────────

type mrEventPayload struct {
	ObjectKind string `json:"object_kind"`
	Project    struct {
		ID         int64  `json:"id"`
		PathWithNS string `json:"path_with_namespace"`
	} `json:"project"`
	ObjectAttributes struct {
		IID          int64  `json:"iid"`
		Action       string `json:"action"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		OldRev string `json:"oldrev"`
	} `json:"object_attributes"`
	Labels []struct {
		Title string `json:"title"`
	} `json:"labels"`
}

func (h *Handler) handleMREvent(ctx context.Context, body []byte) {
	var payload mrEventPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.Error("parse MR event", "error", err)
		return
	}

	attrs := payload.ObjectAttributes
	projectID := payload.Project.ID

	if attrs.Action != "open" && attrs.Action != "update" {
		return
	}

	// Check if the MR has the review trigger label
	if !h.hasReviewLabel(payload.Labels) {
		return
	}

	settings, err := h.repoSettings.GetOrCreate(ctx, projectID, payload.Project.PathWithNS)
	if err != nil {
		slog.Error("get or create repo settings", "project_id", projectID, "error", err)
		return
	}

	headSHA := attrs.LastCommit.ID
	if headSHA == "" {
		return
	}

	exists, err := h.reviewJobStore.ExistsPendingOrCompleted(ctx, projectID, attrs.IID, headSHA, 30)
	if err != nil {
		slog.Error("idempotency check", "error", err)
		return
	}
	if exists {
		return
	}

	job := &domain.ReviewJob{
		ID:                  uuid.New(),
		GitLabProjectID:     projectID,
		MrIID:               attrs.IID,
		HeadSHA:             headSHA,
		TargetBranch:        attrs.TargetBranch,
		SourceBranch:        attrs.SourceBranch,
		TriggerSource:       domain.TriggerSourceWebhook,
		Status:              domain.ReviewJobStatusPending,
		RepoModelOverride:   settings.ModelOverride,
		RepoLanguage:        settings.Language,
		RepoFramework:       settings.Framework,
		RepoExcludePatterns: settings.ExcludePatterns,
		QueuedAt:            time.Now(),
	}

	if err := h.reviewJobStore.Create(ctx, job); err != nil {
		slog.Error("create review job", "error", err)
		return
	}

	qJob := domain.QueueJob{
		Type:       domain.QueueJobTypeReview,
		JobID:      job.ID,
		ProjectID:  projectID,
		EnqueuedAt: time.Now(),
	}
	if err := h.queue.Enqueue(ctx, qJob); err != nil {
		slog.Error("enqueue review job", "error", err)
		return
	}

	slog.Info("review job enqueued",
		"job_id", job.ID.String(), "project_id", projectID, "mr_iid", attrs.IID)
}

// ─── Note Event ─────────────────────────────────────────────────────────────────

type noteEventPayload struct {
	ObjectKind string `json:"object_kind"`
	Project    struct {
		ID         int64  `json:"id"`
		PathWithNS string `json:"path_with_namespace"`
	} `json:"project"`
	User struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	ObjectAttributes struct {
		ID           int64  `json:"id"`
		NoteableType string `json:"noteable_type"`
		Body         string `json:"body"`
		DiscussionID string `json:"discussion_id"`
	} `json:"object_attributes"`
	MergeRequest *struct {
		IID int64 `json:"iid"`
	} `json:"merge_request"`
}

func (h *Handler) handleNoteEvent(ctx context.Context, body []byte) {
	var payload noteEventPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}

	if payload.ObjectAttributes.NoteableType != "MergeRequest" || payload.MergeRequest == nil {
		return
	}
	if payload.User.ID == h.botUserID {
		return
	}

	projectID := payload.Project.ID
	mrIID := payload.MergeRequest.IID
	discussionID := payload.ObjectAttributes.DiscussionID

	discussion, err := h.gitlabClient.GetDiscussion(ctx, projectID, mrIID, discussionID)
	if err != nil {
		slog.Warn("failed to get discussion for reply",
			"project_id", projectID, "mr_iid", mrIID,
			"discussion_id", discussionID, "error", err)
		return
	}
	if discussion == nil || len(discussion.Notes) == 0 {
		return
	}

	firstNote := discussion.Notes[0]
	if firstNote.AuthorID != h.botUserID {
		return
	}

	var filePath *string
	var lineNum *int
	if firstNote.Position != nil {
		fp := firstNote.Position.FilePath
		ln := firstNote.Position.NewLine
		filePath = &fp
		lineNum = &ln
	}

	replyJob := &domain.ReplyJob{
		ID:                 uuid.New(),
		GitLabProjectID:    projectID,
		MrIID:              mrIID,
		DiscussionID:       discussionID,
		TriggerNoteID:      payload.ObjectAttributes.ID,
		TriggerNoteContent: payload.ObjectAttributes.Body,
		TriggerNoteAuthor:  payload.User.Username,
		BotCommentID:       firstNote.ID,
		BotCommentContent:  firstNote.Body,
		BotCommentFilePath: filePath,
		BotCommentLine:     lineNum,
		Status:             domain.ReplyJobStatusPending,
		QueuedAt:           time.Now(),
	}

	if err := h.replyJobStore.Create(ctx, replyJob); err != nil {
		slog.Error("create reply job", "error", err)
		return
	}

	qJob := domain.QueueJob{
		Type:       domain.QueueJobTypeReply,
		JobID:      replyJob.ID,
		ProjectID:  projectID,
		EnqueuedAt: time.Now(),
	}
	if err := h.queue.Enqueue(ctx, qJob); err != nil {
		slog.Error("enqueue reply job", "error", err)
		return
	}
}

func (h *Handler) validateSignature(token string) bool {
	return hmac.Equal([]byte(token), []byte(h.webhookSecret))
}

func (h *Handler) hasReviewLabel(labels []struct {
	Title string `json:"title"`
}) bool {
	for _, l := range labels {
		if l.Title == h.reviewTriggerLabel {
			return true
		}
	}
	return false
}
