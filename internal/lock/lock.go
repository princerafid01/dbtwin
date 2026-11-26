// Package lock makes sure only one dbtwin command that changes things
// (create, delete, reset, refresh) runs at a time.
package lock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// path is the lock file. /run is cleared on every boot, and only root
// can write there, which is fine: dbtwin always runs as root.
const path = "/run/dbtwin.lock"

// ErrBusy means another dbtwin command holds the lock.
// The API checks for it to answer "busy, try again later".
var ErrBusy = errors.New("another dbtwin command (create, delete, reset or refresh) is running. Try again when it finishes")

// Take locks dbtwin, or fails right away if another dbtwin command
// already holds the lock. Call the returned function to unlock.
//
// The lock is a flock on the file, not the file itself: the file may stay
// on disk forever, it is the lock on it that matters. The kernel drops the
// lock whenever the process ends, even after a crash or kill -9, so a dead
// dbtwin can never leave dbtwin locked.
func Take() (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("can't open lock file %s: %v", path, err)
	}

	// LOCK_EX = only one holder at a time
	// LOCK_NB = don't wait: fail right away if someone else holds it
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("can't lock %s: %v", path, err)
	}

	// closing the file also drops the lock
	return func() { f.Close() }, nil
}
