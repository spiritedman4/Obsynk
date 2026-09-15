package drive

import (
	"context"
	"fmt"

	drivev3 "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// Client is a thin wrapper around the Google Drive API v3 service.
type Client struct {
	svc *drivev3.Service
}

// NewClient builds a Drive client from a cached OAuth token, refreshing it
// automatically as needed. Returns an error if no token is cached yet --
// the caller should run LoginInteractive first in that case.
//
// The token source is deliberately detached from ctx's cancellation.
// oauth2 captures whatever context it is handed and reuses it for every
// future refresh, so a request-scoped or timeout context -- a startup
// deadline, or an RPC that has since returned -- produces a client that
// works only until its access token expires, then fails every call with
// "oauth2.googleapis.com/token: context canceled". Detaching here means no
// caller can reintroduce that by passing the wrong context. Values are
// preserved, so an oauth2.HTTPClient override still applies; individual API
// calls carry their own contexts and are unaffected.
func NewClient(ctx context.Context, creds Credentials, cache TokenCache) (*Client, error) {
	tok, err := cache.Load()
	if err != nil {
		return nil, fmt.Errorf("drive: loading cached token (run LoginInteractive first): %w", err)
	}

	cfg := oauthConfig(creds, "")
	refreshCtx := context.WithoutCancel(ctx)
	ts := &savingTokenSource{inner: cfg.TokenSource(refreshCtx, tok), cache: cache, last: tok}

	svc, err := drivev3.NewService(refreshCtx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("drive: creating service: %w", err)
	}
	return &Client{svc: svc}, nil
}

// AboutMe returns the connected account's info (currently just enough to
// display which Google account is connected in the plugin's settings tab).
func (c *Client) AboutMe(ctx context.Context) (*drivev3.About, error) {
	about, err := withRetry(ctx, func() (*drivev3.About, error) {
		return c.svc.About.Get().Fields("user").Context(ctx).Do()
	})
	if err != nil {
		return nil, fmt.Errorf("drive: fetching account info: %w", err)
	}
	return about, nil
}
