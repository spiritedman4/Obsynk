package drive

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

// shrinkBackoffForTest lowers the retry delays so tests exercise the
// retry/give-up path in milliseconds instead of real seconds, restoring the
// production values afterward.
func shrinkBackoffForTest(t *testing.T) {
	t.Helper()
	origRetries, origBase, origMax := maxRetries, baseDelay, maxDelay
	maxRetries = 3
	baseDelay = 1 * time.Millisecond
	maxDelay = 5 * time.Millisecond
	t.Cleanup(func() {
		maxRetries, baseDelay, maxDelay = origRetries, origBase, origMax
	})
}

func TestWithRetry_SucceedsWithoutRetry(t *testing.T) {
	calls := 0
	result, err := withRetry(context.Background(), func() (int, error) {
		calls++
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Errorf("result = %d, want 42", result)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestWithRetry_RetriesOnRateLimitThenSucceeds(t *testing.T) {
	shrinkBackoffForTest(t)

	calls := 0
	result, err := withRetry(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return 0, &googleapi.Error{Code: 429}
		}
		return 7, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 7 {
		t.Errorf("result = %d, want 7", result)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetry_GivesUpAfterMaxRetries(t *testing.T) {
	shrinkBackoffForTest(t)

	calls := 0
	_, err := withRetry(context.Background(), func() (int, error) {
		calls++
		return 0, &googleapi.Error{Code: 403}
	})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if calls != maxRetries+1 {
		t.Errorf("calls = %d, want %d (initial attempt + %d retries)", calls, maxRetries+1, maxRetries)
	}
}

func TestWithRetry_NonRetryableFailsImmediately(t *testing.T) {
	shrinkBackoffForTest(t)

	calls := 0
	wantErr := &googleapi.Error{Code: 404}
	_, err := withRetry(context.Background(), func() (int, error) {
		calls++
		return 0, wantErr
	})
	if !errors.Is(err, error(wantErr)) && err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (non-retryable error should not retry)", calls)
	}
}

func TestWithRetry_ContextCancellationStopsRetries(t *testing.T) {
	origMax := maxRetries
	maxRetries = 1000 // effectively unbounded for this test
	baseDelay = 20 * time.Millisecond
	maxDelay = 50 * time.Millisecond
	t.Cleanup(func() { maxRetries = origMax })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := withRetry(ctx, func() (int, error) {
		return 0, &googleapi.Error{Code: 429}
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v, expected to stop promptly once the context deadline passed", elapsed)
	}
}
