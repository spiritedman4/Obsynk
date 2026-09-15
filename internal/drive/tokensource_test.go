package drive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type memCache struct{ tok *oauth2.Token }

func (m *memCache) Load() (*oauth2.Token, error) { return m.tok, nil }
func (m *memCache) Save(t *oauth2.Token) error   { m.tok = t; return nil }

// A token source must not be bound to the context that happened to be in
// scope when the client was constructed. oauth2 captures that context and
// reuses it for every refresh, so a request-scoped or timeout context
// yields a client that works until its access token expires and then fails
// every call with "oauth2.googleapis.com/token: context canceled" -- which
// is exactly what a manual sync hit, an hour after each daemon start.
func TestTokenSourceOutlivesTheConstructionContext(t *testing.T) {
	t.Parallel()

	var refreshes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	cfg := &oauth2.Config{
		ClientID:     "id",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
	}
	expired := &oauth2.Token{
		AccessToken:  "stale",
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(-time.Hour),
	}

	// Construction-time context, cancelled the way NewServer's startup
	// timeout and every RPC context are once they return.
	ctx, cancel := context.WithCancel(context.Background())
	cache := &memCache{tok: expired}
	ts := &savingTokenSource{inner: cfg.TokenSource(context.WithoutCancel(ctx), expired), cache: cache, last: expired}
	cancel()

	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("refresh after the construction context was cancelled: %v", err)
	}
	if tok.AccessToken != "fresh" {
		t.Errorf("access token = %q, want the refreshed one", tok.AccessToken)
	}
	if refreshes != 1 {
		t.Errorf("token endpoint hit %d times, want 1", refreshes)
	}
	if cache.tok.AccessToken != "fresh" {
		t.Error("refreshed token was not persisted to the cache")
	}
}
