package drive

import (
	"context"
	"fmt"
	"strings"

	drivev3 "google.golang.org/api/drive/v3"
)

// EnsureRootFolder finds (by name + parent) or creates the vault's
// dedicated Drive folder, returning its file ID. Idempotent: safe to call
// on every daemon start. parentID may be empty to search/create under
// Drive's root ("My Drive").
func (c *Client) EnsureRootFolder(ctx context.Context, name, parentID string) (string, error) {
	return c.ensureFolder(ctx, name, parentID)
}

// EnsureSubfolders creates any intermediate folder path components
// mirroring dirRelPath under rootID (idempotent, caching nothing across
// calls -- callers doing many files in the same directory should cache the
// result themselves), returning the leaf folder's ID. An empty or "."
// dirRelPath returns rootID unchanged.
func (c *Client) EnsureSubfolders(ctx context.Context, rootID, dirRelPath string) (string, error) {
	dirRelPath = strings.Trim(dirRelPath, "/")
	if dirRelPath == "" || dirRelPath == "." {
		return rootID, nil
	}

	current := rootID
	for _, part := range strings.Split(dirRelPath, "/") {
		if part == "" {
			continue
		}
		id, err := c.ensureFolder(ctx, part, current)
		if err != nil {
			return "", err
		}
		current = id
	}
	return current, nil
}

func (c *Client) ensureFolder(ctx context.Context, name, parentID string) (string, error) {
	query := fmt.Sprintf("name = '%s' and mimeType = 'application/vnd.google-apps.folder' and trashed = false", escapeQueryValue(name))
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
		MimeType: "application/vnd.google-apps.folder",
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
