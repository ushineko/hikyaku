/*
Package atomicfile replaces a file whole: a temporary file in the same
directory, renamed over the old one, so a crash leaves the old file or the
new one and never half of either.
*/
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with data. The file is created 0600 (os.CreateTemp's
// mode), which is what everything hikyaku writes should be.
func Write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
