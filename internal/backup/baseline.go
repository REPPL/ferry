package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/REPPL/ferry/internal/paths"
)

// Baseline entry layout under baselineDir:
//
//	<key>.json        metadata (PathState), keyed by sha256 of the managed path
//	blobs/<sha256>    original file content, CONTENT-ADDRESSED by its own bytes
//
// The baseline is the IMMUTABLE per-machine record of true pre-ferry state. The
// first time ferry touches a path it is written ONCE; every later touch is a
// no-op. Overwriting it would destroy the only record of the original state and
// is therefore impossible through this API.
//
// Race-free commit protocol (correct even WITHOUT the apply lock):
//   - The blob is content-addressed: its filename IS the sha256 of its bytes, so
//     two racers capturing the SAME original bytes write byte-identical content to
//     the SAME name — an idempotent write that no loser can corrupt.
//   - The metadata <key>.json is the single commit point, published exactly once
//     via os.Link of a fully-written temp file. The winner is whoever's Link
//     succeeds; EEXIST means another writer already committed → the loser discards
//     its staged temp and treats the existing baseline as authoritative.

func (e *Engine) baselineMetaPath(absPath string) string {
	return filepath.Join(e.baselineDir, keyFor(absPath)+".json")
}

// baselineBlobsDir holds content-addressed baseline blobs.
func (e *Engine) baselineBlobsDir() string {
	return filepath.Join(e.baselineDir, "blobs")
}

// baselineBlobPathFor returns the content-addressed path for a blob whose bytes
// hash to the given hex sha256.
func (e *Engine) baselineBlobPathFor(hashHex string) string {
	return filepath.Join(e.baselineBlobsDir(), hashHex)
}

// hashContent returns the hex sha256 of content, used to content-address blobs.
func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// validKind reports whether k is one of the three recognised baseline kinds. A
// metadata entry whose Kind is unset or unknown is NOT a complete baseline.
func validKind(k PathKind) bool {
	switch k {
	case KindAbsent, KindFile, KindSymlink:
		return true
	default:
		return false
	}
}

// HasBaseline reports whether a COMPLETE, valid immutable baseline exists for
// absPath. A bare existence check is not enough; see Baseline for what "complete"
// means. A partial/corrupt entry reads as absent so the true baseline is still
// captured on the next touch.
func (e *Engine) HasBaseline(absPath string) bool {
	_, ok, err := e.Baseline(absPath)
	return ok && err == nil
}

// HasBaselineReadOnly reports whether a COMPLETE, valid immutable baseline exists
// for absPath under stateRoot, WITHOUT creating the state directory or any of its
// subdirs. It is the write-free counterpart to New()+HasBaseline for read-only
// previews (diff / status / apply --dry-run): New()/NewAt() eagerly mkdir+chmod
// baseline/, blobs/, journal/, snapshots/, which would mutate the filesystem on a
// machine that only has the state ROOT (or nothing). This path stats the baseline
// metadata only, reusing the SAME completeness semantics as HasBaseline (parse,
// non-empty Path, valid Kind, blob present for KindFile) so a partial/corrupt entry
// reads as absent here exactly as it does on the mutating path. Returns false if
// the state dir is absent — a never-applied machine has no baseline.
func HasBaselineReadOnly(stateRoot, absPath string) bool {
	if stateRoot == "" {
		return false
	}
	// Symlink-harden the state ROOT BEFORE any os.ReadFile/os.Stat below, so this
	// read-only probe is structurally safe for EVERY caller (diff/status/apply
	// --dry-run terminal baseline check, restore baseline enumeration): a
	// ~/.local/state/ferry symlinked into ~/.ssh or a system path is refused, never
	// read through. The check is lexical, creates no dirs, never touches ~/.ssh, and
	// is a no-op for a test root not under $HOME (t.TempDir()). A refused root reads
	// as "no baseline" — the safe answer for a poisoned store.
	if err := paths.HardenStoreDir(stateRoot); err != nil {
		return false
	}
	// Construct only the path fields; do NOT call NewAt (which would mkdir/chmod
	// the store layout). Baseline reads e.baselineDir via baselineMetaPath and
	// e.baselineBlobsDir, both pure path joins, so this is fully read-only.
	e := &Engine{baselineDir: filepath.Join(stateRoot, "baseline")}
	_, ok, err := e.Baseline(absPath)
	return ok && err == nil
}

// Baseline returns the recorded pre-ferry PathState for absPath. ok is false if
// no baseline has been written, OR if the on-disk metadata is not a COMPLETE,
// valid record. Complete means: it parses, Path is non-empty, Kind is one of the
// valid kinds, and a KindFile entry references an existing content-addressed
// blob. Anything else (a crash mid-write, a partial `{"path":"/x"}`) reads as
// absent so the real baseline can still be captured next touch. A genuine read
// error (not parse/absent) is surfaced.
func (e *Engine) Baseline(absPath string) (state PathState, ok bool, err error) {
	data, err := os.ReadFile(e.baselineMetaPath(absPath))
	if errors.Is(err, fs.ErrNotExist) {
		return PathState{}, false, nil
	}
	if err != nil {
		return PathState{}, false, err
	}
	return e.parseValidBaseline(data)
}

// parseValidBaseline unmarshals baseline metadata bytes and reports the state
// only if it is a COMPLETE, valid record: it parses, Path is non-empty, Kind is
// one of the valid kinds, and a KindFile entry references an existing
// content-addressed blob. Anything else reads as absent (ok=false, err=nil) so
// junk/partial entries are uniformly ignored by both Baseline and the full
// restore enumeration. A genuine read error is surfaced by the caller.
func (e *Engine) parseValidBaseline(data []byte) (PathState, bool, error) {
	var state PathState
	if err := json.Unmarshal(data, &state); err != nil {
		return PathState{}, false, nil // truncated/partial metadata
	}
	if state.Path == "" || !validKind(state.Kind) {
		return PathState{}, false, nil // empty/zero or unknown-kind: not a baseline
	}
	if state.Kind == KindFile {
		// A file baseline must reference an existing content-addressed blob.
		if !state.HasBlob || state.Blob == "" {
			return PathState{}, false, nil
		}
		if _, statErr := os.Stat(e.baselineBlobPathFor(state.Blob)); statErr != nil {
			return PathState{}, false, nil
		}
	}
	return state, true, nil
}

// ensureBaseline records the current state of absPath as the immutable baseline
// IF AND ONLY IF none exists yet. Captures the live state itself, so the caller
// must call this BEFORE mutating the path. A second call for the same path is a
// deliberate no-op — baseline immutability is enforced by writeBaseline's atomic
// single-commit publish (no check-then-act here, so there is no TOCTOU).
func (e *Engine) ensureBaseline(absPath string) error {
	if e.HasBaseline(absPath) {
		return nil
	}
	state, content, err := captureState(absPath)
	if err != nil {
		return err
	}
	return e.writeBaseline(state, content)
}

// writeBaseline persists a baseline entry exactly once, race-free even without
// the apply lock. There is NO delete-and-retry repair path: both the blob and the
// metadata are published write-once via os.Link, so no code path ever removes a
// published baseline (eliminating any TOCTOU where one writer's Remove could
// clobber another's freshly-committed entry).
//
//   - Blob: content-addressed at blobs/<sha256> and published via os.Link from a
//     fsync'd temp. EEXIST means an identical-bytes blob is already present
//     (content-addressing guarantees the bytes match) — discard the temp, fine.
//     The blob inode AND mode are thus immutable once present.
//   - Metadata: written COMPLETE to a fsync'd temp, then os.Link is the single
//     commit. Link success = winner. EEXIST = the slot is taken: if it holds a
//     VALID baseline, the loser discards its temp and returns (write-once); if it
//     holds a pre-existing INVALID/corrupt entry (only possible from a legacy or
//     externally-corrupted file, never this code), we do NOT race to delete it —
//     we return a clear error so the corrupt slot is surfaced, and HasBaseline /
//     Baseline already read it as absent.
func (e *Engine) writeBaseline(state PathState, content []byte) error {
	if state.HasBlob {
		state.Blob = hashContent(content)
		if err := e.publishBlob(state.Blob, content, state.Mode); err != nil {
			return err
		}
	}

	meta, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// Stage the COMPLETE metadata to a temp file (fsync'd), then publish it with a
	// single atomic os.Link.
	tmpMeta, err := stageStoreBlob(e.baselineDir, meta, filePerm)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpMeta) }()

	metaPath := e.baselineMetaPath(state.Path)
	if err := os.Link(tmpMeta, metaPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Slot taken. A valid baseline wins (write-once). A corrupt slot is
			// surfaced as an error — never racily deleted from the write path.
			if _, ok, _ := e.Baseline(state.Path); ok {
				return nil
			}
			return &CorruptBaselineError{Path: state.Path}
		}
		return err
	}
	return syncDir(e.baselineDir)
}

// publishBlob writes content to its content-addressed home blobs/<hashHex>
// write-once: a fsync'd temp is os.Link'd into place, so the blob's inode and
// mode are immutable once present. EEXIST means the identical bytes are already
// stored (content-addressing guarantees a match) — the staged temp is discarded.
func (e *Engine) publishBlob(hashHex string, content []byte, mode os.FileMode) error {
	blobPath := e.baselineBlobPathFor(hashHex)
	if _, err := os.Stat(blobPath); err == nil {
		return nil // already present with identical (content-addressed) bytes.
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmpBlob, err := stageStoreBlob(e.baselineBlobsDir(), content, mode)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpBlob) }()
	if err := os.Link(tmpBlob, blobPath); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil // a racer placed identical bytes first.
		}
		return err
	}
	return syncDir(e.baselineBlobsDir())
}

// CorruptBaselineError is returned when a baseline must be written but the
// metadata slot is occupied by an unreadable/incomplete entry (a legacy or
// externally-corrupted file). The write path never deletes it (that would race a
// concurrent valid publish); the corrupt entry must be cleared out of band.
type CorruptBaselineError struct{ Path string }

func (e *CorruptBaselineError) Error() string {
	return "backup: corrupt baseline metadata blocks recording the baseline for " + e.Path
}

// loadBlob reads a stored content payload for a PathState that HasBlob.
func loadBlob(blobPath string) ([]byte, error) {
	return os.ReadFile(blobPath)
}

// stageStoreBlob writes a content payload to a fresh temp file in dir at the
// effective (>=0600, stricter preserved) mode, fsyncs it for durability, and
// returns the temp path. The caller renames it into its final immutable home
// (or removes it on a lost race). Keeping the staged blob in the SAME directory
// guarantees the later rename stays on one filesystem and is atomic.
func stageStoreBlob(dir string, content []byte, origMode os.FileMode) (string, error) {
	mode := effectiveMode(origMode)
	tmp, err := os.CreateTemp(dir, ".ferry-blob-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	// Force the mode regardless of umask so the store never ends up looser.
	if err := os.Chmod(name, mode); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// writeStoreBlob writes a content payload into the secret-bearing store at the
// effective (>=0600, stricter preserved) mode.
func writeStoreBlob(blobPath string, content []byte, origMode os.FileMode) error {
	mode := effectiveMode(origMode)
	if err := os.WriteFile(blobPath, content, mode); err != nil {
		return err
	}
	// Force the mode regardless of umask so the store never ends up looser.
	return os.Chmod(blobPath, mode)
}
