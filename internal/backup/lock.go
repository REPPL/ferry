package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// syncFile fsyncs the lockfile after its metadata is written. It is a package
// var (not a direct f.Sync() call) only so a test can inject a Sync failure to
// exercise the post-creation cleanup path; production always uses (*os.File).Sync.
var syncFile = (*os.File).Sync

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
		lock, err := e.publishLock()
		if err == nil {
			return lock, nil
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

// publishLock atomically creates the lockfile with COMPLETE contents, or returns
// fs.ErrExist if a lock is already present. It writes the PID/timestamp payload
// to a same-directory temp file, fsyncs it, then os.Link()s it into place: link
// fails with EEXIST when the destination exists, so the lock only ever becomes
// visible AFTER its contents are fully written and durable.
//
// This closes the create-then-write race the plain O_CREATE|O_EXCL path had: a
// concurrent acquirer that failed the create used to be able to read the
// half-written (empty) lockfile of a LIVE owner, conclude it was corrupt/stale,
// and reclaim it — letting two applies run at once. With atomic publish there is
// no window in which a live owner's lock is observable but not yet readable, so
// readLockInfo only ever sees a complete payload (or a genuinely corrupt one from
// out-of-band tampering / a crash, which reclaim still handles).
func (e *Engine) publishLock() (*Lock, error) {
	info := lockInfo{PID: os.Getpid(), AcquiredAt: time.Now().UTC()}
	data, _ := json.MarshalIndent(info, "", "  ")

	tmp, err := os.CreateTemp(filepath.Dir(e.lockPath), "lock-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return nil, werr
	}
	if serr := syncFile(tmp); serr != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return nil, serr
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpName)
		return nil, cerr
	}
	// Atomic publish. EEXIST (fs.ErrExist) is the "lock already held" signal the
	// caller decides held-vs-stale on; the temp is always cleaned up.
	if lerr := os.Link(tmpName, e.lockPath); lerr != nil {
		_ = os.Remove(tmpName)
		return nil, lerr
	}
	_ = os.Remove(tmpName)
	return &Lock{path: e.lockPath, pid: info.PID}, nil
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
