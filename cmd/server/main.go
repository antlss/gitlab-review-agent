package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/joho/godotenv"
	"github.com/samber/do/v2"

	"github.com/antlss/gitlab-review-agent/internal/config"
	"github.com/antlss/gitlab-review-agent/internal/di"
	"github.com/antlss/gitlab-review-agent/internal/domain"
	"github.com/antlss/gitlab-review-agent/internal/handler/cron"
	"github.com/antlss/gitlab-review-agent/internal/handler/webhook"
	"github.com/antlss/gitlab-review-agent/internal/handler/worker"
	"github.com/antlss/gitlab-review-agent/internal/pkg/queue"
)

func main() {
	_ = godotenv.Load()

	injector := do.New(
		di.ConfigPackage,
		di.InfraPackage,
		di.CorePackage,
		di.HandlerPackage,
	)

	cfg := do.MustInvoke[*config.Config](injector)
	if err := cfg.ValidateForServer(); err != nil {
		slog.Error("invalid server configuration", "error", err)
		os.Exit(1)
	}
	slog.Info("starting ai-review-agent server", "store_driver", cfg.Store.Driver)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker pool
	pool := do.MustInvoke[*worker.Pool](injector)
	pool.Start(ctx)

	// Start cron runner
	cronRunner := do.MustInvoke[*cron.Runner](injector)
	cronRunner.Start()
	defer cronRunner.Stop()

	// Webhook handler needs server context for bounding background goroutines
	webhookHandler := webhook.NewHandler(webhook.HandlerDeps{
		WebhookSecret:      cfg.GitLab.WebhookSecret,
		BotUserID:          cfg.GitLab.BotUserID,
		ReviewTriggerLabel: cfg.Review.TriggerLabel,
		RepoSettings:       do.MustInvoke[domain.RepositorySettingsStore](injector),
		ReviewJobStore:     do.MustInvoke[domain.ReviewJobStore](injector),
		ReplyJobStore:      do.MustInvoke[domain.ReplyJobStore](injector),
		GitLabClient:       do.MustInvoke[domain.GitLabClient](injector),
		Queue:              do.MustInvoke[*queue.Queue](injector),
		ServerCtx:          ctx,
	})

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Post("/webhook/gitlab", webhookHandler.HandleGitLabEvent)
	r.Get("/health", webhookHandler.HandleHealth)

	addr := fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port)
	server := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	slog.Info("server listening", "addr", addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-sigCh
	slog.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	server.Shutdown(shutdownCtx)
	cancel()

	jobQueue := do.MustInvoke[*queue.Queue](injector)
	jobQueue.Close()
	webhookHandler.Shutdown()

	workerDone := make(chan struct{})
	go func() { pool.Wait(); close(workerDone) }()
	select {
	case <-workerDone:
		slog.Info("all workers stopped gracefully")
	case <-shutdownCtx.Done():
		slog.Warn("shutdown timeout — some workers may still be running")
	}

	slog.Info("server stopped")
}
