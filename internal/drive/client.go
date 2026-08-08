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
func NewClient(ctx context.Context, creds Credentials, cache TokenCache) (*Client, error) {
	tok, err := cache.Load()
	if err != nil {
		return nil, fmt.Errorf("drive: loading cached token (run LoginInteractive first): %w", err)
	}

	cfg := oauthConfig(creds, "")
	ts := &savingTokenSource{inner: cfg.TokenSource(ctx, tok), cache: cache, last: tok}

	svc, err := drivev3.NewService(ctx, option.WithTokenSource(ts))
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
