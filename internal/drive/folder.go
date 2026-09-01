package drive

import (
	"context"
	"errors"
	"fmt"
	"strings"

	drivev3 "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

// folderMimeType is the mime type Drive gives folders.
const folderMimeType = "application/vnd.google-apps.folder"

// EnsureRootFolder finds (by name + parent) or creates the vault's
// dedicated Drive folder, returning its file ID. Idempotent: safe to call
// on every daemon start. parentID may be empty to search/create under
// Drive's root ("My Drive").
func (c *Client) EnsureRootFolder(ctx context.Context, name, parentID string) (string, error) {
	return c.EnsureChildFolder(ctx, parentID, name)
}

// VerifyFolder reports whether folderID still names a live folder sitting
// directly under parentID, which is how a remembered folder ID is checked
// before it's reused across syncs. An empty parentID skips the parent
// check.
//
// This is a files.get by ID, which -- unlike the files.list search that
// EnsureChildFolder falls back on -- is strongly consistent, so it never
// reports a folder missing merely because Drive's index hasn't caught up.
//
// A folder that's gone (404), trashed, moved elsewhere, or no longer a
// folder returns false with a nil error: the ID is stale, not broken, and
// the caller should fall back to find-or-create. A genuine API failure
// returns an error, so callers don't mistake "couldn't check" for "isn't
// there" and create a duplicate.
func (c *Client) VerifyFolder(ctx context.Context, folderID, parentID string) (bool, error) {
	f, err := withRetry(ctx, func() (*drivev3.File, error) {
		return c.svc.Files.Get(folderID).
			Fields("id, mimeType, trashed, parents").
			Context(ctx).
			Do()
	})
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && (apiErr.Code == 404 || apiErr.Code == 403) {
			// 403 here means the folder still exists but this account can no
			// longer reach it -- unusable either way, same as gone.
			return false, nil
		}
		return false, fmt.Errorf("drive: verifying folder %s: %w", folderID, err)
	}

	if f.Trashed || f.MimeType != folderMimeType {
		return false, nil
	}
	if parentID == "" {
		return true, nil
	}
	for _, p := range f.Parents {
		if p == parentID {
			return true, nil
		}
	}
	return false, nil
}

// EnsureChildFolder finds (by exact name) or creates a single folder named
// name directly under parentID, returning its file ID. parentID may be
// empty to search/create under Drive's root ("My Drive").
//
// Two caveats, both handled by syncengine's folderCache, which is what
// callers inside a sync should go through:
//
// It is only idempotent against itself when called serially. Find-or-create
// is a list-then-create round trip, so two concurrent calls for the same
// missing folder both see an empty list and both create it.
//
// The lookup half is a files.list search, and Drive's search index is
// eventually consistent, so a folder created moments ago may not be found
// and will be created again. Addressing a known folder by ID (VerifyFolder)
// is not subject to that.
func (c *Client) EnsureChildFolder(ctx context.Context, parentID, name string) (string, error) {
	query := fmt.Sprintf("name = '%s' and mimeType = '%s' and trashed = false", escapeQueryValue(name), folderMimeType)
	if parentID != "" {
		query += fmt.Sprintf(" and '%s' in parents", parentID)
	}

	list, err := withRetry(ctx, func() (*drivev3.FileList, error) {
		return c.svc.Files.List().
			Q(query).
			Fields("files(id, name)").
			PageSize(1).
			Context(ctx).
			Do()
	})
	if err != nil {
		return "", fmt.Errorf("drive: listing folder %q: %w", name, err)
	}
	if len(list.Files) > 0 {
		return list.Files[0].Id, nil
	}

	f := &drivev3.File{
		Name:     name,
		MimeType: folderMimeType,
	}
	if parentID != "" {
		f.Parents = []string{parentID}
	}

	created, err := withRetry(ctx, func() (*drivev3.File, error) {
		return c.svc.Files.Create(f).Fields("id").Context(ctx).Do()
	})
	if err != nil {
		return "", fmt.Errorf("drive: creating folder %q: %w", name, err)
	}
	return created.Id, nil
}

func escapeQueryValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return v
}
