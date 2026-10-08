package mcp

import (
	"os"
	"path/filepath"
)

// atomicWriteFile mirrors executor.py's _atomic_write: write to a sibling
// temp file in the same directory, then os.Rename (POSIX-atomic within one
// filesystem) into place. When securePermissions is true, the file is
// chmod 0600 (credential-bearing content); otherwise it inherits the
// pre-existing target's mode if there was one, else the OS default.
func atomicWriteFile(path, content string, securePermissions bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	var priorMode os.FileMode
	hadPrior := false
	if info, err := os.Stat(path); err == nil {
		priorMode = info.Mode().Perm()
		hadPrior = true
	}
	tmp, err := os.CreateTemp(dir, ".mcp-write-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if securePermissions {
		if err := os.Chmod(tmpPath, 0o600); err != nil {
			// Matches Python: a failed permission-secure attempt is a
			// warning, not an abort — the write still proceeds.
		}
	} else if hadPrior {
		_ = os.Chmod(tmpPath, priorMode)
	}
	return os.Rename(tmpPath, path)
}
