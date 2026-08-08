// Package drive wraps the Google Drive API v3 client with the pieces the
// sync engine needs: installed-app OAuth2 login, folder lookup/creation,
// and content transfer, all with retry/backoff on Drive's rate-limit
// responses.
package drive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	drivev3 "google.golang.org/api/drive/v3"
)

// Credentials are the OAuth client ID/secret for an installed-app flow,
// provided by the user via the plugin's settings tab (from their own
// Google Cloud project) -- never hardcoded.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// TokenCache persists the OAuth2 token between daemon runs.
type TokenCache interface {
	Load() (*oauth2.Token, error)
	Save(*oauth2.Token) error
}

// FileTokenCache persists the token as JSON at Path, mode 0600.
type FileTokenCache struct {
	Path string
}

func (c FileTokenCache) Load() (*oauth2.Token, error) {
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func (c FileTokenCache) Save(tok *oauth2.Token) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.Path, data, 0o600)
}

func oauthConfig(creds Credentials, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		Endpoint:     google.Endpoint,
		RedirectURL:  redirectURL,
		// drive.file scope: access only to files/folders this app creates
		// or opens, rather than the whole Drive -- narrowest scope that
		// still lets us create and own a dedicated vault folder.
		Scopes: []string{drivev3.DriveFileScope},
	}
}

// LoginInteractive runs the OAuth2 installed-app flow: starts a local
// loopback HTTP listener on an OS-assigned port, builds the authorize URL
// with that port as the redirect URI, invokes onAuthURL with it (so the
// caller -- the gRPC StartLogin handler -- can stream it to the plugin to
// open in the system browser), then blocks until the redirect arrives,
// exchanges the code for a token, and persists it via cache. Returns once
// the token is cached, or when ctx is cancelled.
func LoginInteractive(ctx context.Context, creds Credentials, cache TokenCache, onAuthURL func(url string)) error {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("drive: starting local redirect listener: %w", err)
	}

	port := lis.Addr().(*net.TCPAddr).Port
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/", port)
	cfg := oauthConfig(creds, redirectURL)

	state, err := randomState()
	if err != nil {
		lis.Close()
		return fmt.Errorf("drive: generating oauth state: %w", err)
	}

	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if msg := q.Get("error"); msg != "" {
			fmt.Fprintln(w, "Authorization failed, you can close this tab.")
			select {
			case resultCh <- result{err: fmt.Errorf("drive: authorization denied: %s", msg)}:
			default:
			}
			return
		}
		if q.Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			select {
			case resultCh <- result{err: fmt.Errorf("drive: oauth state mismatch")}:
			default:
			}
			return
		}
		fmt.Fprintln(w, "Connected! You can close this tab and return to Obsidian.")
		select {
		case resultCh <- result{code: q.Get("code")}:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(lis)
	defer srv.Close()

	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	if onAuthURL != nil {
		onAuthURL(authURL)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-resultCh:
		if res.err != nil {
			return res.err
		}
		tok, err := cfg.Exchange(ctx, res.code)
		if err != nil {
			return fmt.Errorf("drive: exchanging code: %w", err)
		}
		if err := cache.Save(tok); err != nil {
			return fmt.Errorf("drive: saving token: %w", err)
		}
		return nil
	}
}

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// savingTokenSource wraps an oauth2.TokenSource, re-persisting the token via
// cache whenever the underlying source refreshes it, so a refreshed access
// token survives a daemon restart without needing another refresh call.
type savingTokenSource struct {
	mu    sync.Mutex
	inner oauth2.TokenSource
	cache TokenCache
	last  *oauth2.Token
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.inner.Token()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	changed := s.last == nil || tok.AccessToken != s.last.AccessToken
	s.last = tok
	s.mu.Unlock()

	if changed {
		_ = s.cache.Save(tok) // best-effort; a cache write failure shouldn't fail the live call
	}
	return tok, nil
}
