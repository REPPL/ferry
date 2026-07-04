package agents

import (
	"os"

	"github.com/REPPL/ferry/internal/dotfile"
)

// instrPerm / hookPerm are the modes for a first-ever write of an agents
// target: instruction/asset files are plain 0644; a source with an executable
// bit (hooks are scripts) materialises 0755 so it stays runnable. An existing
// destination's mode is preserved, matching the dotfile domain.
const (
	instrPerm os.FileMode = 0o644
	hookPerm  os.FileMode = 0o755
)

// ApplyItem materialises one planned agents item onto its $HOME destination,
// honouring the same three-way state machine as the dotfile domain (the item's
// derived Content stands in for the repo side via dotfile.ClassifyContent):
//
//   - clean: hash-gated no-op (an identical pre-existing file is adopted into
//     last-applied via PendingHash);
//   - missing / repo-ahead: backed-up atomic write through the Backuper;
//   - locally-drifted: SKIPPED without force — the domain is repo-authoritative,
//     but ferry still never silently discards a live edit; --force overwrites
//     (backed up, reversible);
//   - conflict: refused with *dotfile.ConflictError unless force.
//
// The empty-over-substantial data-loss guard applies exactly as for dotfiles.
// Writes never persist last-applied directly: the hash rides on
// Result.PendingHash for the caller to commit AFTER the journal commit
// (dotfile.CommitLastApplied), so a crash can never leave last-applied ahead
// of a rolled-back file.
func ApplyItem(it Item, store *dotfile.Store, b dotfile.Backuper, force bool) (dotfile.Result, error) {
	st, err := dotfile.ClassifyContent(it.Target, it.Content, store)
	if err != nil {
		return dotfile.Result{}, err
	}
	res := dotfile.Result{Target: it.Target, State: st.State}

	switch st.State {
	case dotfile.StateClean:
		// Adopt an identical pre-existing file into last-applied so a later
		// repo advance classifies as repo-ahead, not a false conflict.
		if st.AppliedHash != st.RepoHash {
			res.PendingHash = st.RepoHash
		}
		res.Action = dotfile.ActionNoop
		return res, nil
	case dotfile.StateLocallyDrifted:
		if !force {
			res.Action = dotfile.ActionSkipped
			return res, nil
		}
	case dotfile.StateConflict:
		if !force {
			res.Action = dotfile.ActionConflict
			return res, &dotfile.ConflictError{Result: res}
		}
	}

	// Write path: missing, repo-ahead, or a force-overridden drift/conflict.
	action := dotfile.ActionUpdated
	if !st.LiveExists {
		action = dotfile.ActionCreated
	}
	if st.LiveExists {
		if gerr := guardEmptyOverSubstantial(it, force, &res); gerr != nil {
			return res, gerr
		}
	}
	if err := b.BackupAndWrite(it.Target.Home, it.Content, permFor(it)); err != nil {
		return dotfile.Result{}, err
	}
	res.PendingHash = st.RepoHash
	res.Action = action
	return res, nil
}

// permFor picks the mode for a write: an existing regular destination keeps
// its mode (the dotfile convention); a fresh write takes the source's
// executable-ness (hooks run, instructions don't).
func permFor(it Item) os.FileMode {
	if fi, err := os.Lstat(it.Target.Home); err == nil && fi.Mode().IsRegular() {
		return fi.Mode().Perm()
	}
	if it.Exec {
		return hookPerm
	}
	return instrPerm
}

// guardEmptyOverSubstantial enforces the dotfile domain's data-loss guard on
// an in-memory agents item: replacing a SUBSTANTIAL live file with
// empty/near-empty content is refused without --force (nothing written) and
// flagged for a loud warning when --force overrides it. Judged by the SAME
// exported metric as the dotfile apply path, so the two domains can never
// disagree on what counts as data loss.
func guardEmptyOverSubstantial(it Item, force bool, res *dotfile.Result) error {
	if !dotfile.IsNearEmpty(it.Content) {
		return nil
	}
	live, err := os.ReadFile(it.Target.Home)
	if err != nil {
		return nil // unreadable live file: leave it to the write path's own error
	}
	if !dotfile.IsSubstantial(live) {
		return nil
	}
	if !force {
		res.Action = dotfile.ActionConflict
		return &dotfile.EmptyOverSubstantialError{
			Result:   *res,
			Path:     it.Target.Home,
			LiveSize: dotfile.SignificantBytes(live),
		}
	}
	res.ForcedEmptyOverSubstantial = true
	res.ForcedPath = it.Target.Home
	return nil
}
