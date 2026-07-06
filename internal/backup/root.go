package backup

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// leafRoot opens the PARENT directory of absPath as an *os.Root and returns it
// together with the leaf BASENAME to operate on THROUGH it. Every filesystem
// mutation performed via the returned root+base is confined to the REAL parent
// directory: os.Root refuses to traverse a final-component symlink that escapes
// the root, so a same-user process that swaps the leaf to a symlink pointing into
// ~/.ssh or outside $HOME — AFTER guardResolvedContainment validated the parent
// chain but BEFORE the mutation lands — cannot redirect a follow-open write out
// of the parent. This is the leaf-level second defence layered on top of the
// parent-chain guard, which the caller still runs first.
//
// os.OpenRoot works on ANY directory, including a non-$HOME test state root, so
// this adds no home-only restriction beyond guardResolvedContainment's own; the
// parent must merely already exist (the write callers MkdirAll it first, and a
// restore targets a baseline path whose parent exists).
func leafRoot(absPath string) (*os.Root, string, error) {
	root, err := os.OpenRoot(filepath.Dir(absPath))
	if err != nil {
		return nil, "", err
	}
	return root, filepath.Base(absPath), nil
}

// createTempInRoot creates a uniquely named temp file directly under root's
// directory, opened THROUGH the root so the create is confined to it. It mirrors
// os.CreateTemp — random suffix, O_EXCL, retry on collision — but every open goes
// through the root, so a leaf symlink swapped into the directory cannot redirect
// the create. The returned name is the leaf basename, for use with root.Rename.
func createTempInRoot(root *os.Root) (*os.File, string, error) {
	for i := 0; i < 10000; i++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", err
		}
		name := ".ferry-tmp-" + hex.EncodeToString(b[:])
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return nil, "", err
		}
		return f, name, nil
	}
	return nil, "", errors.New("backup: exhausted attempts creating a confined temp file")
}

// removeLeafConfined removes absPath's leaf THROUGH an os.Root opened on its
// parent, so a swapped leaf symlink cannot redirect the unlink (a symlink is
// removed as a symlink, never followed). A missing parent directory means the
// leaf cannot exist, so it is a no-op — matching os.RemoveAll, which treats a
// non-existent path as success.
func removeLeafConfined(absPath string) error {
	root, base, err := leafRoot(absPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	return root.RemoveAll(base)
}
