package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const maxRetries = 3

// RateLimitError is returned by doRequest when the API responds with HTTP 429.
// It is NOT retried inside doRequest so that BalancedClient can immediately
// rotate to the next API key rather than waiting out the backoff on the same key.
type RateLimitError struct {
	StatusCode int
	Body       string
}

func (e *RateLimitError) Error() string {
	body := e.Body
	if len(body) > 200 {
		body = body[:200] + "..."
	}
	return fmt.Sprintf("rate_limited (HTTP %d): %s", e.StatusCode, body)
}

// IsRateLimitError reports whether err is or wraps a *RateLimitError.
func IsRateLimitError(err error) bool {
	var rle *RateLimitError
	return errors.As(err, &rle)
}

// doRequest performs an HTTP request with exponential-backoff retry on transient
// server errors (503, 529) and network failures.
// HTTP 429 is returned immediately as *RateLimitError without internal retry —
// BalancedClient handles key rotation and backoff at a higher level.
// body bytes are re-used for each attempt since bytes.NewReader is seekable.
func doRequest(
	ctx context.Context,
	client *http.Client,
	method, url string,
	body []byte,
	headers map[string]string,
) ([]byte, error) {
	backoff := 2 * time.Second

	slog.Debug("LLM API request", "method", method, "url", url, "payload_bytes", len(body))

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			if attempt < maxRetries {
				slog.Warn("LLM request failed, retrying", "attempt", attempt+1, "error", err)
				if err := sleepCtx(ctx, backoff); err != nil {
					return nil, err
				}
				backoff *= 2
				continue
			}
			return nil, fmt.Errorf("http request: %w", err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}

		// 429: surface immediately as RateLimitError — no local retry
		if resp.StatusCode == 429 {
			slog.Warn("LLM rate limited", "attempt", attempt+1, "status", 429)
			return nil, &RateLimitError{StatusCode: 429, Body: string(respBody)}
		}

		// Retry on transient server errors (NOT rate limits)
		if attempt < maxRetries && (resp.StatusCode == 503 || resp.StatusCode == 529) {
			slog.Warn("LLM server error, retrying",
				"attempt", attempt+1, "status", resp.StatusCode)
			if err := sleepCtx(ctx, backoff); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
		}

		slog.Debug("LLM API response", "status", resp.StatusCode, "response_bytes", len(respBody), "attempt", attempt+1)

		return respBody, nil
	}

	return nil, fmt.Errorf("max retries exhausted")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
