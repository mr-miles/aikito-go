package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// FileSnapshot ports config_runtime.py's FileSnapshot: the state of a
// physical config file when a plan was built, so the executor can refuse
// to write over a file that changed in between.
type FileSnapshot struct {
	Path          string
	Exists        bool
	ContentHash   string // "" when unreadable or a directory (Python's None)
	IsSymlink     bool
	SymlinkTarget string
	IsDir         bool
}

// CaptureFileSnapshot ports capture_file_snapshot.
func CaptureFileSnapshot(path string) FileSnapshot {
	li, err := os.Lstat(path)
	if err != nil {
		return FileSnapshot{Path: path}
	}
	s := FileSnapshot{Path: path, Exists: true, IsSymlink: li.Mode()&os.ModeSymlink != 0}
	if s.IsSymlink {
		s.SymlinkTarget, _ = os.Readlink(path)
	}
	st, statErr := os.Stat(path)
	s.IsDir = statErr == nil && st.IsDir() && !s.IsSymlink
	if statErr == nil && !s.IsDir {
		if data, err := os.ReadFile(path); err == nil {
			sum := sha256.Sum256(data)
			s.ContentHash = hex.EncodeToString(sum[:])
		}
	}
	return s
}

// ValidatePrecondition ports FileSnapshot.validate_precondition, with its
// messages.
func (s FileSnapshot) ValidatePrecondition(target string) (bool, string) {
	li, err := os.Lstat(target)
	if err != nil {
		if s.Exists {
			return false, fmt.Sprintf("File '%s' existed at plan time but is now missing", target)
		}
		return true, ""
	}
	if !s.Exists {
		return false, fmt.Sprintf("File '%s' was missing at plan time but now exists", target)
	}
	isLink := li.Mode()&os.ModeSymlink != 0
	if isLink != s.IsSymlink {
		kind := func(link bool) string {
			if link {
				return "symlink"
			}
			return "regular file/entry"
		}
		return false, fmt.Sprintf("File '%s' type changed: expected %s, found %s", target, kind(s.IsSymlink), kind(isLink))
	}
	if isLink {
		dest, err := os.Readlink(target)
		if err != nil {
			return false, fmt.Sprintf("Cannot read symlink '%s': %v", target, err)
		}
		if dest != s.SymlinkTarget {
			return false, fmt.Sprintf("Symlink '%s' target changed from '%s' to '%s'", target, s.SymlinkTarget, dest)
		}
	}
	st, statErr := os.Stat(target)
	if (statErr == nil && st.IsDir()) != s.IsDir {
		return false, fmt.Sprintf("File '%s' entry type changed from plan snapshot", target)
	}
	if s.ContentHash != "" {
		data, err := os.ReadFile(target)
		if err != nil {
			return false, fmt.Sprintf("Cannot read file '%s' for precondition validation: %v", target, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != s.ContentHash {
			return false, fmt.Sprintf("File '%s' content has been modified externally since plan generation", target)
		}
	}
	return true, ""
}
