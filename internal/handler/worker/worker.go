package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/antlss/gitlab-review-agent/internal/pkg/queue"
	"github.com/antlss/gitlab-review-agent/internal/domain"
)

// ReviewPipelineExecutor is called for review jobs.
type ReviewPipelineExecutor interface {
	Execute(ctx context.Context, job *domain.ReviewJob) error
}

// ReplyPipelineExecutor is called for reply jobs.
type ReplyPipelineExecutor interface {
	Execute(ctx context.Context, job *domain.ReplyJob) error
}

type PoolDeps struct {
	Queue          *queue.Queue
	ReviewPipeline ReviewPipelineExecutor
	ReplyPipeline  ReplyPipelineExecutor
	ReviewJobStore domain.ReviewJobStore
	ReplyJobStore  domain.ReplyJobStore
}

type runner struct {
	id             string
	queue          *queue.Queue
	reviewPipeline ReviewPipelineExecutor
	replyPipeline  ReplyPipelineExecutor
	reviewJobStore domain.ReviewJobStore
	replyJobStore  domain.ReplyJobStore
}

type Pool struct {
	runners []*runner
	wg      sync.WaitGroup
}

func NewPool(n int, deps PoolDeps) *Pool {
	p := &Pool{}
	for i := 0; i < n; i++ {
		r := &runner{
			id:             uuid.New().String(),
			queue:          deps.Queue,
			reviewPipeline: deps.ReviewPipeline,
			replyPipeline:  deps.ReplyPipeline,
			reviewJobStore: deps.ReviewJobStore,
			replyJobStore:  deps.ReplyJobStore,
		}
		p.runners = append(p.runners, r)
	}
	return p
}

func (p *Pool) Start(ctx context.Context) {
	for _, r := range p.runners {
		p.wg.Add(1)
		go func(r *runner) {
			defer p.wg.Done()
			r.run(ctx)
		}(r)
	}
}

func (p *Pool) Wait() {
	p.wg.Wait()
}

func (r *runner) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// GetNextJob blocks until a job is available or ctx is cancelled
		job, projectID, err := r.queue.GetNextJob(ctx, r.id)
		if err != nil {
			if ctx.Err() != nil {
				return // shutting down
			}
			slog.Error("failed to get next job", "error", err)
			continue
		}
		if job == nil {
			// Queue closed or context cancelled
			return
		}

		if err := r.processJob(ctx, job); err != nil {
			slog.Error("job failed",
				"job_id", job.JobID.String(),
				"job_type", string(job.Type),
				"error", err,
			)
			_ = r.queue.SendToDLQ(ctx, *job, err.Error())
		}

		_ = r.queue.ReleaseLock(ctx, projectID, r.id)
	}
}

func (r *runner) processJob(ctx context.Context, job *domain.QueueJob) error {
	switch job.Type {
	case domain.QueueJobTypeReview:
		reviewJob, err := r.reviewJobStore.GetByID(ctx, job.JobID)
		if err != nil {
			return fmt.Errorf("load review job: %w", err)
		}
		if reviewJob == nil {
			return fmt.Errorf("review job not found: %s", job.JobID)
		}
		return r.reviewPipeline.Execute(ctx, reviewJob)

	case domain.QueueJobTypeReply:
		replyJob, err := r.replyJobStore.GetByID(ctx, job.JobID)
		if err != nil {
			return fmt.Errorf("load reply job: %w", err)
		}
		if replyJob == nil {
			return fmt.Errorf("reply job not found: %s", job.JobID)
		}
		return r.replyPipeline.Execute(ctx, replyJob)

	default:
		return fmt.Errorf("unknown job type: %s", job.Type)
	}
}
