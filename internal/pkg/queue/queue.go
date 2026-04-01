package queue

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

const (
	// lockTTLSeconds must exceed the maximum expected job processing duration.
	// A large PR with 30 iterations at ~60s per LLM call can take ~30 minutes.
	lockTTL = 30 * time.Minute
)

type lockEntry struct {
	workerID  string
	expiresAt time.Time
}

type dlqEntry struct {
	Job      domain.QueueJob `json:"job"`
	Error    string          `json:"error"`
	FailedAt time.Time       `json:"failedAt"`
}

type Queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	queues map[int64][]domain.QueueJob // per-project FIFO
	locks  map[int64]lockEntry         // per-project processing lock
	dlq    map[int64][]dlqEntry        // per-project dead letter queue
	closed bool
}

func New() *Queue {
	q := &Queue{
		queues: make(map[int64][]domain.QueueJob),
		locks:  make(map[int64]lockEntry),
		dlq:    make(map[int64][]dlqEntry),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Close signals all waiting workers to wake up and prevents new enqueues.
// Call this during graceful shutdown.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// Enqueue adds a job to the tail of the project's queue (FIFO).
func (q *Queue) Enqueue(_ context.Context, job domain.QueueJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return fmt.Errorf("queue is closed")
	}

	q.queues[job.ProjectID] = append(q.queues[job.ProjectID], job)
	q.cond.Broadcast() // wake up waiting workers
	return nil
}

// GetNextJob tries to acquire a job from any available project queue.
// Returns (job, projectID, nil) if a job was found, (nil, 0, nil) if no jobs available.
func (q *Queue) GetNextJob(ctx context.Context, workerID string) (*domain.QueueJob, int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Single goroutine to wake us on context cancellation (avoids per-loop churn)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.cond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	for {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}

		if q.closed {
			return nil, 0, nil
		}

		// Try to find a job from any project queue
		for projectID, jobs := range q.queues {
			if len(jobs) == 0 {
				continue
			}

			// Check if project is locked (with TTL expiry)
			if entry, locked := q.locks[projectID]; locked {
				if time.Now().Before(entry.expiresAt) {
					continue // still locked
				}
				slog.Warn("lock expired", "project_id", projectID, "worker_id", entry.workerID)
				delete(q.locks, projectID)
			}

			// Pop from head (FIFO) and acquire lock
			job := jobs[0]
			if len(jobs) == 1 {
				delete(q.queues, projectID)
			} else {
				q.queues[projectID] = jobs[1:]
			}

			q.locks[projectID] = lockEntry{
				workerID:  workerID,
				expiresAt: time.Now().Add(lockTTL),
			}

			return &job, projectID, nil
		}

		q.cond.Wait()
	}
}

// ReleaseLock releases the lock for a project, but only if owned by the given worker.
func (q *Queue) ReleaseLock(_ context.Context, projectID int64, workerID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	entry, exists := q.locks[projectID]
	if !exists {
		return nil
	}
	if entry.workerID == workerID {
		delete(q.locks, projectID)
	}
	return nil
}

// SendToDLQ moves a failed job to the dead letter queue.
func (q *Queue) SendToDLQ(_ context.Context, job domain.QueueJob, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.dlq[job.ProjectID] = append(q.dlq[job.ProjectID], dlqEntry{
		Job:      job,
		Error:    errMsg,
		FailedAt: time.Now(),
	})
	return nil
}

// ListLocks returns all currently held locks (excluding expired ones).
func (q *Queue) ListLocks(_ context.Context) (map[int64]string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	locks := make(map[int64]string)
	for pid, entry := range q.locks {
		if now.Before(entry.expiresAt) {
			locks[pid] = entry.workerID
		}
	}
	return locks, nil
}

// ForceReleaseLock removes a lock regardless of ownership.
func (q *Queue) ForceReleaseLock(_ context.Context, projectID int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	delete(q.locks, projectID)
	return nil
}
