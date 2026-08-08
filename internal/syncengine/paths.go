package syncengine

import stdpath "path"

// parentOf returns the parent directory of a forward-slash relative path,
// "" for a top-level path (meaning the vault/Drive root).
func parentOf(relPath string) string {
	dir := stdpath.Dir(relPath)
	if dir == "." {
		return ""
	}
	return dir
}
