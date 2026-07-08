package dotfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Action is what apply did (or, in dry-run, would do) for one target.
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
// but has NOT yet persisted to the store — it is set only by the deferred apply
// (ApplyContentDeferred, the crash-safe path), and only when a write actually
// happened. The caller persists
// it with CommitLastApplied AFTER the surrounding journal commit, so last-applied
// can never get ahead of a rolled-back file. It is empty for noop/skipped/conflict
// results.
type Result struct {
	Target Target
	State  State  // the three-way state apply observed
	Action Action // what apply did

	PendingHash string // last-applied hash to commit post-journal (deferred apply only)

	// PendingContent carries the exact bytes ferry deployed to the home target,
	// to be recorded as the last-deployed baseline alongside PendingHash by the
	// caller's post-journal CommitLastApplied. It is set only when PendingHash is
	// (a write, or a clean-adoption of an identical existing file), so the
	// snapshot advances atomically with the hash and never gets ahead of a
	// rolled-back file. Empty for noop-without-adoption/skipped/conflict results.
	PendingContent []byte

	// SecretRouted marks a target whose deployed bytes were rendered from the
	// secret store (a {{ferry.secret ...}} placeholder was substituted with a
	// real value). For such a target CommitLastApplied records ONLY the content
	// hash and NEVER the PendingContent bytes: the rendered bytes hold plaintext
	// secrets, and ferry's last-applied state file is a non-secret bookkeeping
	// file, so snapshotting them would be a secret-at-rest leak. The hash alone
	// still advances the sync point and drives drift/baseline detection. The apply
	// core stamps this from the secretRouted argument the plan supplies (the raw
	// source referenced the secret store); it is false for every non-secret target,
	// which keeps its full byte snapshot.
	SecretRouted bool

	// ForcedEmptyOverSubstantial is set when --force pushed an empty/near-empty
	// repo source OVER a substantial existing live file (the data-loss transition
	// the guard normally refuses). The caller MUST surface a warning naming the
	// file and both sides of the hazard; ForcedPath carries the erased path.
	ForcedEmptyOverSubstantial bool
	ForcedPath                 string
}

// ConflictError is returned by apply when it refuses to overwrite. It carries
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

// EmptyOverSubstantialError is returned by apply when, WITHOUT --force, it would
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

// ApplyContentDeferred materializes one target onto the home destination with the
// repo side provided IN MEMORY: content stands in for the repo source (the caller
// already composed/derived it — e.g. the agents domain's rendered instruction
// sets, or the apply command's overlay-merged, secret-rendered effective bytes),
// so no repo file and no temp staging is involved — closing the secret-at-rest
// window a staged temp file would open. It runs the three-way decision table
// (ClassifyContent), the empty-over-substantial data-loss guard, and the
// Backuper-mediated atomic write, so any overwrite is backed up first.
//
// On a refused overwrite it returns a *ConflictError (Result.Action ==
// ActionConflict) so a caller can both detect the conflict via errors.As and
// render the Result.
//
// It does NOT persist last-applied. When it performs a write, the hash to record
// is returned in Result.PendingHash (and the deployed bytes in
// Result.PendingContent); the caller must persist them with CommitLastApplied
// AFTER committing the surrounding journal. This decouples last-applied from the
// file write so a crash/commit failure between the write and the journal commit
// can never leave last-applied ahead of a rolled-back file (Codex#3). The
// StateClean adoption case (an identical pre-existing file) advances last-applied
// via PendingHash too.
//
// freshPerm is the mode for a FIRST-EVER write of the destination (an
// existing regular destination's mode is preserved, exactly as for a
// dotfile); pass 0o644 for plain files or 0o755 for scripts that must stay
// runnable. last-applied is never persisted directly: the hash rides on
// Result.PendingHash for the caller's post-journal CommitLastApplied.
//
// secretRouted marks a target whose bytes were rendered from the secret store
// (they hold plaintext credentials). The apply core OWNS the two secret-at-rest
// invariants for such a target so no caller can forget either: it strips
// group/other access from the written file mode — even when preserving an
// EXISTING destination's mode (tightening is always owner-safe, so a
// pre-existing 0644 file adopted with secret content still lands 0600, never
// group-/world-readable) — and it stamps Result.SecretRouted so CommitLastApplied
// records only the content hash, never the plaintext bytes.
func ApplyContentDeferred(t Target, content []byte, freshPerm os.FileMode, store *Store, b Backuper, force, secretRouted bool) (Result, error) {
	return applyContent(t, content, freshPerm, store, b, force, false, secretRouted)
}

// applyContent is the ONE shared apply core: the three-way decision table over
// the in-memory desired content (ClassifyContent), the empty-over-substantial
// data-loss guard, and the Backuper-mediated write. ApplyContentDeferred is its
// public entry; every domain (dotfiles, termcfg, agents) funnels here, so the
// paths can never diverge.
func applyContent(t Target, content []byte, freshPerm os.FileMode, store *Store, b Backuper, force, dryRun, secretRouted bool) (Result, error) {
	st, err := ClassifyContent(t, content, store)
	if err != nil {
		return Result{}, err
	}
	// Stamp the secret-at-rest flag on EVERY result the core returns (not just a
	// write), so no caller has to remember to set it and CommitLastApplied's
	// hash-only path can never be bypassed by a forgotten assignment.
	res := Result{Target: t, State: st.State, SecretRouted: secretRouted}

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
			// Live already reproduces content, so content IS the deployed baseline:
			// carry it on the Result so the caller's post-journal CommitLastApplied
			// records it, establishing the baseline for an already-in-sync target
			// (the first-apply-after-upgrade bootstrap).
			res.PendingHash = st.RepoHash
			res.PendingContent = content
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
			if err := guardEmptyOverSubstantial(t, content, force, dryRun, &res); err != nil {
				return res, err
			}
			action := ActionUpdated
			if dryRun {
				res.Action = action
				return res, nil
			}
			if err := writeContent(t, content, st.RepoHash, freshPerm, secretRouted, b, &res); err != nil {
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
			if err := guardEmptyOverSubstantial(t, content, force, dryRun, &res); err != nil {
				return res, err
			}
		}
		if dryRun {
			res.Action = action
			return res, nil
		}
		if err := writeContent(t, content, st.RepoHash, freshPerm, secretRouted, b, &res); err != nil {
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

// CommitLastApplied persists the deferred last-applied hashes recorded by
// ApplyContentDeferred — and, alongside each, the last-deployed content baseline the
// same apply captured on Result.PendingContent. The caller runs it AFTER the
// surrounding journal commit, so a crash before commit leaves both the hash and
// the snapshot untouched (matching the rolled-back file) and a successful commit
// is followed by the matching write. Results with no PendingHash
// (noop-without-adoption/skipped/conflict) are ignored, so passing the full
// result slice is safe.
//
// A SecretRouted result records ONLY its hash (content=nil, the hash-only path):
// its deployed bytes carry substituted secret-store values, and the last-applied
// file is a non-secret bookkeeping file, so snapshotting them would write a
// plaintext secret at rest. The hash still advances the sync point and drives
// drift/baseline detection — only the byte snapshot is withheld.
func CommitLastApplied(results []Result, store *Store) error {
	for _, r := range results {
		if r.PendingHash == "" {
			continue
		}
		content := r.PendingContent
		if r.SecretRouted {
			content = nil
		}
		if err := store.setDeployed(r.Target.Name, r.PendingHash, content); err != nil {
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
			// Drop the marker plus ferry's generated include directive that follows.
			// The zsh/tmux directive is ONE line (`[ -f ~/… ] && source ~/…` /
			// `source-file -q ~/…`); the git directive is a TWO-line `[include]`
			// block (`[include]` then `\tpath = ~/…`). Consume whichever follows; a
			// non-matching next line is left in place (nothing user-authored removed).
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "[include]" &&
				i+2 < len(lines) && isFerryGitIncludePath(lines[i+2]) {
				i += 2
			} else if i+1 < len(lines) && isFerryOverlayInclude(lines[i+1]) {
				i++
			}
			continue
		}
		kept = append(kept, lines[i])
	}
	return []byte(strings.Join(kept, "\n"))
}

// isFerryOverlayInclude reports whether line is ferry's generated per-machine
// overlay include, in any include-style file format ferry emits:
//   - shell/zsh: the guarded `[ -f ~/<file> ] && source ~/<file>` shape;
//   - tmux:      the `source-file -q ~/<file>` shape.
//
// This is the SHAPE-keyed half of the two-strip contract: it recognises ferry's
// own generated line by its structure (never a bare user `source …`), and only
// ever runs on the line that FOLLOWS ferry's marker, so recognising both formats
// stays narrow to ferry's own output. Matching the precise structure (a guarded
// shell include, or `source-file -q ~/`, not a bare directive) keeps a
// user-authored line from being stripped.
func isFerryOverlayInclude(line string) bool {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "[ -f ~/") && strings.Contains(t, "] && source ~/") {
		return true
	}
	return strings.HasPrefix(t, "source-file -q ~/")
}

// isFerryGitIncludePath reports whether line is the `path = ~/…` line of ferry's
// generated git `[include]` block (the second line of the two-line git-INI
// directive). It is the git-INI branch of the shape-keyed strip: recognised by
// the `path` key with a `~/`-anchored value, never a bare user directive, and only
// ever tested on the line that follows ferry's marker + `[include]` header.
func isFerryGitIncludePath(line string) bool {
	t := strings.TrimSpace(line)
	eq := strings.IndexByte(t, '=')
	if eq < 0 {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(t[:eq]))
	val := strings.TrimSpace(t[eq+1:])
	return key == "path" && strings.HasPrefix(val, "~/")
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
// for a target whose live file exists and is about to be overwritten. It judges
// the desired content against the live file (t.Home): when the desired content
// is empty/near-empty (blank/whitespace/comments-only) AND the live file
// is substantial, the transition would erase real config. Without --force it
// returns an *EmptyOverSubstantialError (apply writes nothing, live file
// untouched). With --force it does NOT error but records the hazard on res so the
// caller warns before the overwrite proceeds. Any non-dangerous transition is a
// no-op. A live-read error is treated as "not substantial" (fail open to the
// normal path — the deploy's own write handles a genuinely unreadable target).
//
// Near-emptiness is judged on the USER's real managed content: ferry's injected
// per-machine overlay directive is stripped first (stripFerryOverlayDirective) so
// the zsh include path — which stages raw shared source PLUS ferry's generated
// `source ~/…local` line — cannot smuggle an empty shared source past the guard on
// the strength of ferry's OWN boilerplate. desired is the exact in-memory content
// the apply would write, so the guard judges precisely the bytes at stake.
func guardEmptyOverSubstantial(t Target, desired []byte, force, dryRun bool, res *Result) error {
	if !isNearEmpty(stripFerryOverlayDirective(desired)) {
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

// writeContent copies the desired content to the home destination through the
// Backuper and records the new last-applied hash on res for the caller's
// post-journal CommitLastApplied. The bytes written are the SAME bytes
// ClassifyContent hashed (repoHash), so last-applied can never record a lie. An
// existing regular destination keeps its mode and a fresh write takes freshPerm,
// except a secret-routed target always has group/other stripped (see below).
// last-applied is never persisted here: the hash rides on res.PendingHash and the
// deployed bytes on res.PendingContent, so a crash between the write and the
// journal commit can never leave last-applied ahead of a rolled-back file.
//
// secretRouted forces group/other access off the FINAL mode — whether fresh or
// inherited from an existing file — so a secret target's plaintext is never
// written group-/world-readable (0644 -> 0600, an executable 0755 -> 0700). This
// is the single enforcement point for the secret-at-rest perm invariant;
// tightening is always owner-safe, so clamping a preserved mode never breaks the
// owner's own access.
func writeContent(t Target, content []byte, repoHash string, freshPerm os.FileMode, secretRouted bool, b Backuper, res *Result) error {
	if b == nil {
		return errors.New("dotfile: nil Backuper")
	}
	perm := freshPerm
	if fi, err := os.Lstat(t.Home); err == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm() // preserve an existing destination's mode
	}
	if secretRouted {
		perm &^= 0o077 // strip group/other: a rendered secret is never group-/world-readable
	}

	if err := b.BackupAndWrite(t.Home, content, perm); err != nil {
		return err
	}
	// content is exactly the bytes just written, so it is the last-deployed
	// baseline for this target; carry it on res for the caller's CommitLastApplied.
	res.PendingHash = repoHash
	res.PendingContent = content
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
