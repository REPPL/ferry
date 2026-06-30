package dotfile

import "fmt"

// State is the three-way classification of a single target, derived from the
// live file, the last-applied hash, and the repo content. status and diff
// commands render this; apply acts on it.
type State string

const (
	// StateClean: live == repo == last-applied. Nothing to do.
	StateClean State = "clean"
	// StateRepoAhead: apply will write the repo content to the home target
	// (backing up the prior state first). This covers three cases that all
	// resolve to "deploy the repo content":
	//   - live == last-applied (no local edits) but repo changed — a normal update;
	//   - the not-yet-deployed case where the home file is absent and last-applied
	//     is recorded — apply writes it;
	//   - FIRST-TOUCH ADOPTION: a pre-existing home file ferry has never managed
	//     (no last-applied record) that DIFFERS from the repo. apply adopts it by
	//     backing the live file up to the immutable baseline (via the Backuper)
	//     and then deploying the repo content. This is deliberately NOT a conflict:
	//     first-ever apply of an in-scope dotfile takes ownership rather than
	//     refusing. The backup-then-write keeps it reversible (restore leaves no
	//     trace); if the backup cannot be made, BackupAndWrite errors and nothing
	//     is deployed.
	StateRepoAhead State = "repo-ahead"
	// StateLocallyDrifted: live differs from last-applied but the repo has NOT
	// moved past last-applied (repo == last-applied). The local edit is a
	// capture candidate; apply leaves it alone (no repo change to push).
	StateLocallyDrifted State = "locally-drifted"
	// StateConflict: ferry HAS a last-applied record for the target AND the live
	// file differs from it (the user edited a file ferry previously managed
	// without capturing) AND the repo also moved past last-applied. Both
	// directions want the file; apply refuses without --force. This is the only
	// conflict case: a pre-existing file ferry has never managed is NOT a conflict
	// (see StateRepoAhead first-touch adoption) — conflict is reserved for the
	// genuine uncaptured-edit-to-a-managed-file case.
	StateConflict State = "conflict"
	// StateMissing: the home target is absent and ferry has no last-applied
	// record — a fresh, never-deployed target. apply will create it.
	StateMissing State = "missing"
)

// Status is the full three-way picture of one target.
type Status struct {
	Target      Target
	State       State
	LiveExists  bool
	LiveHash    string // "" when absent
	RepoHash    string // "" when the repo source is absent
	AppliedHash string // "" when no last-applied record
	HasApplied  bool
}

// Classify computes the three-way state of a target without mutating anything.
// It is the shared core of status, diff, and the apply decision: apply calls it
// and then acts, while status/diff just report the result.
func Classify(t Target, store *Store) (Status, error) {
	repoHash, repoExists, err := hashFile(t.Repo)
	if err != nil {
		return Status{}, err
	}
	if !repoExists {
		// A declared target with no repo source is a configuration error the
		// caller must surface; the domain cannot materialize nothing.
		return Status{}, fmt.Errorf("dotfile: repo source missing for %q (%s)", t.Name, t.Repo)
	}

	liveHash, liveExists, err := hashFile(t.Home)
	if err != nil {
		return Status{}, err
	}
	appliedHash, hasApplied := store.LastApplied(t.Name)

	st := Status{
		Target:      t,
		LiveExists:  liveExists,
		LiveHash:    liveHash,
		RepoHash:    repoHash,
		AppliedHash: appliedHash,
		HasApplied:  hasApplied,
	}
	st.State = classify(liveExists, liveHash, repoHash, appliedHash, hasApplied)
	return st, nil
}

// ClassifyContent computes the SAME three-way state as Classify, but takes the
// effective desired content as in-memory bytes instead of reading the repo
// source at t.Repo. It writes NOTHING to disk: the effective bytes are hashed in
// memory (no temp file), the live file at t.Home is read, and the last-applied
// hash comes from the store. This is the path read-only previews (status, diff,
// apply --dry-run) take so they never stage the (possibly secret-rendered)
// effective content to a temp file.
//
// `effective` is the repo-equivalent content the caller already composed
// (shared⊕local, secret-rendered); it stands in for the repo source side of the
// comparison. The live-file symlink/regular checks and the first-touch adoption
// rule are identical to Classify — both delegate to the same pure classify
// decision table, so file-based and in-memory classification can never diverge
// for identical content.
func ClassifyContent(t Target, effective []byte, store *Store) (Status, error) {
	repoHash := hashBytes(effective)

	liveHash, liveExists, err := hashFile(t.Home)
	if err != nil {
		return Status{}, err
	}
	appliedHash, hasApplied := store.LastApplied(t.Name)

	st := Status{
		Target:      t,
		LiveExists:  liveExists,
		LiveHash:    liveHash,
		RepoHash:    repoHash,
		AppliedHash: appliedHash,
		HasApplied:  hasApplied,
	}
	st.State = classify(liveExists, liveHash, repoHash, appliedHash, hasApplied)
	return st, nil
}

// classify is the pure decision table over the three hashes.
func classify(liveExists bool, liveHash, repoHash, appliedHash string, hasApplied bool) State {
	if !liveExists {
		// Home target absent. If ferry has a last-applied record, the file was
		// deployed and then removed/lost — re-deploy (repo-ahead). Otherwise it
		// has never been deployed.
		if hasApplied {
			return StateRepoAhead
		}
		return StateMissing
	}

	if !hasApplied {
		// Home file exists but ferry never applied it (first-ever touch). If it
		// already matches the repo, treat it as clean (adopting an identical
		// pre-existing file is safe). Otherwise this is FIRST-TOUCH ADOPTION:
		// ferry takes ownership by backing the live file up to the baseline and
		// deploying the repo content. That deploy path is exactly StateRepoAhead,
		// so classify it as such — NOT a conflict. (A conflict requires a
		// last-applied record; without one there is no "uncaptured edit" to
		// protect, only an unmanaged file we are adopting for the first time.)
		if liveHash == repoHash {
			return StateClean
		}
		return StateRepoAhead
	}

	switch {
	case liveHash == repoHash:
		// Live already matches the repo (regardless of last-applied) -> nothing
		// to push.
		return StateClean
	case liveHash == appliedHash:
		// No local edits since last apply, and live != repo, so the repo moved
		// ahead -> update.
		return StateRepoAhead
	case repoHash == appliedHash:
		// Repo unchanged since last apply, but live != applied -> purely local
		// edits, a capture candidate.
		return StateLocallyDrifted
	default:
		// Both live and repo diverged from last-applied -> conflict.
		return StateConflict
	}
}
