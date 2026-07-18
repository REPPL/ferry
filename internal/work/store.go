package work

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The cargo store is a plain directory of cargo bundles on shared or portable
// media — never the ferry config repo, never hosted by default. Layout, per
// project (keyed by the abcd-compatible root SHA):
//
//	<store>/<root-sha>/<seq>-<bundle-hash>.ferrywork   cargo bundles
//	<store>/<root-sha>/claim.<account>@<host>.json     per-account claims
//
// No file is ever written by two accounts (each claim file is written only by
// its owner; bundles are created O_CREAT|O_EXCL), which sidesteps both the
// /Users/Shared POSIX-permission trap and claim-write races, and works
// unchanged on permissionless exFAT.

// StoreInWorktreeError reports a store location inside a git worktree — which
// mechanically catches "inside the ferry config repo".
type StoreInWorktreeError struct{ Dir string }

func (e *StoreInWorktreeError) Error() string {
	return "work: cargo store " + e.Dir + " resolves inside a git worktree — the store must be a plain directory outside any repository"
}

// StoreSyncRootError reports a store under a known cloud-sync root. Cargo
// holds personal working context; syncing it to a hosted service must be an
// explicit, loud choice.
type StoreSyncRootError struct {
	Dir     string
	SyncDir string
}

func (e *StoreSyncRootError) Error() string {
	return "work: cargo store " + e.Dir + " is under the cloud-synced directory " + e.SyncDir + " — nothing in ferry uploads the store, but this location would; pass the explicit override to accept that"
}

// syncRootSegments are path segments that mark a cloud-synced tree: iCloud
// ("Mobile Documents"), macOS provider mounts ("CloudStorage"), and the
// classic vendor directories.
var syncRootSegments = []string{
	"mobile documents",
	"cloudstorage",
	"dropbox",
	"google drive",
	"onedrive",
}

// Store is an opened cargo store.
type Store struct {
	root string
}

// OpenStore validates and opens the cargo store at root. The location guards
// are mechanical, not just documented: a store inside any git worktree (or
// git dir) is refused outright; a store under a known sync root is refused
// unless allowSyncRoot. The top-level store directory is created once by the
// user (documented setup); a missing root is a clear error, not a silent
// mkdir — on removable media it usually means the volume is not mounted.
func OpenStore(root string, allowSyncRoot bool) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	} else if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("work: cargo store %s does not exist (is the volume mounted?) — create it once, then retry", abs)
	} else {
		return nil, err
	}

	if inside, err := dirInsideGit(abs); err != nil {
		return nil, err
	} else if inside {
		return nil, &StoreInWorktreeError{Dir: abs}
	}

	if syncDir := syncRootAncestor(abs); syncDir != "" && !allowSyncRoot {
		return nil, &StoreSyncRootError{Dir: abs, SyncDir: syncDir}
	}
	return &Store{root: abs}, nil
}

// Root returns the store's resolved root directory.
func (st *Store) Root() string { return st.root }

// ProjectDir returns the per-project directory for a store key.
func (st *Store) ProjectDir(key string) string { return filepath.Join(st.root, key) }

// dirInsideGit reports whether dir resolves inside a git worktree or git dir,
// probed with the isolated git environment. Git being absent skips the check
// (the work verbs cannot run without git anyway — identity needs it first).
func dirInsideGit(dir string) (bool, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return false, nil
	}
	out, err := gitOutput(dir, "rev-parse", "--is-inside-work-tree", "--is-inside-git-dir")
	if err != nil {
		// "not a git repository" is the healthy answer for a store location.
		return false, nil
	}
	return strings.Contains(out, "true"), nil
}

// syncRootAncestor returns the shallowest ancestor of dir whose basename is a
// known sync-root segment, or "" when there is none.
func syncRootAncestor(dir string) string {
	segs := strings.Split(dir, string(filepath.Separator))
	prefix := ""
	for _, seg := range segs {
		if seg == "" {
			prefix = string(filepath.Separator)
			continue
		}
		prefix = filepath.Join(prefix, seg)
		lower := strings.ToLower(seg)
		for _, mark := range syncRootSegments {
			if lower == mark {
				return prefix
			}
		}
	}
	return ""
}

// BundleRef names one cargo bundle in the store.
type BundleRef struct {
	Seq    uint64
	SHA256 string
	Path   string
}

// bundleName matches "<seq>-<bundle-hash>.ferrywork". Seq padding is
// display-sugar; the parse accepts any width.
var bundleName = regexp.MustCompile(`^([0-9]+)-([0-9a-f]{64})\.ferrywork$`)

// Bundles lists the project's cargo bundles in ascending sequence order. A
// missing project directory reads as "never packed". Equal-seq entries (two
// accounts packed without an intervening receive) are BOTH returned — the
// caller surfaces the fork; ordering between them is by name, deterministic.
// Non-bundle files (claims, junk) are ignored.
func (st *Store) Bundles(key string) ([]BundleRef, error) {
	if !rootSHA.MatchString(key) {
		return nil, fmt.Errorf("work: store key %q is not a full commit SHA", key)
	}
	entries, err := os.ReadDir(st.ProjectDir(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []BundleRef
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := bundleName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		seq, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		refs = append(refs, BundleRef{
			Seq:    seq,
			SHA256: m[2],
			Path:   filepath.Join(st.ProjectDir(key), e.Name()),
		})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Seq != refs[j].Seq {
			return refs[i].Seq < refs[j].Seq
		}
		return refs[i].Path < refs[j].Path
	})
	return refs, nil
}

// WriteBundle stores the project's next cargo bundle. The sequence is
// allocated by creating the file O_CREAT|O_EXCL and retrying at seq+1 on
// collision — safe on APFS, SMB, and exFAT. Because the bundle's manifest
// records its own sequence number, the caller passes a build callback: it is
// invoked with each candidate sequence and returns the exact bytes to store,
// so the recorded seq always matches the allocated one. The write is fsynced
// before the ref is returned: a handover the packer believes in must be
// durable on the shared medium.
func (st *Store) WriteBundle(key string, build func(seq uint64) ([]byte, error)) (BundleRef, error) {
	if !rootSHA.MatchString(key) {
		return BundleRef{}, fmt.Errorf("work: store key %q is not a full commit SHA", key)
	}
	if err := st.ensureProjectDir(key); err != nil {
		return BundleRef{}, err
	}
	existing, err := st.Bundles(key)
	if err != nil {
		return BundleRef{}, err
	}
	var next uint64 = 1
	if n := len(existing); n > 0 {
		next = existing[n-1].Seq + 1
	}
	const maxAttempts = 10000
	for attempt := 0; attempt < maxAttempts; attempt++ {
		data, err := build(next)
		if err != nil {
			return BundleRef{}, err
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		name := fmt.Sprintf("%06d-%s.ferrywork", next, hash)
		path := filepath.Join(st.ProjectDir(key), name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			next++
			continue
		}
		if err != nil {
			return BundleRef{}, err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			os.Remove(path)
			return BundleRef{}, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			os.Remove(path)
			return BundleRef{}, err
		}
		if err := f.Close(); err != nil {
			os.Remove(path)
			return BundleRef{}, err
		}
		return BundleRef{Seq: next, SHA256: hash, Path: path}, nil
	}
	return BundleRef{}, fmt.Errorf("work: could not allocate a bundle sequence for %s after %d attempts", key, maxAttempts)
}

// ensureProjectDir creates the per-project store directory, group/world-
// writable: two accounts of one human share it, and on /Users/Shared a
// default-umask directory created by Alice would be unwritable by Bob.
func (st *Store) ensureProjectDir(key string) error {
	dir := st.ProjectDir(key)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	// MkdirAll's mode is filtered by the umask; make the grant explicit.
	// Ignored (no-op) on permissionless filesystems like exFAT.
	if err := os.Chmod(dir, 0o777); err != nil {
		return err
	}
	return nil
}
