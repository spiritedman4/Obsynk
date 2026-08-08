package drive

import (
	"context"
	stdpath "path"
	"time"
)

// Entry is one file or folder discovered while walking a Drive folder tree.
type Entry struct {
	RelativePath string
	FileID       string
	IsDir        bool
	MD5          string
	ModTime      time.Time
	ParentID     string
}

// ListVaultTree recursively lists every non-trashed file and folder under
// rootID, returning a flat map keyed by path relative to rootID. Listing is
// sequential (one folder at a time) rather than concurrent: Drive's
// per-user rate limit, not local work, is the bottleneck here, and vaults
// typically have a modest folder count, so the added complexity of a
// worker-pool walk isn't justified yet.
func (c *Client) ListVaultTree(ctx context.Context, rootID string) (map[string]Entry, error) {
	out := make(map[string]Entry)
	if err := c.walkTree(ctx, rootID, "", out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) walkTree(ctx context.Context, folderID, prefix string, out map[string]Entry) error {
	children, err := c.ListChildren(ctx, folderID)
	if err != nil {
		return err
	}

	for _, f := range children {
		rel := f.Name
		if prefix != "" {
			rel = stdpath.Join(prefix, f.Name)
		}

		isDir := f.MimeType == "application/vnd.google-apps.folder"
		modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)

		out[rel] = Entry{
			RelativePath: rel,
			FileID:       f.Id,
			IsDir:        isDir,
			MD5:          f.Md5Checksum,
			ModTime:      modTime,
			ParentID:     folderID,
		}

		if isDir {
			if err := c.walkTree(ctx, f.Id, rel, out); err != nil {
				return err
			}
		}
	}
	return nil
}
