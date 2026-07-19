package work

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/backup"
)

// ReceiveOptions carries the caller's choices into a receive.
type ReceiveOptions struct {
	// Force overrides the guarded-overwrite refusals, the superseded guard,
	// and (on the packer's own account) turns a take-back into a real landing.
	Force bool
	// BundleSHA256 names one bundle explicitly, resolving an equal-seq tie.
	BundleSHA256 string
	// Account is this account's claim identity, user@host.
	Account string
	// Now is an RFC3339 timestamp for display fields.
	Now string
}

// ReceiveResult reports a completed receive (or take-back).
type ReceiveResult struct {
	Ref      BundleRef
	Manifest *Manifest
	// TakeBack is set when the packer reclaimed their own baton: the marker
	// and claim were updated and NOTHING was restored.
	TakeBack bool
	// Written are the absolute paths this receive wrote or removed.
	Written []string
	// Skipped are destinations left untouched (union-merge files already
	// present; guarded files already holding the incoming content).
	Skipped    []string
	SnapshotID string
}

// TieError reports an equal-seq fork at the highest sequence: Alice and Bob
// both packed without an intervening receive. The user prunes one or names
// one explicitly; ferry never picks silently.
type TieError struct {
	Refs   []BundleRef
	Claims []Claim
}

func (e *TieError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "work: %d bundles share the highest sequence %06d — two accounts packed without an intervening receive:\n", len(e.Refs), e.Refs[0].Seq)
	for _, r := range e.Refs {
		owner := "unknown"
		for _, c := range e.Claims {
			for _, ev := range c.Events {
				if ev.Op == OpPack && ev.Seq == r.Seq && ev.Bundle == r.SHA256 {
					owner = c.Account
				}
			}
		}
		fmt.Fprintf(&b, "  %s (packed by %s)\n", filepath.Base(r.Path), owner)
	}
	b.WriteString("prune one, or name one explicitly to receive it")
	return b.String()
}

// DivergedError reports a guarded-overwrite refusal: the destination changed
// since this account last held the baton (or was already populated before a
// first receive).
type DivergedError struct {
	Item string
	Dest string
	Why  string
}

func (e *DivergedError) Error() string {
	return fmt.Sprintf("work: refusing to overwrite %s (%s): %s — review it, then re-run with --force to replace it", e.Dest, e.Item, e.Why)
}

// SupersededError reports that the newest bundle was taken back at the source
// (Alice packed, reclaimed the baton, and kept working).
type SupersededError struct{ Ref BundleRef }

func (e *SupersededError) Error() string {
	return fmt.Sprintf("work: bundle %06d was taken back by its packer and superseded at the source — receive it anyway with --force, or wait for a fresh pack", e.Ref.Seq)
}

// cargoContent is one verified, extracted cargo file.
type cargoContent struct {
	data []byte
	mode os.FileMode
}

// pendingWrite is one planned destination mutation.
type pendingWrite struct {
	path string
	data []byte
	mode os.FileMode
}

// Receive lands the project's latest cargo at this account: locate the cargo
// (by sequence, never mtime), verify every byte against the manifest, apply
// the per-item receive policies (guarded overwrite / union merge) behind the
// divergence guards, snapshot the affected paths, write backup-first, and
// record state and claim. On the account that packed the chosen bundle it is
// a take-back instead: clear the handover marker, record the claim, restore
// nothing (unless Force).
func Receive(st *Store, eng *backup.Engine, lc Locator, id Identity, state *State, opts ReceiveOptions) (*ReceiveResult, error) {
	if !claimAccount.MatchString(opts.Account) {
		return nil, fmt.Errorf("work: claim account %q is not of the form user@host", opts.Account)
	}
	key, refs, err := st.LocateProject(id)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("work: no cargo for this project in %s — nothing has been packed for it", st.Root())
	}
	claims, err := st.Claims(key)
	if err != nil {
		return nil, err
	}

	chosen, err := chooseBundle(refs, claims, opts.BundleSHA256)
	if err != nil {
		return nil, err
	}
	m, contents, err := readBundle(chosen.Path, chosen.SHA256)
	if err != nil {
		return nil, err
	}
	if !intersects(m.Roots, id.Roots) {
		return nil, fmt.Errorf("work: bundle %06d belongs to a different project (no shared root commit)", chosen.Seq)
	}
	if m.Seq != chosen.Seq {
		return nil, fmt.Errorf("work: bundle %s declares sequence %d but is stored as %06d — the store looks tampered with", filepath.Base(chosen.Path), m.Seq, chosen.Seq)
	}

	// v1 landing surface: the project repo itself must live under $HOME so
	// every write below is either $HOME-contained (memory, transcripts) or
	// inside the repo's guarded .work.local.
	if err := projectUnderHome(lc); err != nil {
		return nil, err
	}

	// Take-back: the packer reclaiming their own baton before anyone landed
	// it. Clears the marker and records the claim; restores nothing.
	if m.PackedBy == opts.Account && !opts.Force {
		dir, err := workLocalDir(lc.ProjectDir)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(filepath.Join(dir, HandoverMarkerName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if err := st.AppendClaim(key, opts.Account, ClaimEvent{Op: OpTakeBack, Seq: chosen.Seq, Bundle: chosen.SHA256, At: opts.Now}); err != nil {
			return nil, err
		}
		return &ReceiveResult{Ref: chosen, Manifest: m, TakeBack: true}, nil
	}
	if !opts.Force && takenBack(claims, m.PackedBy, chosen) {
		return nil, &SupersededError{Ref: chosen}
	}

	// The lock (when taken) must be released on EVERY exit below, including a
	// plan that failed after acquiring it — register the defer before the
	// error check.
	writes, removals, skipped, unlock, err := planReceive(lc, m, contents, state, opts.Force)
	if unlock != nil {
		defer unlock()
	}
	if err != nil {
		return nil, err
	}

	affected := make([]string, 0, len(writes)+len(removals))
	for _, w := range writes {
		affected = append(affected, w.path)
	}
	affected = append(affected, removals...)
	sort.Strings(affected)

	snapID, err := eng.Snapshot(affected)
	if err != nil {
		return nil, err
	}
	// Persist the receive record BEFORE writing anything: a mid-write failure
	// must leave `ferry work restore` pointing at THIS receive's snapshot —
	// not nil, and never at the previous receive's — so the advertised
	// recovery genuinely reverts the partial landing.
	state.LastReceive = &ReceiveRecord{SnapshotID: snapID, Seq: chosen.Seq, Bundle: chosen.SHA256, At: opts.Now, Paths: affected}
	state.RecordWritten(affected...)
	if err := state.Save(); err != nil {
		return nil, err
	}
	run, err := eng.Begin()
	if err != nil {
		return nil, err
	}
	for _, w := range writes {
		if err := eng.BackupAndWrite(run, w.path, w.data, w.mode); err != nil {
			return nil, fmt.Errorf("work: landing %s failed (`ferry work restore` reverts the partial receive): %w", w.path, err)
		}
	}
	for _, p := range removals {
		if err := eng.BackupAndRemove(run, p); err != nil {
			return nil, fmt.Errorf("work: removing %s failed (`ferry work restore` reverts the partial receive): %w", p, err)
		}
	}
	if err := run.Commit(); err != nil {
		return nil, err
	}

	state.Baseline = &Baseline{Op: OpReceive, Seq: chosen.Seq, Bundle: chosen.SHA256, At: opts.Now, Files: manifestHashes(m)}
	if err := state.Save(); err != nil {
		return nil, fmt.Errorf("work: cargo landed but saving local state failed: %w", err)
	}
	if err := st.AppendClaim(key, opts.Account, ClaimEvent{Op: OpReceive, Seq: chosen.Seq, Bundle: chosen.SHA256, At: opts.Now}); err != nil {
		return nil, fmt.Errorf("work: cargo landed but recording the claim failed: %w", err)
	}
	return &ReceiveResult{Ref: chosen, Manifest: m, Written: affected, Skipped: skipped, SnapshotID: snapID}, nil
}

// chooseBundle picks the bundle to receive: an explicitly named one, or the
// single bundle at the highest sequence — an equal-seq tie there is refused.
func chooseBundle(refs []BundleRef, claims []Claim, explicit string) (BundleRef, error) {
	if explicit != "" {
		for _, r := range refs {
			if r.SHA256 == explicit {
				return r, nil
			}
		}
		return BundleRef{}, fmt.Errorf("work: no bundle with hash %s in the store", explicit)
	}
	top := refs[len(refs)-1].Seq
	var tied []BundleRef
	for _, r := range refs {
		if r.Seq == top {
			tied = append(tied, r)
		}
	}
	if len(tied) > 1 {
		return BundleRef{}, &TieError{Refs: tied, Claims: claims}
	}
	return tied[0], nil
}

// planReceive builds the write/remove plan for every included item under its
// receive policy. It returns an unlock func when the transcript store's
// advisory lock was taken (held across the writes).
func planReceive(lc Locator, m *Manifest, contents map[string]cargoContent, state *State, force bool) (writes []pendingWrite, removals []string, skipped []string, unlock func(), err error) {
	items := BuiltinItems()
	byName := map[string]Item{}
	for _, it := range items {
		byName[it.Name] = it
	}
	// The in-repo targets are gated by the .work.local symlink guard once,
	// up front (both file items live there).
	if _, err := workLocalDir(lc.ProjectDir); err != nil {
		return nil, nil, nil, nil, err
	}

	baseline := map[string]map[string]string{}
	if state.Baseline != nil {
		baseline = state.Baseline.Files
	}

	for _, mi := range m.Items {
		if !mi.Included {
			continue
		}
		it, ok := byName[mi.Name]
		if !ok {
			return nil, nil, nil, unlock, fmt.Errorf("work: manifest names item %q, which this ferry's registry does not know", mi.Name)
		}
		root, err := it.Locate(lc)
		if err != nil {
			return nil, nil, nil, unlock, err
		}
		switch {
		case it.Policy == PolicyGuardedOverwrite && it.Kind == KindFile:
			if len(mi.Files) != 1 {
				return nil, nil, nil, unlock, fmt.Errorf("work: item %q must carry exactly one file, has %d", mi.Name, len(mi.Files))
			}
			mf := mi.Files[0]
			if mf.Path != filepath.Base(root) {
				return nil, nil, nil, unlock, fmt.Errorf("work: item %q names %q, expected %q", mi.Name, mf.Path, filepath.Base(root))
			}
			in := contents[mi.Name+"/"+mf.Path]
			destHash, exists, err := hashFileIfExists(root)
			if err != nil {
				return nil, nil, nil, unlock, err
			}
			if exists && destHash == mf.SHA256 {
				skipped = append(skipped, root)
				continue
			}
			if exists && !force {
				if why, diverged := guardAgainstBaseline(baseline[mi.Name], mf.Path, destHash); diverged {
					return nil, nil, nil, unlock, &DivergedError{Item: mi.Name, Dest: root, Why: why}
				}
			}
			writes = append(writes, pendingWrite{path: root, data: in.data, mode: in.mode})

		case it.Policy == PolicyGuardedOverwrite && it.Kind == KindDir:
			existing, err := hashDir(root)
			if err != nil {
				return nil, nil, nil, unlock, err
			}
			incoming := map[string]ManifestFile{}
			for _, mf := range mi.Files {
				incoming[mf.Path] = mf
			}
			if !force {
				for rel, destHash := range existing {
					if mf, ok := incoming[rel]; ok && destHash == mf.SHA256 {
						continue
					}
					if why, diverged := guardAgainstBaseline(baseline[mi.Name], rel, destHash); diverged {
						return nil, nil, nil, unlock, &DivergedError{Item: mi.Name, Dest: filepath.Join(root, filepath.FromSlash(rel)), Why: why}
					}
				}
			}
			// Guarded REPLACE: destination becomes exactly the cargo.
			for _, mf := range mi.Files {
				dest := filepath.Join(root, filepath.FromSlash(mf.Path))
				if existing[mf.Path] == mf.SHA256 {
					skipped = append(skipped, dest)
					continue
				}
				in := contents[mi.Name+"/"+mf.Path]
				writes = append(writes, pendingWrite{path: dest, data: in.data, mode: in.mode})
			}
			for rel := range existing {
				if _, ok := incoming[rel]; !ok {
					removals = append(removals, filepath.Join(root, filepath.FromSlash(rel)))
				}
			}

		case it.Policy == PolicyUnionMerge:
			var trWrites []pendingWrite
			existing, err := hashDir(root)
			if err != nil {
				return nil, nil, nil, unlock, err
			}
			for _, mf := range mi.Files {
				dest := filepath.Join(root, filepath.FromSlash(mf.Path))
				if _, ok := existing[mf.Path]; ok {
					skipped = append(skipped, dest)
					continue
				}
				in := contents[mi.Name+"/"+mf.Path]
				trWrites = append(trWrites, pendingWrite{path: dest, data: in.data, mode: in.mode})
			}
			if len(trWrites) > 0 {
				// Take the transcript store's advisory lock while writing so
				// the receive cannot interleave with a live session-end
				// capture.
				release, err := acquireDirLock(root)
				if err != nil {
					return nil, nil, nil, unlock, err
				}
				unlock = release
				writes = append(writes, trWrites...)
			}

		default:
			return nil, nil, nil, unlock, fmt.Errorf("work: item %q has unsupported policy/kind %s/%s", mi.Name, it.Policy, it.Kind)
		}
	}
	return writes, removals, skipped, unlock, nil
}

// guardAgainstBaseline decides whether a destination file's current hash
// diverges from what this account last held.
func guardAgainstBaseline(itemBaseline map[string]string, rel, destHash string) (string, bool) {
	base, ok := itemBaseline[rel]
	if !ok {
		return "it is already populated and this account has no baseline for it (first receive into a non-empty destination)", true
	}
	if base != destHash {
		return "it changed since this account last held the baton", true
	}
	return "", false
}

// takenBack reports whether the packer recorded a take-back for the bundle.
func takenBack(claims []Claim, packer string, ref BundleRef) bool {
	for _, c := range claims {
		if c.Account != packer {
			continue
		}
		for _, ev := range c.Events {
			if ev.Op == OpTakeBack && ev.Seq == ref.Seq && (ev.Bundle == "" || ev.Bundle == ref.SHA256) {
				return true
			}
		}
	}
	return false
}

// projectUnderHome enforces the v1 landing rule: the project repo must live
// under this account's $HOME (out-of-home repos are a documented limitation,
// not silently unguarded writes).
func projectUnderHome(lc Locator) error {
	home, err := filepath.EvalSymlinks(lc.Home)
	if err != nil {
		return err
	}
	repo, err := filepath.EvalSymlinks(lc.ProjectDir)
	if err != nil {
		return err
	}
	if repo != home && !strings.HasPrefix(repo, home+string(filepath.Separator)) {
		return fmt.Errorf("work: project %s is outside this account's home %s — v1 lands work state only for in-home repos", lc.ProjectDir, lc.Home)
	}
	return nil
}

// hashFileIfExists returns the sha256 of a regular file, or exists=false.
func hashFileIfExists(path string) (string, bool, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("work: %s is not a regular file (mode %v)", path, fi.Mode())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true, nil
}

// hashDir maps every regular file under root (recursively) to its content
// hash, keyed by forward-slash rel path. A missing root is an empty map; a
// non-regular entry is a refusal.
func hashDir(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if p == root && errors.Is(walkErr, fs.ErrNotExist) {
				return filepath.SkipAll
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("work: %s is not a regular file (mode %v)", p, d.Type())
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// acquireDirLock takes the advisory lock file in dir (creating dir first),
// returning the release func. A held lock is a refusal, not a wait.
func acquireDirLock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lock := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("work: %s is locked (a live session capture may be writing) — retry when it finishes, or remove the stale lock", lock)
	}
	if err != nil {
		return nil, err
	}
	f.Close()
	return func() { os.Remove(lock) }, nil
}

// manifestHashes indexes a manifest's included files as item -> rel -> sha.
func manifestHashes(m *Manifest) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, mi := range m.Items {
		if !mi.Included {
			continue
		}
		for _, mf := range mi.Files {
			if out[mi.Name] == nil {
				out[mi.Name] = map[string]string{}
			}
			out[mi.Name][mf.Path] = mf.SHA256
		}
	}
	return out
}

// intersects reports whether two sorted root-SHA sets share a member.
func intersects(a, b []string) bool {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	for _, y := range b {
		if set[y] {
			return true
		}
	}
	return false
}

// readBundle reads and fully verifies a cargo bundle: the file's overall
// hash must match its name, every manifest-listed file must be present with
// matching size and hash, and no unlisted member may exist. Cargo is
// untrusted input from shared media — nothing is landed on trust.
func readBundle(path, wantSHA string) (*Manifest, map[string]cargoContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, nil, fmt.Errorf("work: bundle %s content hash %s does not match its name — the file was modified in the store", filepath.Base(path), got[:12])
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("work: %s is not a cargo bundle: %w", filepath.Base(path), err)
	}
	m, err := readManifestMember(zr)
	if err != nil {
		return nil, nil, err
	}
	expected := map[string]ManifestFile{}
	for _, mi := range m.Items {
		for _, mf := range mi.Files {
			expected[mi.Name+"/"+mf.Path] = mf
		}
	}
	contents := map[string]cargoContent{}
	for _, f := range zr.File {
		if f.Name == ManifestMember {
			continue
		}
		mf, ok := expected[f.Name]
		if !ok {
			return nil, nil, fmt.Errorf("work: bundle member %q is not in the manifest — refusing the bundle", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		// Read at most the declared size + 1: a decompression bomb or a
		// tampered member shows up as a size mismatch, never as unbounded
		// memory.
		buf, err := io.ReadAll(io.LimitReader(rc, mf.Size+1))
		rc.Close()
		if err != nil {
			return nil, nil, err
		}
		if int64(len(buf)) != mf.Size {
			return nil, nil, fmt.Errorf("work: bundle member %q size %d does not match the manifest's %d", f.Name, len(buf), mf.Size)
		}
		s := sha256.Sum256(buf)
		if hex.EncodeToString(s[:]) != mf.SHA256 {
			return nil, nil, fmt.Errorf("work: bundle member %q content does not match its manifest hash", f.Name)
		}
		contents[f.Name] = cargoContent{data: buf, mode: cargoMode(f.Mode())}
	}
	for name := range expected {
		if _, ok := contents[name]; !ok {
			return nil, nil, fmt.Errorf("work: manifest lists %q but the bundle has no such member", name)
		}
	}
	return m, contents, nil
}

// readManifestMember extracts and decodes the manifest from an open zip.
func readManifestMember(zr *zip.Reader) (*Manifest, error) {
	for _, f := range zr.File {
		if f.Name != ManifestMember {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		// The manifest itself is small; 8 MiB is far beyond any real one and
		// bounds a tampered header.
		data, err := io.ReadAll(io.LimitReader(rc, 8<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		return DecodeManifest(data)
	}
	return nil, fmt.Errorf("work: bundle has no %s member", ManifestMember)
}

// WorkRestore reverts exactly the last receive from its per-receive snapshot
// and clears the record, so a second call is a clear "nothing to revert".
func WorkRestore(eng *backup.Engine, state *State) (string, error) {
	if state.LastReceive == nil {
		return "", fmt.Errorf("work: no receive to revert on this account")
	}
	snapID := state.LastReceive.SnapshotID
	if err := eng.RestoreSnapshot(snapID); err != nil {
		return "", err
	}
	state.LastReceive = nil
	if err := state.Save(); err != nil {
		return "", err
	}
	return snapID, nil
}

// LocateProject finds the store directory holding this project's cargo: the
// computed-key directory first, then a manifest scan of the remaining store
// directories for a root-SHA-set intersection (a subtree import can reorder
// rev-list output without orphaning existing cargo). A corrupt foreign
// bundle never blocks the scan — it is simply not a match.
func (st *Store) LocateProject(id Identity) (string, []BundleRef, error) {
	refs, err := st.Bundles(id.Key)
	if err != nil {
		return "", nil, err
	}
	if len(refs) > 0 {
		return id.Key, refs, nil
	}
	entries, err := os.ReadDir(st.root)
	if errors.Is(err, fs.ErrNotExist) {
		return id.Key, nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == id.Key || !rootSHA.MatchString(e.Name()) {
			continue
		}
		cand, err := st.Bundles(e.Name())
		if err != nil || len(cand) == 0 {
			continue
		}
		newest := cand[len(cand)-1]
		m, _, err := readBundle(newest.Path, newest.SHA256)
		if err != nil {
			continue
		}
		if intersects(m.Roots, id.Roots) {
			return e.Name(), cand, nil
		}
	}
	return id.Key, nil, nil
}
