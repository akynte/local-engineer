package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyTree copies a fixture into a scratch directory.
//
// Symlinks are deliberately not followed: a fixture is data, and a link
// pointing outside it would let a task reach the machine running the
// evaluation.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o750)
		case !d.Type().IsRegular():
			// Skip symlinks, sockets and devices rather than reproducing them.
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p) //nolint:gosec // a path walked inside the fixture
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		return os.WriteFile(target, body, info.Mode().Perm()) //nolint:gosec // a scratch evaluation copy
	})
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // a path walked inside the scratch copy
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
