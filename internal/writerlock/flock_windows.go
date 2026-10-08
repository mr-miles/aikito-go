//go:build windows

package writerlock

import (
	"os"
	"syscall"
	"unsafe"
)

// Python locks one byte at offset 0 with msvcrt.locking(LK_LOCK). The
// syscall package has no LockFileEx wrapper, so call kernel32 directly
// rather than take an x/sys dependency.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const lockfileExclusiveLock = 0x2

func lockFile(f *os.File) error {
	var ol syscall.Overlapped
	if r, _, err := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&ol))); r == 0 {
		return err
	}
	return nil
}

func unlockFile(f *os.File) error {
	var ol syscall.Overlapped
	if r, _, err := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol))); r == 0 {
		return err
	}
	return nil
}
