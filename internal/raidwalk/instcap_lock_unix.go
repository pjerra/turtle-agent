//go:build unix

package raidwalk

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// acquireLedgerLock takes an exclusive flock on the lock file, creating it if needed. The lock lives
// on the open descriptor: the kernel drops it when the holder closes the file or dies, and a live
// holder is never taken over. The file itself is never unlinked, so there is no unlink race between
// a releasing holder and a waiter that opened the old inode. Waiters poll with LOCK_NB and give up
// with errLedgerLockBusy after ledgerLockTries.
func acquireLedgerLock(lock string) (*os.File, error) {
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for i := 0; i < ledgerLockTries; i++ {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, err
		}
		time.Sleep(ledgerLockPoll)
	}
	f.Close()
	return nil, errLedgerLockBusy
}

// releaseLedgerLock unlocks and closes the descriptor acquireLedgerLock returned. It only ever
// releases the caller's own lock: the lock is tied to that descriptor.
func releaseLedgerLock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
