package backup

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

// PathKind classifies the prior state of a managed path.
type PathKind string

const (
	// KindAbsent means the path did not exist pre-ferry. Restoring it removes
	// whatever ferry later created there.
	KindAbsent PathKind = "absent"
	// KindFile means the path was a regular file; Content + Mode are saved.
	KindFile PathKind = "file"
	// KindSymlink means the path was a symlink; Target is saved.
	KindSymlink PathKind = "symlink"
)

// PathState is the captured prior state of a single path. It is the unit stored
// in the baseline (immutable) and journal (per-run), and re-snapshotted before
// a restore. Content for files is stored as a side payload (see blobName); the
// metadata here is what serialises into the manifest JSON.
type PathState struct {
	Path    string      `json:"path"`               // absolute path captured
	Kind    PathKind    `json:"kind"`               // absent | file | symlink
	Mode    os.FileMode `json:"mode,omitempty"`     // file mode (KindFile only)
	Target  string      `json:"target,omitempty"`   // link target (KindSymlink only)
	HasBlob bool        `json:"has_blob,omitempty"` // a content payload exists in the store
	// ModTime is the original modification time, captured once with the baseline
	// and immutable like the rest of the state. Restore stamps it back via
	// os.Chtimes so a reverted file is mtime-identical to its pre-ferry original
	// (the "leaves no trace" promise), not freshly timestamped. Zero/irrelevant
	// for KindAbsent. It is metadata only — content-addressed blobs are keyed by
	// bytes alone, so two files with identical content but different mtimes share
	// one blob yet keep distinct ModTimes here.
	ModTime time.Time `json:"mod_time,omitempty"`
	// Blob, when set, is the sha256 (hex) of the content payload. The immutable
	// baseline store is CONTENT-ADDRESSED: the blob lives at blobs/<Blob>, so two
	// racers capturing the same original bytes write byte-identical content under
	// the same name (a harmless idempotent write), and a loser can never corrupt
	// the winner's blob. Per-run journal/snapshot blobs are keyed by path within
	// their own run directory and leave this empty.
	Blob string `json:"blob,omitempty"`
}

// captureState reads the current on-disk state of path WITHOUT following a
// final symlink, so a symlink is recorded as a symlink (not its target). The
// returned content is the file bytes for a regular file (nil otherwise).
func captureState(path string) (PathState, []byte, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return PathState{Path: path, Kind: KindAbsent}, nil, nil
	}
	if err != nil {
		return PathState{}, nil, err
	}

	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return PathState{}, nil, err
		}
		// fi is from Lstat, so ModTime() here is the LINK's own mtime (not its
		// target's). Recorded for completeness; restore preserves it best-effort
		// (see restoreState's symlink note for the portability limit).
		return PathState{Path: path, Kind: KindSymlink, Target: target, ModTime: fi.ModTime()}, nil, nil
	case fi.Mode().IsRegular():
		content, err := os.ReadFile(path)
		if err != nil {
			return PathState{}, nil, err
		}
		return PathState{
			Path:    path,
			Kind:    KindFile,
			Mode:    fi.Mode().Perm(),
			HasBlob: true,
			ModTime: fi.ModTime(),
		}, content, nil
	default:
		// Directories, devices, sockets etc. are out of scope for the file
		// engine — ferry only manages regular files and symlinks. Surface it
		// rather than silently mishandling.
		return PathState{}, nil, &UnsupportedKindError{Path: path, Mode: fi.Mode()}
	}
}

// UnsupportedKindError is returned when a managed path is neither a regular
// file, a symlink, nor absent (e.g. a directory or device node).
type UnsupportedKindError struct {
	Path string
	Mode os.FileMode
}

func (e *UnsupportedKindError) Error() string {
	return "backup: unsupported path kind for " + e.Path + " (" + e.Mode.String() + ")"
}

// restoreState writes path back to the recorded prior state. blob is the saved
// file content (required when state.HasBlob). It removes whatever is currently
// at path first, so a file->symlink or symlink->file transition restores
// cleanly, and an absent prior state leaves nothing behind.
func restoreState(state PathState, blob []byte) error {
	// Re-validate the RESOLVED parent chain BEFORE mutating: a parent swapped to
	// a symlink since the baseline was captured (e.g. between apply and restore)
	// would otherwise redirect the RemoveAll+write below outside $HOME or into
	// ~/.ssh. Fails closed on refusal; a not-under-$HOME path is a no-op.
	if err := guardResolvedContainment(state.Path); err != nil {
		return err
	}
	// Remove the current occupant (file or symlink). RemoveAll on a single
	// non-dir path behaves like Remove; ENOENT is fine.
	if err := os.RemoveAll(state.Path); err != nil {
		return err
	}

	switch state.Kind {
	case KindAbsent:
		return nil // pre-ferry nothing -> leave nothing.
	case KindSymlink:
		if err := os.Symlink(state.Target, state.Path); err != nil {
			return err
		}
		// Stamp the LINK's OWN mtime back so a restored symlink is mtime-identical
		// to its pre-ferry original (the "leaves no trace" promise), not freshly
		// timestamped. os.Chtimes FOLLOWS the link and would stamp the target, so we
		// use a NOFOLLOW primitive (lutimes / utimensat+AT_SYMLINK_NOFOLLOW via
		// golang.org/x/sys/unix Lutimes) that exists on both supported platforms
		// (darwin + linux). A zero ModTime (e.g. a legacy baseline recorded before
		// this field existed) leaves the just-created link mtime.
		if !state.ModTime.IsZero() {
			return lchtimes(state.Path, state.ModTime)
		}
		return nil
	case KindFile:
		// Write with the preserved (possibly stricter than 0600) original mode.
		// The live home file's mode is the user's, not the secret-store default.
		if err := os.WriteFile(state.Path, blob, state.Mode.Perm()); err != nil {
			return err
		}
		// WriteFile is subject to umask; force the exact recorded mode.
		if err := os.Chmod(state.Path, state.Mode.Perm()); err != nil {
			return err
		}
		// Stamp the original modification time back so the restored file is
		// mtime-identical to its pre-ferry original, not freshly timestamped.
		// atime is set to the same recorded instant; only mtime is observable in
		// the "leaves no trace" promise. A zero ModTime (e.g. a legacy baseline
		// recorded before this field existed) leaves the just-written mtime.
		if !state.ModTime.IsZero() {
			return os.Chtimes(state.Path, state.ModTime, state.ModTime)
		}
		return nil
	default:
		return &UnsupportedKindError{Path: state.Path, Mode: state.Mode}
	}
}
