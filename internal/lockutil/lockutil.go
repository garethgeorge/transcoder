package lockutil

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	"github.com/gofrs/flock"
)

type namedLockSetEntry struct {
	Name string `json:"name"`
	PID  int    `json:"pid"`
}

var ErrLockAlreadyHeld = errors.New("lock already held")

type NamedLockSet struct {
	mu   sync.Mutex
	File string
}

// openLockedFile opens the lock file and returns the file handle and filesystem lock
func (nls *NamedLockSet) openLockedFile() (*os.File, *flock.Flock, error) {
	lock := flock.New(nls.File + ".lock")
	if err := lock.Lock(); err != nil {
		return nil, nil, err
	}

	f, err := os.OpenFile(nls.File, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		lock.Unlock()
		return nil, nil, err
	}

	return f, lock, nil
}

// readLockEntries reads all lock entries from the file
func readLockEntries(f *os.File) ([]namedLockSetEntry, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	var locks []namedLockSetEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry namedLockSetEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		locks = append(locks, entry)
	}
	return locks, scanner.Err()
}

// writeLockEntries writes all lock entries to the file
func writeLockEntries(f *os.File, locks []namedLockSetEntry) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	for _, entry := range locks {
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}
	return nil
}

func (nls *NamedLockSet) TryAcquire(name string) error {
	nls.mu.Lock()
	defer nls.mu.Unlock()

	f, lock, err := nls.openLockedFile()
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		lock.Unlock()
	}()

	locks, err := readLockEntries(f)
	if err != nil {
		return err
	}

	// Check if lock is already held
	var keepLocks []namedLockSetEntry
	for _, entry := range locks {
		if !checkPIDRunning(entry.PID) {
			continue
		}
		if entry.Name == name {
			return fmt.Errorf("%w: by PID %d", ErrLockAlreadyHeld, entry.PID)
		}
		keepLocks = append(keepLocks, entry)
	}

	// Add new lock entry
	keepLocks = append(keepLocks, namedLockSetEntry{Name: name, PID: os.Getpid()})
	return writeLockEntries(f, keepLocks)
}

func (nls *NamedLockSet) Release(name string) error {
	nls.mu.Lock()
	defer nls.mu.Unlock()

	f, lock, err := nls.openLockedFile()
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		lock.Unlock()
	}()

	locks, err := readLockEntries(f)
	if err != nil {
		return err
	}

	// Remove matching entries
	newLocks := make([]namedLockSetEntry, 0, len(locks))
	for _, entry := range locks {
		if entry.Name != name || entry.PID != os.Getpid() {
			newLocks = append(newLocks, entry)
		}
	}

	return writeLockEntries(f, newLocks)
}

func checkPIDRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}
