//go:build !unix

package raidwalk

import (
	"fmt"
	"os"
)

// acquireLedgerLock fails closed on platforms without flock: the ledger is never written unlocked,
// and no lock file is left behind to go stale.
func acquireLedgerLock(lock string) (*os.File, error) {
	return nil, fmt.Errorf("instance-cap ledger lock %s needs flock, which this platform does not provide", lock)
}

func releaseLedgerLock(f *os.File) {}
