package grpcserver

import (
	"context"
	"sync"
	"testing"
	"time"
)

func newTestServer() *Server {
	return &Server{syncSem: make(chan struct{}, 1)}
}

// Two syncs overlapping is what left duplicate folders on Drive: each built
// its own folderCache, so each created its own copy of every directory, and
// every file was uploaded twice.
func TestAcquireSyncSerializesOverlappingSyncs(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	ctx := context.Background()

	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := s.acquireSync(ctx)
			if err != nil {
				t.Errorf("acquireSync: %v", err)
				return
			}
			defer release()

			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()

			time.Sleep(2 * time.Millisecond)

			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if maxInFlight != 1 {
		t.Errorf("%d syncs ran concurrently, want 1", maxInFlight)
	}
}

// A queued batch waits rather than being dropped, so no vault change is lost.
func TestAcquireSyncBlocksUntilReleased(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	release, err := s.acquireSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan struct{})
	go func() {
		r, err := s.acquireSync(context.Background())
		if err == nil {
			r()
		}
		close(got)
	}()

	select {
	case <-got:
		t.Fatal("second sync started while the first still held the lock")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Error("second sync never acquired after release")
	}
}

// A client that disconnects while waiting must not leave the sync path held.
func TestAcquireSyncHonorsCallerCancellation(t *testing.T) {
	t.Parallel()

	s := newTestServer()
	release, err := s.acquireSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.acquireSync(ctx)
		done <- err
	}()

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled waiter reported success")
		}
	case <-time.After(2 * time.Second):
		t.Error("a cancelled waiter blocked forever")
	}
}
