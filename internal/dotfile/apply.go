package dotfile

import (
	"errors"
	"fmt"
	"os"
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
	// ActionConflict: apply refused because overwriting would clobber uncaptured
	// local edits or an unmanaged file; nothing was written. The caller reports
	// "run capture, or apply --force".
	ActionConflict Action = "conflict"
	// ActionSkipped: target is locally-drifted (a capture candidate) with no
	// repo change to push; apply leaves it for capture and writes nothing.
	ActionSkipped Action = "skipped"
)

// Result reports the outcome of applying one target.
type Result struct {
	Target Target
	State  State  // the three-way state apply observed
	Action Action // what apply did
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
func Apply(t Target, store *Store, b Backuper, force, dryRun bool) (Result, error) {
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
			if err := store.set(t.Name, st.RepoHash); err != nil {
				return Result{}, err
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
			if err := writeTarget(t, store, b, st.RepoHash); err != nil {
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
		if err := writeTarget(t, store, b, st.RepoHash); err != nil {
			return Result{}, err
		}
		res.Action = action
		return res, nil
	default:
		return Result{}, fmt.Errorf("dotfile: unhandled state %q for %q", st.State, t.Name)
	}
}

// writeTarget copies the repo content to the home destination through the
// Backuper and records the new last-applied hash. The hash is recorded only
// after a successful write, so a failed write never advances last-applied.
func writeTarget(t Target, store *Store, b Backuper, repoHash string) error {
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
	return store.set(t.Name, repoHash)
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
