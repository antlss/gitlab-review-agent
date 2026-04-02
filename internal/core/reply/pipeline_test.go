package reply

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/core/agents/replier"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

type fakeReplyAgent struct {
	reply     string
	lastInput replier.ReplyInput
}

func (a *fakeReplyAgent) GenerateReply(_ context.Context, _ domain.LLMClient, input replier.ReplyInput) (string, error) {
	a.lastInput = input
	return a.reply, nil
}

type fakeReplyRepoManager struct {
	projectID        int64
	projectPath      string
	headSHA          string
	fetchCalls       int
	acquireCalls     int
	releaseCalls     int
	fetchCheckoutErr error
	files            map[string]string
}

func (m *fakeReplyRepoManager) AcquireGitLock(_ context.Context, projectID int64) error {
	m.acquireCalls++
	m.projectID = projectID
	return nil
}

func (m *fakeReplyRepoManager) ReleaseGitLock(_ context.Context, _ int64) {
	m.releaseCalls++
}

func (m *fakeReplyRepoManager) FetchAndCheckout(_ context.Context, projectID int64, projectPath string, _ int64, _ string, headSHA string) error {
	m.fetchCalls++
	m.projectID = projectID
	m.projectPath = projectPath
	m.headSHA = headSHA
	return m.fetchCheckoutErr
}

func (m *fakeReplyRepoManager) ReadFileAtSHA(_ context.Context, _ int64, sha, filePath string) ([]byte, error) {
	if sha != m.headSHA {
		return nil, errors.New("unexpected sha")
	}
	content, ok := m.files[filePath]
	if !ok {
		return nil, errors.New("file not found")
	}
	return []byte(content), nil
}

func (m *fakeReplyRepoManager) RepoPath(_ int64) string {
	return ""
}

type fakeReplyJobStore struct {
	statuses                []domain.ReplyJobStatus
	updateCompletedErr      error
	updateCompletedCalls    int
	lastCompletedIntent     domain.ReplyIntent
	lastCompletedSignal     domain.FeedbackSignal
	lastThreadStateBefore   domain.ThreadState
	lastThreadStateAfter    domain.ThreadState
}

func (s *fakeReplyJobStore) Create(_ context.Context, _ *domain.ReplyJob) error { return nil }
func (s *fakeReplyJobStore) GetByID(_ context.Context, _ uuid.UUID) (*domain.ReplyJob, error) {
	return nil, nil
}
func (s *fakeReplyJobStore) UpdateStatus(_ context.Context, _ uuid.UUID, status domain.ReplyJobStatus, _ *string) error {
	s.statuses = append(s.statuses, status)
	return nil
}
func (s *fakeReplyJobStore) UpdateCompleted(_ context.Context, _ uuid.UUID, _ string, intent domain.ReplyIntent, signal domain.FeedbackSignal, beforeState, afterState domain.ThreadState) error {
	s.updateCompletedCalls++
	s.lastCompletedIntent = intent
	s.lastCompletedSignal = signal
	s.lastThreadStateBefore = beforeState
	s.lastThreadStateAfter = afterState
	return s.updateCompletedErr
}

type fakeReplyRepoSettingsStore struct {
	settings               *domain.RepositorySettings
	incrementFeedbackCalls int
}

func (s *fakeReplyRepoSettingsStore) GetByProjectID(_ context.Context, _ int64) (*domain.RepositorySettings, error) {
	return s.settings, nil
}
func (s *fakeReplyRepoSettingsStore) GetOrCreate(_ context.Context, _ int64, _ string) (*domain.RepositorySettings, error) {
	return s.settings, nil
}
func (s *fakeReplyRepoSettingsStore) Upsert(_ context.Context, _ *domain.RepositorySettings) error {
	return nil
}
func (s *fakeReplyRepoSettingsStore) IncrementFeedbackCount(_ context.Context, _ int64, _ int) error {
	s.incrementFeedbackCalls++
	return nil
}
func (s *fakeReplyRepoSettingsStore) ResetFeedbackCount(_ context.Context, _ int64) error { return nil }
func (s *fakeReplyRepoSettingsStore) UpdateCustomPrompt(_ context.Context, _ int64, _ string) error {
	return nil
}
func (s *fakeReplyRepoSettingsStore) ListEnabled(_ context.Context) ([]*domain.RepositorySettings, error) {
	return nil, nil
}
func (s *fakeReplyRepoSettingsStore) MarkArchived(_ context.Context, _ int64) error { return nil }

type fakeReplyFeedbackStore struct {
	feedback              *domain.ReviewFeedback
	updateSignalCalls     int
	lastUpdatedThreadState domain.ThreadState
}

func (s *fakeReplyFeedbackStore) Create(_ context.Context, _ *domain.ReviewFeedback) error {
	return nil
}
func (s *fakeReplyFeedbackStore) GetByNoteID(_ context.Context, _ int64) (*domain.ReviewFeedback, error) {
	return s.feedback, nil
}
func (s *fakeReplyFeedbackStore) UpdateSignal(_ context.Context, _ int64, _ domain.FeedbackSignal, _ string, threadState domain.ThreadState) error {
	s.updateSignalCalls++
	s.lastUpdatedThreadState = threadState
	if s.feedback != nil {
		s.feedback.ThreadState = domain.Ptr(threadState)
	}
	return nil
}
func (s *fakeReplyFeedbackStore) ListForConsolidation(_ context.Context, _ int64, _ int) ([]*domain.ReviewFeedback, error) {
	return nil, nil
}
func (s *fakeReplyFeedbackStore) MarkConsolidated(_ context.Context, _ []uuid.UUID) error { return nil }
func (s *fakeReplyFeedbackStore) ListRecentByProject(_ context.Context, _ int64, _ int) ([]*domain.ReviewFeedback, error) {
	return nil, nil
}

type fakeReplyGitLabClient struct {
	discussion             *domain.GitLabDiscussion
	mr                     *domain.GitLabMR
	project                *domain.GitLabProject
	postReplyCalls         int
	resolveDiscussionCalls int
}

func (c *fakeReplyGitLabClient) GetMR(_ context.Context, _, _ int64) (*domain.GitLabMR, error) {
	return c.mr, nil
}
func (c *fakeReplyGitLabClient) GetProject(_ context.Context, _ int64) (*domain.GitLabProject, error) {
	return c.project, nil
}
func (c *fakeReplyGitLabClient) ListMRFiles(_ context.Context, _, _ int64) ([]domain.GitLabMRFile, error) {
	return nil, nil
}
func (c *fakeReplyGitLabClient) GetMRDiscussions(_ context.Context, _, _ int64) ([]domain.GitLabDiscussion, error) {
	return nil, nil
}
func (c *fakeReplyGitLabClient) GetDiscussion(_ context.Context, _, _ int64, _ string) (*domain.GitLabDiscussion, error) {
	return c.discussion, nil
}
func (c *fakeReplyGitLabClient) PostInlineComment(_ context.Context, _ domain.PostInlineCommentRequest) (*domain.PostCommentResponse, error) {
	return nil, nil
}
func (c *fakeReplyGitLabClient) PostThreadComment(_ context.Context, _, _ int64, _ string) (*domain.PostCommentResponse, error) {
	return nil, nil
}
func (c *fakeReplyGitLabClient) PostReply(_ context.Context, _, _ int64, _ string, _ string) (*domain.PostCommentResponse, error) {
	c.postReplyCalls++
	return &domain.PostCommentResponse{NoteID: 99}, nil
}
func (c *fakeReplyGitLabClient) ResolveDiscussion(_ context.Context, _, _ int64, _ string) error {
	c.resolveDiscussionCalls++
	return nil
}

func TestPipelineExecuteSyncsLatestRepoStateBeforeGeneratingReply(t *testing.T) {
	content := strings.Join([]string{
		"package pkg",
		"",
		"func Work() error {",
		"    return nil",
		"}",
	}, "\n")

	replyStore := &fakeReplyJobStore{}
	repoSettings := &fakeReplyRepoSettingsStore{
		settings: &domain.RepositorySettings{ProjectPath: "group/project"},
	}
	feedbackStore := &fakeReplyFeedbackStore{}
	gitlabClient := &fakeReplyGitLabClient{
		discussion: &domain.GitLabDiscussion{
			ID: "discussion-1",
			Notes: []domain.GitLabNote{
				{ID: 10, AuthorName: "bot", Body: "Please fix this", CreatedAt: time.Now()},
				{ID: 11, AuthorName: "dev", Body: "fixed", CreatedAt: time.Now()},
			},
		},
		mr: &domain.GitLabMR{
			IID:          7,
			Title:        "MR",
			HeadSHA:      "new-head",
			TargetBranch: "main",
		},
		project: &domain.GitLabProject{PathWithNS: "group/project"},
	}
	agent := &fakeReplyAgent{reply: "looks good"}
	repoManager := &fakeReplyRepoManager{files: map[string]string{"pkg/service.go": content}}

	pipeline := NewPipeline(PipelineDeps{
		Config:        testReplyConfig(),
		ReplyJobStore: replyStore,
		RepoSettings:  repoSettings,
		FeedbackStore: feedbackStore,
		GitLabClient:  gitlabClient,
		ReplyAgent:    agent,
		RepoManager:   repoManager,
	})

	filePath := "pkg/service.go"
	line := 4
	job := &domain.ReplyJob{
		ID:                 uuid.New(),
		GitLabProjectID:    42,
		MrIID:              7,
		DiscussionID:       "discussion-1",
		TriggerNoteContent: "fixed this",
		BotCommentID:       10,
		BotCommentContent:  "Please fix this",
		BotCommentFilePath: &filePath,
		BotCommentLine:     &line,
	}

	if err := pipeline.Execute(context.Background(), job); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if repoManager.fetchCalls != 1 {
		t.Fatalf("FetchAndCheckout() calls = %d, want 1", repoManager.fetchCalls)
	}
	if repoManager.projectPath != "group/project" {
		t.Fatalf("FetchAndCheckout() projectPath = %s, want group/project", repoManager.projectPath)
	}
	if repoManager.headSHA != "new-head" {
		t.Fatalf("FetchAndCheckout() headSHA = %s, want new-head", repoManager.headSHA)
	}
	if !strings.Contains(agent.lastInput.LatestCodeContext, "4:     return nil") {
		t.Fatalf("LatestCodeContext = %q, want line context from latest checkout", agent.lastInput.LatestCodeContext)
	}
	if gitlabClient.postReplyCalls != 1 {
		t.Fatalf("PostReply() calls = %d, want 1", gitlabClient.postReplyCalls)
	}
	if repoSettings.incrementFeedbackCalls != 1 {
		t.Fatalf("IncrementFeedbackCount() calls = %d, want 1", repoSettings.incrementFeedbackCalls)
	}
}

func TestPipelineExecuteDoesNotRetryAfterReplyWasPostedWhenCompletionPersistFails(t *testing.T) {
	replyStore := &fakeReplyJobStore{
		updateCompletedErr: errors.New("db unavailable"),
	}
	gitlabClient := &fakeReplyGitLabClient{
		discussion: &domain.GitLabDiscussion{
			ID: "discussion-2",
			Notes: []domain.GitLabNote{
				{ID: 20, AuthorName: "bot", Body: "Please fix this", CreatedAt: time.Now()},
			},
		},
		mr: &domain.GitLabMR{IID: 7, Title: "MR", HeadSHA: "head"},
	}

	pipeline := NewPipeline(PipelineDeps{
		Config:        testReplyConfig(),
		ReplyJobStore: replyStore,
		RepoSettings:  &fakeReplyRepoSettingsStore{},
		FeedbackStore: &fakeReplyFeedbackStore{},
		GitLabClient:  gitlabClient,
		ReplyAgent:    &fakeReplyAgent{reply: "acknowledged"},
		RepoManager:   &fakeReplyRepoManager{},
	})

	job := &domain.ReplyJob{
		ID:                 uuid.New(),
		GitLabProjectID:    42,
		MrIID:              7,
		DiscussionID:       "discussion-2",
		TriggerNoteContent: "thanks",
		BotCommentID:       20,
		BotCommentContent:  "Please fix this",
	}

	if err := pipeline.Execute(context.Background(), job); err != nil {
		t.Fatalf("Execute() error = %v, want nil after reply is already posted", err)
	}
	if gitlabClient.postReplyCalls != 1 {
		t.Fatalf("PostReply() calls = %d, want 1", gitlabClient.postReplyCalls)
	}
	if replyStore.updateCompletedCalls != 1 {
		t.Fatalf("UpdateCompleted() calls = %d, want 1", replyStore.updateCompletedCalls)
	}
}

func TestPipelineExecuteDoesNotAutoResolveAcknowledgeOrRejectReplies(t *testing.T) {
	tests := []struct {
		name           string
		trigger        string
		expectedIntent domain.ReplyIntent
	}{
		{
			name:           "acknowledge reply",
			trigger:        "noted, will address later",
			expectedIntent: domain.IntentAcknowledge,
		},
		{
			name:           "reject reply",
			trigger:        "this is intentional by design",
			expectedIntent: domain.IntentReject,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replyStore := &fakeReplyJobStore{}
			repoSettings := &fakeReplyRepoSettingsStore{}
			feedbackStore := &fakeReplyFeedbackStore{}
			gitlabClient := &fakeReplyGitLabClient{
				discussion: &domain.GitLabDiscussion{
					ID: "discussion-3",
					Notes: []domain.GitLabNote{
						{ID: 30, AuthorName: "bot", Body: "Please fix this", CreatedAt: time.Now()},
						{ID: 31, AuthorName: "dev", Body: tt.trigger, CreatedAt: time.Now()},
					},
				},
				mr: &domain.GitLabMR{IID: 7, Title: "MR", HeadSHA: "head"},
			}

			pipeline := NewPipeline(PipelineDeps{
				Config:        testReplyConfig(),
				ReplyJobStore: replyStore,
				RepoSettings:  repoSettings,
				FeedbackStore: feedbackStore,
				GitLabClient:  gitlabClient,
				ReplyAgent:    &fakeReplyAgent{reply: "thanks for the update"},
				RepoManager:   &fakeReplyRepoManager{},
			})

			job := &domain.ReplyJob{
				ID:                 uuid.New(),
				GitLabProjectID:    42,
				MrIID:              7,
				DiscussionID:       "discussion-3",
				TriggerNoteContent: tt.trigger,
				BotCommentID:       30,
				BotCommentContent:  "Please fix this",
			}

			if err := pipeline.Execute(context.Background(), job); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if gitlabClient.postReplyCalls != 1 {
				t.Fatalf("PostReply() calls = %d, want 1", gitlabClient.postReplyCalls)
			}
			if feedbackStore.updateSignalCalls != 1 {
				t.Fatalf("UpdateSignal() calls = %d, want 1", feedbackStore.updateSignalCalls)
			}
			if replyStore.updateCompletedCalls != 1 {
				t.Fatalf("UpdateCompleted() calls = %d, want 1", replyStore.updateCompletedCalls)
			}
			if replyStore.lastCompletedIntent != tt.expectedIntent {
				t.Fatalf("UpdateCompleted() intent = %q, want %q", replyStore.lastCompletedIntent, tt.expectedIntent)
			}
			if replyStore.lastCompletedSignal == domain.FeedbackSignalNeutral {
				t.Fatalf("UpdateCompleted() signal = %q, want non-neutral feedback persistence", replyStore.lastCompletedSignal)
			}
			if repoSettings.incrementFeedbackCalls != 1 {
				t.Fatalf("IncrementFeedbackCount() calls = %d, want 1", repoSettings.incrementFeedbackCalls)
			}
			if gitlabClient.resolveDiscussionCalls != 0 {
				t.Fatalf("ResolveDiscussion() calls = %d, want 0", gitlabClient.resolveDiscussionCalls)
			}
		})
	}
}

func TestPipelineExecutePersistsThreadStateTransition(t *testing.T) {
	replyStore := &fakeReplyJobStore{}
	feedbackStore := &fakeReplyFeedbackStore{
		feedback: &domain.ReviewFeedback{ThreadState: domain.Ptr(domain.ThreadStateOpen)},
	}
	gitlabClient := &fakeReplyGitLabClient{
		discussion: &domain.GitLabDiscussion{
			ID: "discussion-4",
			Notes: []domain.GitLabNote{
				{ID: 40, AuthorName: "bot", Body: "Please fix this", CreatedAt: time.Now()},
				{ID: 41, AuthorName: "dev", Body: "fixed", CreatedAt: time.Now()},
			},
		},
		mr: &domain.GitLabMR{IID: 7, Title: "MR", HeadSHA: "head"},
	}

	pipeline := NewPipeline(PipelineDeps{
		Config:        testReplyConfig(),
		ReplyJobStore: replyStore,
		RepoSettings:  &fakeReplyRepoSettingsStore{},
		FeedbackStore: feedbackStore,
		GitLabClient:  gitlabClient,
		ReplyAgent:    &fakeReplyAgent{reply: "thanks"},
		RepoManager:   &fakeReplyRepoManager{},
	})

	job := &domain.ReplyJob{
		ID:                 uuid.New(),
		GitLabProjectID:    42,
		MrIID:              7,
		DiscussionID:       "discussion-4",
		TriggerNoteContent: "fixed",
		BotCommentID:       40,
		BotCommentContent:  "Please fix this",
	}

	if err := pipeline.Execute(context.Background(), job); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if replyStore.lastThreadStateBefore != domain.ThreadStateOpen {
		t.Fatalf("before state = %q, want %q", replyStore.lastThreadStateBefore, domain.ThreadStateOpen)
	}
	if replyStore.lastThreadStateAfter != domain.ThreadStatePendingVerification {
		t.Fatalf("after state = %q, want %q", replyStore.lastThreadStateAfter, domain.ThreadStatePendingVerification)
	}
	if feedbackStore.lastUpdatedThreadState != domain.ThreadStatePendingVerification {
		t.Fatalf("feedback thread state = %q, want %q", feedbackStore.lastUpdatedThreadState, domain.ThreadStatePendingVerification)
	}
}

func TestPipelineExecutePersistsDismissedThreadStateForReject(t *testing.T) {
	replyStore := &fakeReplyJobStore{}
	feedbackStore := &fakeReplyFeedbackStore{
		feedback: &domain.ReviewFeedback{ThreadState: domain.Ptr(domain.ThreadStateOpen)},
	}
	gitlabClient := &fakeReplyGitLabClient{
		discussion: &domain.GitLabDiscussion{
			ID: "discussion-5",
			Notes: []domain.GitLabNote{
				{ID: 50, AuthorName: "bot", Body: "Please fix this", CreatedAt: time.Now()},
				{ID: 51, AuthorName: "dev", Body: "this is intentional by design", CreatedAt: time.Now()},
			},
		},
		mr: &domain.GitLabMR{IID: 7, Title: "MR", HeadSHA: "head"},
	}

	pipeline := NewPipeline(PipelineDeps{
		Config:        testReplyConfig(),
		ReplyJobStore: replyStore,
		RepoSettings:  &fakeReplyRepoSettingsStore{},
		FeedbackStore: feedbackStore,
		GitLabClient:  gitlabClient,
		ReplyAgent:    &fakeReplyAgent{reply: "understood"},
		RepoManager:   &fakeReplyRepoManager{},
	})

	job := &domain.ReplyJob{
		ID:                 uuid.New(),
		GitLabProjectID:    42,
		MrIID:              7,
		DiscussionID:       "discussion-5",
		TriggerNoteContent: "this is intentional by design",
		BotCommentID:       50,
		BotCommentContent:  "Please fix this",
	}

	if err := pipeline.Execute(context.Background(), job); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if replyStore.lastThreadStateAfter != domain.ThreadStateDismissed {
		t.Fatalf("after state = %q, want %q", replyStore.lastThreadStateAfter, domain.ThreadStateDismissed)
	}
}

func TestPipelineExecutePersistsAcknowledgedThreadState(t *testing.T) {
	replyStore := &fakeReplyJobStore{}
	feedbackStore := &fakeReplyFeedbackStore{
		feedback: &domain.ReviewFeedback{ThreadState: domain.Ptr(domain.ThreadStateOpen)},
	}
	gitlabClient := &fakeReplyGitLabClient{
		discussion: &domain.GitLabDiscussion{
			ID: "discussion-6",
			Notes: []domain.GitLabNote{
				{ID: 60, AuthorName: "bot", Body: "Please fix this", CreatedAt: time.Now()},
				{ID: 61, AuthorName: "dev", Body: "noted, will address later", CreatedAt: time.Now()},
			},
		},
		mr: &domain.GitLabMR{IID: 7, Title: "MR", HeadSHA: "head"},
	}

	pipeline := NewPipeline(PipelineDeps{
		Config:        testReplyConfig(),
		ReplyJobStore: replyStore,
		RepoSettings:  &fakeReplyRepoSettingsStore{},
		FeedbackStore: feedbackStore,
		GitLabClient:  gitlabClient,
		ReplyAgent:    &fakeReplyAgent{reply: "thanks for the update"},
		RepoManager:   &fakeReplyRepoManager{},
	})

	job := &domain.ReplyJob{
		ID:                 uuid.New(),
		GitLabProjectID:    42,
		MrIID:              7,
		DiscussionID:       "discussion-6",
		TriggerNoteContent: "noted, will address later",
		BotCommentID:       60,
		BotCommentContent:  "Please fix this",
	}

	if err := pipeline.Execute(context.Background(), job); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if replyStore.lastThreadStateAfter != domain.ThreadStateAcknowledged {
		t.Fatalf("after state = %q, want %q", replyStore.lastThreadStateAfter, domain.ThreadStateAcknowledged)
	}
}

func testReplyConfig() config.Config {
	return config.Config{
		LLM: config.LLMConfig{
			DefaultProvider: "openai",
			DefaultModel:    "gpt-4o",
			OpenAIAPIKey:    "test-key",
			OpenAIAPIKeys:   []string{"test-key"},
			OpenAIBaseURL:   "http://127.0.0.1",
			ContextWindowSizes: map[string]int{
				"gpt-4o": 128000,
			},
		},
		Review: config.ReviewConfig{
			ResponseLanguage: "en",
		},
	}
}
