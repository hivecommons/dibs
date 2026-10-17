// Package fsutil provides shared filesystem persistence helpers.
package fsutil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteJSON writes v as indented JSON via a temporary file in path's
// directory and a rename, so readers never see a partially written file.
// The directory must already exist. The resulting file has mode 0600.
// This does not fsync the file or directory and does not guarantee durability
// across a power failure.
func AtomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling: %w", err)
	}
	return AtomicWriteFile(path, data)
}

// AtomicWriteFile writes data to path via a temporary file and a rename.
// The resulting file has mode 0600.
func AtomicWriteFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}
