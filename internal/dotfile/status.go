package dotfile

import "fmt"

// State is the three-way classification of a single target, derived from the
// live file, the last-applied hash, and the repo content. status and diff
// commands render this; apply acts on it.
type State string

const (
	// StateClean: live == repo == last-applied. Nothing to do.
	StateClean State = "clean"
	// StateRepoAhead: live == last-applied (no local edits) but repo changed.
	// apply will update the home target. (Includes the not-yet-deployed case
	// where the home file is absent and last-applied is recorded — apply writes
	// it.)
	StateRepoAhead State = "repo-ahead"
	// StateLocallyDrifted: live differs from last-applied but the repo has NOT
	// moved past last-applied (repo == last-applied). The local edit is a
	// capture candidate; apply leaves it alone (no repo change to push).
	StateLocallyDrifted State = "locally-drifted"
	// StateConflict: live differs from last-applied AND the repo also moved
	// (repo != last-applied), OR the home target exists with managed-looking
	// content but ferry has no last-applied record for it. Both directions want
	// the file; apply refuses without --force.
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
		// Home file exists but ferry never applied it. If it already matches the
		// repo, treat it as clean (adopting an identical pre-existing file is
		// safe). Otherwise overwriting would clobber unmanaged content -> conflict.
		if liveHash == repoHash {
			return StateClean
		}
		return StateConflict
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
