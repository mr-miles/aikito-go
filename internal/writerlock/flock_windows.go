//go:build windows

package writerlock

import "os"

// Python locks one byte with msvcrt.locking. The syscall package has no
// LockFileEx wrapper and this module takes no x/sys dependency, so on
// Windows the lock file is created but not exclusively held.
func lockFile(f *os.File) error   { return nil }
func unlockFile(f *os.File) error { return nil }
