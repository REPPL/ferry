package backup

import (
	"os"
	"path/filepath"
)

// AtomicWrite writes data to path via a temp file in the SAME directory then an
// atomic rename. The same-dir temp guarantees the rename stays on one
// filesystem (rename across filesystems is not atomic and may even fail). A
// crash before the rename leaves the original file untouched; after it, the new
// content is fully present. The temp file is fsync'd before rename so the bytes
// are durable, and the temp is cleaned up on any error. It is the canonical
// crash-safe single-file write for the whole codebase; callers outside this
// package (cmd's deps-record and capture writers) use it too rather than
// reinventing temp+rename.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Open the parent directory as an os.Root and route the temp create, chmod,
	// and rename THROUGH it, operating on the leaf BASENAME only. os.Root refuses
	// to traverse a leaf symlink that escapes the parent, closing the leaf-swap
	// TOCTOU that the parent-chain guard (guardResolvedContainment, run earlier by
	// the transactional caller) does not cover: a same-user process that swaps the
	// leaf to a symlink into ~/.ssh or outside $HOME cannot redirect the write. The
	// temp file lives in the SAME directory so the rename stays on one filesystem
	// (a cross-filesystem rename is not atomic and may fail).
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()

	tmp, tmpName, err := createTempInRoot(root)
	if err != nil {
		return err
	}
	// Best-effort cleanup if we bail before the successful rename.
	defer func() { _ = root.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Set the final mode explicitly (the temp is created 0600, but we may need a
	// stricter-preserved or caller-specified mode) and chmod isn't subject to
	// umask the way create is.
	if err := root.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := root.Rename(tmpName, base); err != nil {
		return err
	}
	// fsync the parent directory so the renamed dir entry is durable across a
	// crash (the rename itself is atomic, but the directory metadata is not yet
	// guaranteed on disk until the dir is synced).
	return syncDir(dir)
}

// syncDir opens dir and fsyncs it so directory-entry changes (e.g. a rename
// into it) are durable on crash. A failure to sync is surfaced; a failure to
// open (unsupported on some platforms) is too, by the caller's choice.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}
