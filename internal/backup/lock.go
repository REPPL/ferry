package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"
)

// Lockfile model.
//
// Apply mutates the machine and must be serialised: one apply at a time. The
// lock lives at <state>/lock (paths.LockPath) — OUTSIDE the repo, so moving or
// re-cloning the repo never affects locking. --dry-run / diff never take it
// because they never write.
//
// Acquisition is via O_CREATE|O_EXCL: the file's existence IS the lock, and
// O_EXCL makes creation atomic, so two processes cannot both create it. The
// lock records the owner PID and start time.
//
// Stale-lock handling: a process can die (crash, kill -9) without releasing the
// lock, leaving the file behind forever. So on a failed acquire we read the
// owner PID and ask the OS whether that process is still alive (signal 0). If
// the owner is gone, the lock is stale and we reclaim it. We only reclaim a lock
// whose owner is provably dead — a live owner's lock is always respected.

// ErrLockHeld is returned by Lock when another LIVE process holds the apply
// lock. The held lock's metadata is attached for reporting.
type ErrLockHeld struct {
	OwnerPID   int
	AcquiredAt time.Time
}

func (e *ErrLockHeld) Error() string {
	return fmt.Sprintf("backup: apply lock held by pid %d since %s", e.OwnerPID, e.AcquiredAt.Format(time.RFC3339))
}

// lockInfo is the JSON payload written into the lockfile.
type lockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// Lock is a held apply lock. Release with Unlock.
type Lock struct {
	path string
	pid  int
}

// Lock acquires the exclusive apply lock, reclaiming it first if the recorded
// owner is dead (stale). It returns *ErrLockHeld if a live process holds it.
func (e *Engine) Lock() (*Lock, error) {
	return e.lockWithAlive(processAlive)
}

// lockWithAlive is the testable core: aliveFn reports whether a pid is live.
//
// The reclaim loop is intentionally bounded to two attempts. Under a pathological
// storm — a corrupt/dead-owner lock that we reclaim, immediately re-created by a
// racing fresh acquirer between our Remove and our re-create — both attempts can
// lose and we transiently return *ErrLockHeld even though no single owner is
// durably wedged. This is self-correcting: the next apply re-runs this path and
// acquires once the contention clears, so a bounded loop is preferred over an
// unbounded spin that could livelock under sustained contention.
func (e *Engine) lockWithAlive(aliveFn func(int) bool) (*Lock, error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(e.lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
		if err == nil {
			info := lockInfo{PID: os.Getpid(), AcquiredAt: time.Now().UTC()}
			data, _ := json.MarshalIndent(info, "", "  ")
			if _, werr := f.Write(data); werr != nil {
				f.Close()
				_ = os.Remove(e.lockPath)
				return nil, werr
			}
			if serr := f.Sync(); serr != nil {
				f.Close()
				return nil, serr
			}
			f.Close()
			return &Lock{path: e.lockPath, pid: info.PID}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}

		// Lock exists: decide held-vs-stale.
		info, rerr := readLockInfo(e.lockPath)
		if rerr != nil {
			// Unreadable/corrupt lock with no recoverable owner: treat as stale
			// and reclaim, otherwise apply is wedged forever.
			if rmErr := os.Remove(e.lockPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				return nil, rmErr
			}
			continue
		}
		if info.PID != os.Getpid() && aliveFn(info.PID) {
			return nil, &ErrLockHeld{OwnerPID: info.PID, AcquiredAt: info.AcquiredAt}
		}
		// Owner is this process (re-acquire after a crash) or provably dead:
		// reclaim. Remove and loop to re-create atomically.
		if rmErr := os.Remove(e.lockPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return nil, rmErr
		}
	}
	// Both attempts lost a race to another fresh acquirer.
	info, _ := readLockInfo(e.lockPath)
	return nil, &ErrLockHeld{OwnerPID: info.PID, AcquiredAt: info.AcquiredAt}
}

// Unlock releases the lock. Releasing a lock not owned by this process is a
// no-op guarded by the recorded PID, so a reclaimed-then-rewritten lock is not
// removed by the dead owner's stale handle.
func (l *Lock) Unlock() error {
	if l == nil {
		return nil
	}
	info, err := readLockInfo(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && info.PID != l.pid {
		// Someone else reclaimed and now owns it; don't remove theirs.
		return nil
	}
	if rmErr := os.Remove(l.path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return rmErr
	}
	return nil
}

func readLockInfo(path string) (lockInfo, error) {
	var info lockInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	return info, nil
}

// processAlive reports whether a process with the given PID currently exists.
// On Unix, signal 0 performs error-checking without delivering a signal: nil or
// EPERM means the process exists (EPERM = exists but not ours to signal); ESRCH
// means no such process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid) // always succeeds on Unix
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
