package llm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

const (
	// perKeyConcurrency is the maximum number of concurrent LLM calls per API key.
	// Total global cap = ClientCount × perKeyConcurrency.
	// This prevents overloading keys when many chunks run in parallel.
	perKeyConcurrency = 2

	// allKeysRateLimitedBackoff is the wait before a final retry when every key
	// has been tried and all returned rate-limit errors.
	allKeysRateLimitedBackoff = 5 * time.Second
)

// BalancedClient distributes LLM calls across multiple clients of the same
// provider/model using a least-connections strategy.
//
// Rate-limit handling: when a key returns HTTP 429, the call is immediately
// retried on the next least-loaded key without any backoff delay. Only after
// all keys have been tried does BalancedClient back off and make one final
// attempt on the least-loaded key.
//
// Concurrency cap: a semaphore limits total in-flight calls to
// ClientCount × perKeyConcurrency, preventing pile-ups that trigger
// rate limits in the first place.
type BalancedClient struct {
	clients []clientEntry
	sem     *semaphore.Weighted
	mu      sync.Mutex
}

type clientEntry struct {
	client   domain.LLMClient
	inFlight atomic.Int64
	total    atomic.Int64
}

// NewBalancedClient creates a load-balanced wrapper around multiple LLM clients.
// All clients must use the same model — the balancer only distributes API key load.
func NewBalancedClient(clients []domain.LLMClient) (*BalancedClient, error) {
	if len(clients) == 0 {
		return nil, fmt.Errorf("at least one LLM client is required")
	}

	entries := make([]clientEntry, len(clients))
	for i, c := range clients {
		entries[i] = clientEntry{client: c}
	}

	cap := int64(len(clients)) * perKeyConcurrency

	return &BalancedClient{
		clients: entries,
		sem:     semaphore.NewWeighted(cap),
	}, nil
}

// pick returns the client with the fewest in-flight requests, excluding any
// indices marked as tried. If tried is nil, all clients are candidates.
func (b *BalancedClient) pick(tried []bool) (*clientEntry, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	best := -1
	var bestFlight, bestTotal int64

	for i := range b.clients {
		if tried != nil && tried[i] {
			continue
		}
		flight := b.clients[i].inFlight.Load()
		total := b.clients[i].total.Load()
		if best == -1 || flight < bestFlight || (flight == bestFlight && total < bestTotal) {
			best = i
			bestFlight = flight
			bestTotal = total
		}
	}

	if best == -1 {
		return nil, -1
	}
	return &b.clients[best], best
}

func (b *BalancedClient) Chat(ctx context.Context, req domain.ChatRequest) (*domain.ChatResponse, error) {
	// Acquire a global concurrency slot before doing anything.
	// This queues callers when all slots are occupied rather than letting
	// them pile up and hit rate limits simultaneously.
	if err := b.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("acquire LLM concurrency slot: %w", err)
	}
	defer b.sem.Release(1)

	tried := make([]bool, len(b.clients))
	var lastErr error

	// Try each key once in least-connections order.
	// On 429, rotate to the next available key immediately (no sleep).
	for attempt := 0; attempt < len(b.clients); attempt++ {
		entry, idx := b.pick(tried)
		if entry == nil {
			break
		}
		tried[idx] = true

		req.Model = entry.client.ModelName()
		entry.inFlight.Add(1)
		entry.total.Add(1)
		resp, err := entry.client.Chat(ctx, req)
		entry.inFlight.Add(-1)

		if err == nil {
			slog.Debug("balancer: request succeeded",
				"key_index", idx,
				"attempt", attempt+1,
				"total_keys", len(b.clients),
			)
			return resp, nil
		}

		lastErr = err
		if !IsRateLimitError(err) {
			// Non-rate-limit errors (auth, invalid request, etc.) are not
			// retried on other keys since they would also fail.
			return nil, err
		}

		slog.Warn("balancer: key rate limited, rotating to next key",
			"key_index", idx,
			"attempt", attempt+1,
			"remaining_keys", len(b.clients)-attempt-1,
		)
	}

	// All keys have been tried and all returned rate limits.
	// Back off briefly, then make one final attempt on the least-loaded key.
	if IsRateLimitError(lastErr) {
		slog.Warn("balancer: all keys rate limited, backing off before final retry",
			"keys", len(b.clients),
			"backoff", allKeysRateLimitedBackoff,
		)
		if err := sleepCtx(ctx, allKeysRateLimitedBackoff); err != nil {
			return nil, err
		}
		entry, idx := b.pick(nil)
		if entry != nil {
			req.Model = entry.client.ModelName()
			entry.inFlight.Add(1)
			entry.total.Add(1)
			resp, err := entry.client.Chat(ctx, req)
			entry.inFlight.Add(-1)
			if err == nil {
				return resp, nil
			}
			slog.Warn("balancer: final retry also rate limited", "key_index", idx)
			lastErr = err
		}
	}

	return nil, lastErr
}

func (b *BalancedClient) ModelName() string      { return b.clients[0].client.ModelName() }
func (b *BalancedClient) ContextWindowSize() int { return b.clients[0].client.ContextWindowSize() }

// ClientCount returns the number of underlying API key clients.
func (b *BalancedClient) ClientCount() int { return len(b.clients) }
