package drive

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"google.golang.org/api/googleapi"
)

// Package-level (not const) so tests can shrink the delays instead of a
// real test run taking tens of seconds to exercise the retry/give-up path.
var (
	maxRetries = 5
	baseDelay  = 500 * time.Millisecond
	maxDelay   = 30 * time.Second
)

// withRetry wraps a Drive API call, retrying on 403 (rate limit exceeded)
// and 429 responses with exponential backoff plus jitter, per Google's
// documented guidance for the Drive API. Non-retryable errors (404, 401,
// invalid_grant, etc.) pass straight through on the first attempt.
func withRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	delay := baseDelay

	for attempt := 0; ; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}
		if attempt >= maxRetries || !isRetryable(err) {
			return zero, err
		}

		wait := delay + time.Duration(rand.Int63n(int64(delay)/2+1))
		if wait > maxDelay {
			wait = maxDelay
		}

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(wait):
		}

		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

func isRetryable(err error) bool {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code == 403 || apiErr.Code == 429
	}
	return false
}
