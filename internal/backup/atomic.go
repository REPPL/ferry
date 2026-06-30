package backup

import (
	"os"
	"path/filepath"
)

// atomicWrite writes data to path via a temp file in the SAME directory then an
// atomic rename. The same-dir temp guarantees the rename stays on one
// filesystem (rename across filesystems is not atomic and may even fail). A
// crash before the rename leaves the original file untouched; after it, the new
// content is fully present. The temp file is fsync'd before rename so the bytes
// are durable, and the temp is cleaned up on any error.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ferry-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the successful rename.
	defer func() { _ = os.Remove(tmpName) }()

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
	// Set the final mode explicitly (CreateTemp makes 0600, but we may need a
	// stricter-preserved or caller-specified mode) and chmod isn't subject to
	// umask the way create is.
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
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
