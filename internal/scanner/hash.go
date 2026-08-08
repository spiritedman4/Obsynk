package scanner

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// HashFile computes a streaming sha256 digest of the file at path without
// loading it fully into memory.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
