package dotfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Action is what Apply did (or, in dry-run, would do) for one target.
type Action string

const (
	// ActionNoop: live already matches the repo; nothing written.
	ActionNoop Action = "noop"
	// ActionUpdated: the repo content was copied to the home target (via the
	// Backuper, so it was backed up first and written atomically) and the
	// last-applied hash advanced.
	ActionUpdated Action = "updated"
	// ActionCreated: the home target did not exist and was materialized.
	ActionCreated Action = "created"
	// ActionConflict: apply refused because overwriting would clobber an
	// uncaptured edit to a file ferry previously managed; nothing was written.
	// The caller reports "run capture, or apply --force".
	ActionConflict Action = "conflict"
	// ActionSkipped: target is locally-drifted (a capture candidate) with no
	// repo change to push; apply leaves it for capture and writes nothing.
	ActionSkipped Action = "skipped"
)

// Result reports the outcome of applying one target.
//
// PendingHash carries the last-applied hash this apply WROTE to the home target
// but has NOT yet persisted to the store — it is set only by ApplyDeferred (the
// crash-safe path), and only when a write actually happened. The caller persists
// it with CommitLastApplied AFTER the surrounding journal commit, so last-applied
// can never get ahead of a rolled-back file. It is empty for noop/skipped/conflict
// results and for the eager Apply path (which persists immediately).
type Result struct {
	Target Target
	State  State  // the three-way state apply observed
	Action Action // what apply did

	PendingHash string // last-applied hash to commit post-journal (ApplyDeferred only)
}

// ConflictError is returned by Apply when it refuses to overwrite. It carries
// the Result so callers can render guidance ("run capture, or apply --force").
type ConflictError struct {
	Result Result
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("dotfile: %q has a conflict (state %s); run capture, or apply --force",
		e.Result.Target.Name, e.Result.State)
}

// Apply materializes one target from the repo onto the home destination,
// honouring the three-way state. It writes ONLY through the Backuper, so any
// overwrite is backed up first and the write is atomic. force discards local
// edits (still backed up). dryRun computes the decision and Result but writes
// nothing.
//
// On a refused overwrite it returns a *ConflictError (Result.Action ==
// ActionConflict) so a caller can both detect the conflict via errors.As and
// render the Result.
//
// Apply persists last-applied EAGERLY (immediately after the write). For a
// crash-safe apply that must not let last-applied get ahead of an uncommitted
// journal, use ApplyDeferred + CommitLastApplied instead.
func Apply(t Target, store *Store, b Backuper, force, dryRun bool) (Result, error) {
	return apply(t, store, b, force, dryRun, true)
}

// ApplyDeferred behaves like Apply but does NOT persist last-applied. When it
// performs a write, the hash to record is returned in Result.PendingHash; the
// caller must persist it with CommitLastApplied AFTER committing the surrounding
// journal. This decouples last-applied from the file write so a crash/commit
// failure between the write and the journal commit can never leave last-applied
// ahead of a rolled-back file (Codex#3).
//
// Note the StateClean adoption case (an identical pre-existing file) advances
// last-applied via PendingHash too, so the caller's CommitLastApplied still
// records it post-commit.
func ApplyDeferred(t Target, store *Store, b Backuper, force, dryRun bool) (Result, error) {
	return apply(t, store, b, force, dryRun, false)
}

// apply is the shared core. persist=true records last-applied immediately
// (eager Apply); persist=false leaves it to the caller via Result.PendingHash
// (deferred ApplyDeferred + CommitLastApplied).
func apply(t Target, store *Store, b Backuper, force, dryRun, persist bool) (Result, error) {
	st, err := Classify(t, store)
	if err != nil {
		return Result{}, err
	}
	res := Result{Target: t, State: st.State}

	// A conflict is overridable by force; otherwise refuse.
	if st.State == StateConflict && !force {
		res.Action = ActionConflict
		return res, &ConflictError{Result: res}
	}

	switch st.State {
	case StateClean:
		// Live already matches repo. If last-applied does not yet record this hash
		// (an identical pre-existing file we adopted, or a not-yet-recorded match),
		// adopt it into last-applied now so a later repo advance classifies as a
		// clean repo-ahead update instead of a false conflict. Skip in dry-run.
		if !dryRun && st.AppliedHash != st.RepoHash {
			if persist {
				if err := store.set(t.Name, st.RepoHash); err != nil {
					return Result{}, err
				}
			} else {
				res.PendingHash = st.RepoHash
			}
		}
		res.Action = ActionNoop
		return res, nil
	case StateLocallyDrifted:
		// Local edits, repo unchanged: nothing to push. Leave for capture, unless
		// force is set — then reset the drifted target to the repo/last-applied
		// content (backed up first), honouring "--force overwrites local edits".
		if force {
			action := ActionUpdated
			if dryRun {
				res.Action = action
				return res, nil
			}
			if err := writeTarget(t, store, b, st.RepoHash, persist, &res); err != nil {
				return Result{}, err
			}
			res.Action = action
			return res, nil
		}
		res.Action = ActionSkipped
		return res, nil
	case StateMissing, StateRepoAhead, StateConflict:
		// Materialize the repo content. (StateConflict only reaches here under
		// force.) Created vs updated is purely cosmetic: present-ness of the
		// home file.
		action := ActionUpdated
		if !st.LiveExists {
			action = ActionCreated
		}
		if dryRun {
			res.Action = action
			return res, nil
		}
		if err := writeTarget(t, store, b, st.RepoHash, persist, &res); err != nil {
			return Result{}, err
		}
		res.Action = action
		return res, nil
	default:
		return Result{}, fmt.Errorf("dotfile: unhandled state %q for %q", st.State, t.Name)
	}
}

// LocalOverlayPath returns the repo-side per-machine overlay source for a
// whole-file-replace target: <repoRoot>/local/<domain>/<bare>. This is the
// gitignored copy capture writes and apply reads (PLAN.md "Local overlay —
// concrete paths"). The domain segment lets one machine's local/<domain>/ hold
// the per-machine full copy that replaces the shared dotfiles/<bare>.
func LocalOverlayPath(repoRoot, domain, name string) string {
	bare := name
	if len(bare) > 0 && bare[0] == '.' {
		bare = bare[1:]
	}
	return filepath.Join(repoRoot, LocalSubdir, domain, bare)
}

// LocalSubdir is the repo subdirectory holding per-machine `.local` overlays.
const LocalSubdir = "local"

// ApplyWholeFileOverlay deploys a whole-file-replace target (a generic dotfile
// with no include point, e.g. .gitconfig): when a per-machine local copy exists
// at localSource it is deployed INSTEAD OF the shared content (local wins);
// otherwise the shared content (t.Repo) is deployed. Either way the deploy goes
// through the normal three-way apply (backup-then-write via the Backuper, atomic,
// conflict-aware), so it is as safe and reversible as a plain Apply.
//
// It REFUSES a target whose Overlay is not OverlayWholeFileReplace, so an
// include-style (zsh sidecar) target can never be silently whole-file-replaced —
// the caller must route those through Apply + its own sidecar materialization.
//
// localSource is the repo-side overlay path (see LocalOverlayPath); pass "" when
// the domain has no local copy on this machine and only the shared content
// should deploy. force/dryRun/persist behave exactly as Apply.
func ApplyWholeFileOverlay(t Target, localSource string, store *Store, b Backuper, force, dryRun bool) (Result, error) {
	t, err := selectOverlaySource(t, localSource)
	if err != nil {
		return Result{}, err
	}
	return Apply(t, store, b, force, dryRun)
}

// ApplyWholeFileOverlayDeferred is the crash-safe counterpart of
// ApplyWholeFileOverlay: it performs the same local-wins source selection and
// deploys through the Backuper, but routes through ApplyDeferred, so it does NOT
// persist last-applied. When a write happens the hash to record is returned in
// Result.PendingHash; the caller persists it with CommitLastApplied AFTER the
// surrounding journal commit. This keeps last-applied from getting ahead of a
// rolled-back file when ferry crashes between the file write and run.Commit()
// (the same ordering guarantee the zsh sidecar path gets via ApplyDeferred).
//
// Like the eager version it REFUSES a target whose Overlay is not
// OverlayWholeFileReplace. The apply command should use THIS variant plus
// CommitLastApplied; the eager ApplyWholeFileOverlay remains for callers that
// want immediate persistence.
func ApplyWholeFileOverlayDeferred(t Target, localSource string, store *Store, b Backuper, force, dryRun bool) (Result, error) {
	t, err := selectOverlaySource(t, localSource)
	if err != nil {
		return Result{}, err
	}
	return ApplyDeferred(t, store, b, force, dryRun)
}

// selectOverlaySource enforces the whole-file-replace contract and resolves
// which bytes are the "repo content" for an overlay deploy. A present local copy
// at localSource replaces the shared source: only t.Repo is repointed; the home
// destination, name, and three-way state machinery are unchanged. Shared by the
// eager and deferred overlay applies.
func selectOverlaySource(t Target, localSource string) (Target, error) {
	if t.Overlay != OverlayWholeFileReplace {
		return Target{}, fmt.Errorf("dotfile: whole-file overlay apply called on %q with overlay mode %q, want %q",
			t.Name, t.Overlay, OverlayWholeFileReplace)
	}
	if localSource != "" {
		if _, err := os.Lstat(localSource); err == nil {
			t.Repo = localSource
		} else if !errors.Is(err, os.ErrNotExist) {
			return Target{}, err
		}
	}
	return t, nil
}

// CommitLastApplied persists the deferred last-applied hashes recorded by
// ApplyDeferred. The caller runs it AFTER the surrounding journal commit, so a
// crash before commit leaves last-applied untouched (matching the rolled-back
// file) and a successful commit is followed by the matching last-applied write.
// Results with no PendingHash (noop/skipped/conflict, or eager Apply) are
// ignored, so passing the full result slice is safe.
func CommitLastApplied(results []Result, store *Store) error {
	for _, r := range results {
		if r.PendingHash == "" {
			continue
		}
		if err := store.set(r.Target.Name, r.PendingHash); err != nil {
			return err
		}
	}
	return nil
}

// writeTarget copies the repo content to the home destination through the
// Backuper and records the new last-applied hash. When persist is true the hash
// is written to the store immediately (only after a successful write, so a failed
// write never advances last-applied); when false it is recorded on res.PendingHash
// for the caller to commit post-journal.
func writeTarget(t Target, store *Store, b Backuper, repoHash string, persist bool, res *Result) error {
	if b == nil {
		return errors.New("dotfile: nil Backuper")
	}
	content, err := os.ReadFile(t.Repo)
	if err != nil {
		return err
	}
	// Defend the invariant: the bytes we are about to write must hash to the
	// repoHash Classify computed, or last-applied would record a lie.
	if hashBytes(content) != repoHash {
		return fmt.Errorf("dotfile: repo source for %q changed mid-apply", t.Name)
	}

	perm := defaultPerm
	if fi, err := os.Lstat(t.Home); err == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm() // preserve an existing destination's mode
	}

	if err := b.BackupAndWrite(t.Home, content, perm); err != nil {
		return err
	}
	if persist {
		return store.set(t.Name, repoHash)
	}
	res.PendingHash = repoHash
	return nil
}

// UpdateLastApplied advances a target's last-applied hash to the current live
// file's hash — but ONLY when the live file now reproduces the managed output,
// i.e. live == repo. The capture path calls this after writing the repo
// (shared (+) local): if capture was full, live == repo and last-applied
// advances so status reports clean; if capture was PARTIAL (some hunks
// rejected, live still differs from the repo), last-applied is left untouched so
// status keeps reporting the remaining uncaptured drift, never a false conflict
// or hidden drift.
//
// It returns updated=true when it advanced the hash, false when it deliberately
// left it put (the partial-capture case).
func UpdateLastApplied(t Target, store *Store) (updated bool, err error) {
	repoHash, repoExists, err := hashFile(t.Repo)
	if err != nil {
		return false, err
	}
	if !repoExists {
		return false, fmt.Errorf("dotfile: repo source missing for %q (%s)", t.Name, t.Repo)
	}
	liveHash, liveExists, err := hashFile(t.Home)
	if err != nil {
		return false, err
	}
	if !liveExists || liveHash != repoHash {
		// Capture did not fully reproduce the live file; leave last-applied put.
		return false, nil
	}
	if err := store.set(t.Name, repoHash); err != nil {
		return false, err
	}
	return true, nil
}
