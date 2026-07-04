package dotfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// ForcedEmptyOverSubstantial is set when --force pushed an empty/near-empty
	// repo source OVER a substantial existing live file (the data-loss transition
	// the guard normally refuses). The caller MUST surface a warning naming the
	// file and both sides of the hazard; ForcedPath carries the erased path.
	ForcedEmptyOverSubstantial bool
	ForcedPath                 string
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

// substantialThreshold is the number of SIGNIFICANT bytes (non-whitespace,
// non-comment) at or above which an existing live file counts as "substantial"
// for the empty-over-substantial data-loss guard. A realistic ~/.zshrc (dozens
// of alias/export lines) is far above this; a blank, whitespace-only, or
// comments-only file is at zero. 64 bytes ≈ several real config lines — enough to
// clearly separate a meaningful config from a placeholder.
const substantialThreshold = 64

// EmptyOverSubstantialError is returned by Apply when, WITHOUT --force, it would
// replace a SUBSTANTIAL existing live file with an EMPTY or near-empty (blank /
// whitespace-only / comments-only) repo source. This is the confirmed data-loss
// transition (a fresh init's empty seed zeroing a real ~/.zshrc); apply refuses
// it and writes nothing, leaving the live file byte-, mode-, and mtime-identical.
// The message names the file and BOTH sides of the hazard so the refusal is
// user-honest, and points at the escape hatches (--force / capture).
type EmptyOverSubstantialError struct {
	Result   Result
	Path     string // the live home path that would be erased
	LiveSize int    // significant-byte count of the live file
}

func (e *EmptyOverSubstantialError) Error() string {
	return fmt.Sprintf(
		"refusing to replace %s (a substantial existing file, ~%d bytes of real content) with an empty/blank repo source — this would erase your config. Re-run with --force to overwrite anyway, or run `ferry capture` first to save the current file into the repo",
		e.Path, e.LiveSize)
}

// significantBytes counts the bytes of content that are NOT whitespace and NOT
// part of a comment line (a line whose first non-whitespace rune is '#' or ';').
// It is the metric behind "near-empty" (repo source) and "substantial" (live
// file): a blank, whitespace-only, or comments-only file scores 0; a real config
// scores its meaningful body size. Both '#' and ';' are treated as leading
// comment markers so the metric covers shells/rc files (# comments) AND
// gitconfig/ini files (; comments). A leading UTF-8 BOM is stripped before
// counting so a BOM-only or BOM+comment file still scores 0. A line that merely
// contains a '#' or ';' mid-content still counts (only a leading marker is a
// comment).
func significantBytes(content []byte) int {
	// Strip a leading UTF-8 BOM (0xEF 0xBB 0xBF) so it doesn't mask an otherwise
	// empty/comment-only file as significant.
	s := strings.TrimPrefix(string(content), "\uFEFF")
	n := 0
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		n += len(trimmed)
	}
	return n
}

// isNearEmpty reports whether content is empty, whitespace-only, or comments-only
// — i.e. carries no significant bytes. This is the "empty/near-empty repo source"
// side of the data-loss guard.
func isNearEmpty(content []byte) bool { return significantBytes(content) == 0 }

// IsNearEmpty reports whether content carries no significant bytes (empty,
// whitespace-only, or comments-only — treating a line beginning with '#' or ';'
// as a comment, after stripping a leading BOM). It is the exported form of the
// data-loss guard's near-empty metric so callers outside this package (e.g. init's
// adopt-existing-dotfile check) share ONE definition of "nothing worth managing".
func IsNearEmpty(content []byte) bool { return isNearEmpty(content) }

// isSubstantial reports whether content carries at least substantialThreshold
// significant bytes — the "substantial live file" side of the guard.
func isSubstantial(content []byte) bool { return significantBytes(content) >= substantialThreshold }

// IsSubstantial is the exported form of the guard's "substantial live file"
// test, so callers outside this package that deploy in-memory content (the
// agents domain) enforce the empty-over-substantial data-loss guard by the
// SAME threshold as the dotfile apply path.
func IsSubstantial(content []byte) bool { return isSubstantial(content) }

// SignificantBytes is the exported form of the guard's significance metric
// (non-whitespace, non-comment bytes), so an external caller's refusal message
// can name the live file's real content size the way EmptyOverSubstantialError
// does.
func SignificantBytes(content []byte) int { return significantBytes(content) }

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
			// Data-loss guard: refuse (or, under force, warn about) replacing a
			// substantial live file with an empty/near-empty repo source.
			if err := guardEmptyOverSubstantial(t, force, dryRun, &res); err != nil {
				return res, err
			}
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
		// Data-loss guard: only meaningful when a live file already exists (a
		// created target has nothing to erase). Refuse an empty-over-substantial
		// overwrite without --force; warn (via res) when --force overrides it.
		if st.LiveExists {
			if err := guardEmptyOverSubstantial(t, force, dryRun, &res); err != nil {
				return res, err
			}
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

// ferryOverlayMarker is the exact comment line appendSourceDirective (cmd) emits
// above the injected per-machine overlay `source` directive. It is the anchor the
// guard uses to recognise — and exclude — ferry's OWN generated boilerplate when
// judging whether the USER's managed source is near-empty. It MUST stay
// byte-identical to the marker cmd/apply.go writes.
const ferryOverlayMarker = "# ferry: per-machine overlay, sourced last so it wins"

// stripFerryOverlayDirective removes ferry's injected per-machine overlay block —
// the fixed ferryOverlayMarker comment and the immediately-following generated
// `[ -f ~/<file> ] && source ~/<file>` include line that appendSourceDirective
// (cmd) appends — so near-emptiness is judged on the USER's real managed content,
// not ferry's own boilerplate. This closes the empty-over-substantial BYPASS: for
// the zsh include path apply stages the EFFECTIVE content (raw shared source PLUS
// the injected directive), so a truly empty/comment-only shared source would read
// as "non-empty" once the directive is appended and the guard would wrongly stand
// down. Stripping is PRECISE: it drops only the exact marker comment, and only a
// following line matching ferry's generated `[ -f ~/… ] && source ~/…` shape — a
// user-authored `source`/comment line is never removed, so real content can never
// be hidden to defeat the guard.
func stripFerryOverlayDirective(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == ferryOverlayMarker {
			// Drop the marker, and the next line too when it is ferry's generated
			// include directive (`[ -f ~/… ] && source ~/…`). A non-matching next
			// line is left in place (so nothing user-authored is silently removed).
			if i+1 < len(lines) && isFerryOverlayInclude(lines[i+1]) {
				i++
			}
			continue
		}
		kept = append(kept, lines[i])
	}
	return []byte(strings.Join(kept, "\n"))
}

// isFerryOverlayInclude reports whether line is ferry's generated per-machine
// overlay include — the exact `[ -f ~/<file> ] && source ~/<file>` shape
// appendSourceDirective emits. Matching the guarded `[ -f ~/… ] && source ~/…`
// structure (not a bare `source …`) keeps stripping narrow to ferry's own output.
func isFerryOverlayInclude(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "[ -f ~/") && strings.Contains(t, "] && source ~/")
}

// StripFerryOverlayDirective is the exported form of stripFerryOverlayDirective
// for callers outside this package that must judge near-emptiness by the SAME
// rule as the apply guard (the wizard's minimum-shared-scaffold check): it
// removes ferry's injected per-machine overlay block before the caller runs
// IsNearEmpty over the user's real managed content.
func StripFerryOverlayDirective(content []byte) []byte {
	return stripFerryOverlayDirective(content)
}

// guardEmptyOverSubstantial enforces the empty-over-substantial data-loss guard
// for a target whose live file exists and is about to be overwritten. It reads
// the effective repo source (t.Repo) and the live file (t.Home): when the repo
// source is empty/near-empty (blank/whitespace/comments-only) AND the live file
// is substantial, the transition would erase real config. Without --force it
// returns an *EmptyOverSubstantialError (apply writes nothing, live file
// untouched). With --force it does NOT error but records the hazard on res so the
// caller warns before the overwrite proceeds. Any non-dangerous transition is a
// no-op. A repo-read error is returned (writeTarget would fail anyway); a
// live-read error is treated as "not substantial" (fail open to the normal path —
// the deploy's own write handles a genuinely unreadable target).
//
// Near-emptiness is judged on the USER's real managed content: ferry's injected
// per-machine overlay directive is stripped first (stripFerryOverlayDirective) so
// the zsh include path — which stages raw shared source PLUS ferry's generated
// `source ~/…local` line — cannot smuggle an empty shared source past the guard on
// the strength of ferry's OWN boilerplate.
func guardEmptyOverSubstantial(t Target, force, dryRun bool, res *Result) error {
	repo, err := os.ReadFile(t.Repo)
	if err != nil {
		return err
	}
	if !isNearEmpty(stripFerryOverlayDirective(repo)) {
		return nil // user's managed source has real content: not the dangerous transition.
	}
	live, err := os.ReadFile(t.Home)
	if err != nil {
		return nil // can't read live as a regular file: leave it to the deploy path.
	}
	if !isSubstantial(live) {
		return nil // live file is itself trivial: nothing meaningful to lose.
	}
	if !force {
		res.Action = ActionConflict
		return &EmptyOverSubstantialError{
			Result:   *res,
			Path:     t.Home,
			LiveSize: significantBytes(live),
		}
	}
	// --force: proceed, but flag the hazard so the caller warns. (dryRun never
	// writes, so no warning is recorded on a preview.)
	if !dryRun {
		res.ForcedEmptyOverSubstantial = true
		res.ForcedPath = t.Home
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

// UpdateLastAppliedContent is UpdateLastApplied with the managed-output side
// provided IN MEMORY: it advances t's last-applied to the hash of effective
// ONLY when the live file already reproduces those bytes (live == effective).
// Callers whose effective content is secret-rendered use this variant so the
// rendered bytes are hashed directly and NEVER staged to a temp file (a crash
// window would otherwise leave rendered secrets in $TMPDIR) — mirroring how
// the status/diff path classifies in memory via ClassifyContent.
func UpdateLastAppliedContent(t Target, effective []byte, store *Store) (updated bool, err error) {
	liveHash, liveExists, err := hashFile(t.Home)
	if err != nil {
		return false, err
	}
	effHash := hashBytes(effective)
	if !liveExists || liveHash != effHash {
		// The live file does not reproduce the effective content; leave
		// last-applied put so the remaining drift keeps being reported.
		return false, nil
	}
	if err := store.set(t.Name, effHash); err != nil {
		return false, err
	}
	return true, nil
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
