package drive

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"context"

	drivev3 "google.golang.org/api/drive/v3"
)

// Upload creates a new file under parentID with the given name and content,
// returning the new file's ID and md5Checksum.
func (c *Client) Upload(ctx context.Context, parentID, name string, r io.Reader, modTime time.Time) (fileID, md5 string, err error) {
	f := &drivev3.File{
		Name:         name,
		Parents:      []string{parentID},
		ModifiedTime: modTime.UTC().Format(time.RFC3339),
	}
	created, err := withRetry(ctx, func() (*drivev3.File, error) {
		return c.svc.Files.Create(f).
			Media(r).
			Fields("id, md5Checksum").
			Context(ctx).
			Do()
	})
	if err != nil {
		return "", "", fmt.Errorf("drive: uploading %q: %w", name, err)
	}
	return created.Id, created.Md5Checksum, nil
}

// UpdateContent replaces the content of an existing file, returning its new
// md5Checksum.
func (c *Client) UpdateContent(ctx context.Context, fileID string, r io.Reader, modTime time.Time) (string, error) {
	f := &drivev3.File{
		ModifiedTime: modTime.UTC().Format(time.RFC3339),
	}
	updated, err := withRetry(ctx, func() (*drivev3.File, error) {
		return c.svc.Files.Update(fileID, f).
			Media(r).
			Fields("md5Checksum").
			Context(ctx).
			Do()
	})
	if err != nil {
		return "", fmt.Errorf("drive: updating %s: %w", fileID, err)
	}
	return updated.Md5Checksum, nil
}

// Download opens the content of fileID for reading. The caller must close
// the returned reader.
func (c *Client) Download(ctx context.Context, fileID string) (io.ReadCloser, error) {
	resp, err := withRetry(ctx, func() (*http.Response, error) {
		return c.svc.Files.Get(fileID).Context(ctx).Download()
	})
	if err != nil {
		return nil, fmt.Errorf("drive: downloading %s: %w", fileID, err)
	}
	return resp.Body, nil
}

// Rename renames and/or reparents fileID -- a cheap metadata-only operation
// used for vault renames/moves so content isn't re-uploaded. oldParentID
// may be empty if the file isn't moving directories.
func (c *Client) Rename(ctx context.Context, fileID, newName, newParentID, oldParentID string) error {
	call := c.svc.Files.Update(fileID, &drivev3.File{Name: newName})
	if newParentID != "" {
		call = call.AddParents(newParentID)
	}
	if oldParentID != "" {
		call = call.RemoveParents(oldParentID)
	}
	_, err := withRetry(ctx, func() (*drivev3.File, error) {
		return call.Fields("id").Context(ctx).Do()
	})
	if err != nil {
		return fmt.Errorf("drive: renaming %s: %w", fileID, err)
	}
	return nil
}

// Trash moves fileID to Drive's trash (not a permanent delete).
func (c *Client) Trash(ctx context.Context, fileID string) error {
	_, err := withRetry(ctx, func() (*drivev3.File, error) {
		return c.svc.Files.Update(fileID, &drivev3.File{Trashed: true}).Context(ctx).Do()
	})
	if err != nil {
		return fmt.Errorf("drive: trashing %s: %w", fileID, err)
	}
	return nil
}

// GetMetadata fetches current metadata for fileID.
func (c *Client) GetMetadata(ctx context.Context, fileID string) (*drivev3.File, error) {
	f, err := withRetry(ctx, func() (*drivev3.File, error) {
		return c.svc.Files.Get(fileID).
			Fields("id, name, md5Checksum, modifiedTime, trashed, parents").
			Context(ctx).
			Do()
	})
	if err != nil {
		return nil, fmt.Errorf("drive: getting metadata for %s: %w", fileID, err)
	}
	return f, nil
}

// ListChildren lists all non-trashed files directly under parentID (paging
// through results as needed), for full vault reconciliation.
func (c *Client) ListChildren(ctx context.Context, parentID string) ([]*drivev3.File, error) {
	var all []*drivev3.File
	pageToken := ""
	query := fmt.Sprintf("'%s' in parents and trashed = false", parentID)

	for {
		token := pageToken
		res, err := withRetry(ctx, func() (*drivev3.FileList, error) {
			call := c.svc.Files.List().
				Q(query).
				Fields("nextPageToken, files(id, name, mimeType, md5Checksum, modifiedTime, parents)").
				PageSize(1000).
				Context(ctx)
			if token != "" {
				call = call.PageToken(token)
			}
			return call.Do()
		})
		if err != nil {
			return nil, fmt.Errorf("drive: listing children of %s: %w", parentID, err)
		}
		all = append(all, res.Files...)
		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}
	return all, nil
}
