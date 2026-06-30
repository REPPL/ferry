package evals

// File-location & layout ACs: config.toml location + contents, apply last-written
// tracking (observable), dotfiles copied (not symlinked), ferry.local.toml
// gitignored, and effective-scope overlay.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigTOML covers AC-loc-config-toml: after `ferry init`, config.toml exists
// at $HOME/.config/ferry/config.toml, is outside the repo clone, and RECORDS BOTH
// documented contents — (a) this machine's identity and (b) the path to its repo
// clone (recording only one is insufficient). The docs name ~/.config/ferry/ only;
// no XDG_CONFIG_HOME behaviour is promised, so none is asserted.
//
// TODO(contract): a fully non-interactive init path is not doc-pinned; init "asks
// before scaffolding". We feed empty stdin (declines prompts) and an existing repo
// so init's documented job — writing config.toml — can complete.
func TestConfigTOML_AC_loc_config_toml(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.WriteRepoFile(t, "ferry.toml", baseManifest)

	// Run init AGAINST the seeded repo (existing-clone path) so a path-recording
	// impl has a concrete repo to record. We pass s.Repo both as an argument and via
	// stdin; empty-following stdin declines any scaffold prompt. The setup flow must
	// COMPLETE successfully (exit 0) before we inspect the files it wrote — a CLI
	// that writes config.toml but exits non-zero must NOT pass.
	if _, errOut, code := s.FerryWithInput(s.Repo+"\n", "init", s.Repo); code != 0 {
		t.Fatalf("AC-loc-config-toml: `ferry init <repo>` setup exited %d (must complete successfully)\n%s", code, errOut)
	}

	cfg := s.ConfigTOMLPath()
	info, err := os.Stat(cfg)
	if err != nil {
		t.Fatalf("AC-loc-config-toml: expected config.toml at %s: %v", cfg, err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("AC-loc-config-toml: %s is not a regular file", cfg)
	}
	// config.toml must live at the documented ~/.config/ferry/config.toml.
	if cfg != s.HomePath(".config", "ferry", "config.toml") {
		t.Errorf("AC-loc-config-toml: config.toml is not at ~/.config/ferry/config.toml (got %s)", cfg)
	}

	// Must record BOTH a machine-identity field AND a repo-clone-path field.
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("AC-loc-config-toml: read config.toml: %v", err)
	}
	body := string(data)

	// (b) repo-clone-path: config.toml must record SOME repo path that ACTUALLY
	// EXISTS on disk and is a git repo. We do NOT hard-require the harness's
	// preallocated s.Repo literal — a valid fresh/clone impl may create+record a
	// DIFFERENT path. We extract the recorded path and verify it on disk.
	recorded := extractRepoPath(body)
	if recorded == "" {
		t.Fatalf("AC-loc-config-toml: config.toml records no repo-clone path\n%s", body)
	}
	if _, err := os.Stat(recorded); err != nil {
		t.Errorf("AC-loc-config-toml: recorded repo path %q does not exist on disk: %v\n%s", recorded, err, body)
	} else if out, gerr := exec.Command("git", "-C", recorded, "rev-parse", "--git-dir").CombinedOutput(); gerr != nil {
		t.Errorf("AC-loc-config-toml: recorded repo path %q is not a git repo: %v\n%s", recorded, gerr, out)
	}

	// config.toml must be OUTSIDE the repo it RECORDS — not merely outside s.Repo.
	// A bad impl that records $HOME (or ~/.config/ferry) as the "repo" and thereby
	// nests config.toml inside its own recorded repo must FAIL. We check both the
	// recorded path and the seeded s.Repo for completeness.
	for _, repoPath := range dedupePaths(recorded, s.Repo) {
		if rel, rerr := filepath.Rel(repoPath, cfg); rerr == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			t.Errorf("AC-loc-config-toml: config.toml (%s) lives INSIDE the repo it records (%s); it must be outside (\"not in repo\")", cfg, repoPath)
		}
	}

	// (a) machine-identity: a real identity KEY with a NON-EMPTY value (parsed from
	// TOML), SEPARATE from the repo-path field. The mere word "name" appearing
	// anywhere is NOT sufficient. TODO(contract): the exact key is not doc-pinned;
	// we accept the natural spellings.
	keys := parseTOMLKeyValues(body)
	identityKeys := []string{"hostname", "host", "machine", "machine_name", "machinename", "identity", "id", "name", "node", "nodename"}
	repoKeys := []string{"repo", "clone", "repo_path", "repopath", "path", "clone_path", "clonepath", "repository"}
	foundIdentity := ""
	for _, ik := range identityKeys {
		for k, v := range keys {
			if strings.EqualFold(k, ik) && strings.TrimSpace(v) != "" && !isRepoPathKey(k, repoKeys) {
				foundIdentity = k
			}
		}
	}
	if foundIdentity == "" {
		t.Errorf("AC-loc-config-toml: config.toml has no machine-identity KEY with a non-empty value (separate from the repo-path field); parsed keys=%v\n%s", keys, body)
	}
}

// isRepoPathKey reports whether key matches one of the repo-path key spellings —
// so the identity check doesn't count the repo path itself as the identity field.
func isRepoPathKey(key string, repoKeys []string) bool {
	for _, rk := range repoKeys {
		if strings.EqualFold(key, rk) {
			return true
		}
	}
	return false
}

// parseTOMLKeyValues is a minimal TOML key=value scanner. It returns the LAST
// (bare) key -> unquoted value for every `key = value` line, ignoring table
// headers and comments. Sufficient for config.toml's flat identity/repo fields
// (we only need key presence + non-empty value, not full TOML semantics).
func parseTOMLKeyValues(toml string) map[string]string {
	out := map[string]string{}
	for _, raw := range strings.Split(toml, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip a trailing inline comment outside quotes (best-effort).
		if !strings.HasPrefix(val, "\"") && !strings.HasPrefix(val, "'") {
			if h := strings.IndexByte(val, '#'); h >= 0 {
				val = strings.TrimSpace(val[:h])
			}
		}
		val = strings.Trim(val, "\"'")
		// A bare key like `hostname` (no dotted table prefix) is what we want.
		if i := strings.LastIndexByte(key, '.'); i >= 0 {
			key = key[i+1:]
		}
		out[key] = val
	}
	return out
}

// TestApplyTracksLastWritten covers AC-apply-tracks-last-written (RENAMED from
// AC-loc-state-dir). The docs say `apply` TRACKS what it last wrote but never name
// or locate a state dir, so this asserts ONLY observable consequences — NO
// assertion about where state lives or $HOME-locality (round-3 overreach fix):
//
//	(a) a second `apply` with no change is a no-op (idempotent), and
//	(b) a post-apply uncaptured edit is detected as a conflict on the next apply.
//
// Persisted last-applied tracking is proven by (a)+(b) holding across invocations.
func TestApplyTracksLastWritten_AC_apply_tracks_last_written(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-apply-tracks-last-written: first apply exited %d; stderr:\n%s", code, errOut)
	}
	target := s.HomePath(".zshrc")
	snap := s.SnapshotFile(t, target)
	if !snap.exists {
		t.Fatalf("AC-apply-tracks-last-written: expected managed target deployed by first apply")
	}

	// (a) second apply, no change -> no-op, exit 0, target untouched.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-apply-tracks-last-written: second apply exited %d; stderr:\n%s", code, errOut)
	}
	snap.AssertUnchanged(t)

	// (b) post-apply uncaptured edit -> conflict on next apply; edit must survive.
	edit := "# uncaptured local edit\n"
	if err := os.WriteFile(target, []byte(edit), 0o644); err != nil {
		t.Fatalf("write uncaptured edit: %v", err)
	}
	editSnap := s.SnapshotFile(t, target)
	s.WriteRepoFile(t, ".zshrc", "# repo moved on\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# repo moved on\n")

	out, errOut, code := s.Ferry("apply")
	combined := out + errOut
	// Match AC-conflict-refuse's standard: require an OBSERVABLE conflict REPORT
	// (conflict / uncaptured / capture guidance) — an opaque non-zero exit with no
	// conflict wording must FAIL, not pass. The tracking that detects the edit is
	// proven by the report, not merely by a non-zero exit.
	if !containsAnyFold(combined, "conflict", "uncaptured", "capture") {
		t.Errorf("AC-apply-tracks-last-written: uncaptured edit did not produce a conflict report (conflict/uncaptured/capture wording) — no persisted tracking? exit=%d\n%s", code, combined)
	}
	editSnap.AssertUnchanged(t) // the user's edit must not be clobbered
}

// TestDotfilesCopiedNotSymlinked covers AC-loc-dotfiles-copied-not-symlinked: the
// live dotfile is a COPY — NOT a symlink AND NOT a hardlink into the repo — and
// editing it never rewrites ANY repo copy of that dotfile. We rule out both
// aliasing mechanisms: symlink (lstat) and hardlink (os.SameFile inode/dev compare
// against every seeded repo source), then prove "editing a live file never
// silently rewrites the repo" against ALL seeded sources.
func TestDotfilesCopiedNotSymlinked_AC_loc_dotfiles_copied_not_symlinked(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	// Seed both candidate repo source layouts.
	repoSources := []string{
		s.WriteRepoFile(t, ".zshrc", "# repo source\n"),
		s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# repo source\n"),
	}

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-loc-dotfiles-copied-not-symlinked: apply exited %d; stderr:\n%s", code, errOut)
	}

	target := s.HomePath(".zshrc")
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("AC-loc-dotfiles-copied-not-symlinked: %s not deployed: %v", target, err)
	}
	// (a1) not a symlink.
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("AC-loc-dotfiles-copied-not-symlinked: %s is a SYMLINK; dotfiles must be copied", target)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("AC-loc-dotfiles-copied-not-symlinked: %s is not a regular file", target)
	}
	// (a2) not a HARDLINK into the repo: same-file (inode/dev) must be FALSE vs each
	// repo source. os.Stat follows nothing here (regular file) but distinguishes
	// hardlink aliasing by identity.
	liveInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat live: %v", err)
	}
	for _, src := range repoSources {
		srcInfo, err := os.Stat(src)
		if err != nil {
			continue
		}
		if os.SameFile(liveInfo, srcInfo) {
			rel, _ := filepath.Rel(s.Repo, src)
			t.Errorf("AC-loc-dotfiles-copied-not-symlinked: live %s is a HARDLINK to repo source %s (same inode) — must be an independent copy", target, rel)
		}
	}

	// (b) editing the live file must leave EVERY seeded repo copy byte-unchanged.
	before := map[string]string{}
	for _, src := range repoSources {
		before[src] = hashFile(t, src)
	}
	if err := os.WriteFile(target, []byte("# locally edited, must not touch repo\n"), 0o644); err != nil {
		t.Fatalf("edit live file: %v", err)
	}
	for _, src := range repoSources {
		if hashFile(t, src) != before[src] {
			rel, _ := filepath.Rel(s.Repo, src)
			t.Errorf("AC-loc-dotfiles-copied-not-symlinked: editing the live file rewrote repo source %s (symlink/hardlink aliasing?)", rel)
		}
	}
}

// TestFerryLocalTOMLGitignored covers AC-loc-ferry-local-toml-gitignored:
// ferry.local.toml lives in the repo but is gitignored (never tracked).
//
// We assert ferry establishes the gitignore: after init/apply against a git repo,
// a created ferry.local.toml is reported as ignored (!!) by git, not as a tracked
// change. We also seed the file ourselves to make the gitignore assertion robust
// even if ferry only writes the .gitignore entry.
func TestFerryLocalTOMLGitignored_AC_loc_ferry_local_toml_gitignored(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	s.WriteRepoFile(t, "ferry.toml", baseManifest)
	s.SeedSharedManifest(t, baseManifest)

	// Let ferry set up (it should ensure ferry.local.toml is gitignored).
	s.FerryWithInput("", "init")
	s.Ferry("apply")

	// Ensure the file physically exists so `git status --ignored` can classify it.
	localTOML := s.RepoPath("ferry.local.toml")
	if _, err := os.Stat(localTOML); err != nil {
		if err := os.WriteFile(localTOML, []byte("[manage]\niterm2 = false\n"), 0o644); err != nil {
			t.Fatalf("seed ferry.local.toml: %v", err)
		}
	}

	status := gitStatusIgnored(t, s.Repo)
	// It must be IGNORED (!! ferry.local.toml), never tracked/staged.
	if !statusLineMatches(status, "!!", "ferry.local.toml") {
		t.Errorf("AC-loc-ferry-local-toml-gitignored: ferry.local.toml is not git-ignored.\ngit status --ignored:\n%s", status)
	}
	if statusLineMatches(status, "A", "ferry.local.toml") || statusLineMatches(status, "??", "ferry.local.toml") {
		t.Errorf("AC-loc-ferry-local-toml-gitignored: ferry.local.toml is tracked/untracked-but-not-ignored (would be committed)")
	}
}

// TestFerryTOMLInRepo covers AC-loc-ferry-toml-in-repo: ferry.toml exists in the
// repo clone as the committed SHARED scope manifest, ferry reads its scope from
// there, and it is tracked by git (not gitignored). We do NOT assert capture
// rewrites ferry.toml itself (routing is AC-capture-interactive-route).
func TestFerryTOMLInRepo_AC_loc_ferry_toml_in_repo(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.InitGitRepo(t)
	// Seed + commit ferry.toml so it is a committed/tracked shared manifest.
	s.WriteRepoFile(t, "ferry.toml", baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	gitCommitAll(t, s.Repo, "shared manifest")
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	// (1) ferry.toml exists as a regular file in the repo.
	ferryTOML := s.RepoPath("ferry.toml")
	if info, err := os.Stat(ferryTOML); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("AC-loc-ferry-toml-in-repo: ferry.toml is not a regular file in the repo: %v", err)
	}
	// (2) It is tracked by git (committed), never ignored.
	if !gitIsTracked(t, s.Repo, "ferry.toml") {
		t.Errorf("AC-loc-ferry-toml-in-repo: ferry.toml is not tracked/committed by git")
	}
	status := gitStatusIgnored(t, s.Repo)
	if statusLineMatches(status, "!!", "ferry.toml") {
		t.Errorf("AC-loc-ferry-toml-in-repo: ferry.toml is gitignored (must be the committed shared manifest)")
	}
	// (3) ferry reads scope from <repo>/ferry.toml: apply deploys the in-scope
	// dotfile declared ONLY in that file. If ferry ignored it, .zshrc wouldn't deploy.
	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-loc-ferry-toml-in-repo: apply exited %d; stderr:\n%s", code, errOut)
	}
	if _, err := os.Stat(s.HomePath(".zshrc")); err != nil {
		t.Errorf("AC-loc-ferry-toml-in-repo: in-scope .zshrc not deployed — ferry did not read scope from <repo>/ferry.toml: %v", err)
	}
}

// TestLocalOverlayDir covers AC-loc-local-overlay-dir: a LOCAL-routed capture must
// LAND the captured content specifically under local/<domain>/ (the .zshrc change
// belongs to the zsh domain, so it lands under local/zsh/), and that per-domain
// overlay directory must be gitignored. We do NOT self-seed the overlay — we drive
// a real local-routed capture and prove the routing placed the file in the right
// per-domain dir.
func TestLocalOverlayDir_AC_loc_local_overlay_dir(t *testing.T) {
	t.Parallel()
	s := newCaptureSandbox(t) // git-backed, manifest seeded, .zshrc deployed

	// A local in-scope change to capture and route LOCAL.
	marker := "OVERLAY_DIR_MARKER"
	if err := os.WriteFile(s.HomePath(".zshrc"), []byte("# zshrc\n"+marker+"\n"), 0o644); err != nil {
		t.Fatalf("local edit: %v", err)
	}

	// Accept + route LOCAL.
	s.FerryWithInput("y\nl\ny\n", "capture")

	// The captured content must land at a path EXACTLY under local/<domain>/ — for
	// the zsh domain, under local/zsh/. Not just "somewhere under repo/local".
	landed := findFileContaining(t, s.RepoPath("local"), marker)
	if landed == "" {
		t.Fatalf("AC-loc-local-overlay-dir: local-routed capture did not land content under <repo>/local/")
	}
	rel, err := filepath.Rel(s.Repo, landed)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	// Path must be local/<domain>/... with a non-empty domain segment, and for a
	// .zshrc change the domain segment must be the zsh domain.
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 || parts[0] != "local" {
		t.Errorf("AC-loc-local-overlay-dir: captured local content at %q is not under local/<domain>/ (need local/<domain>/<file>)", rel)
	} else if !containsAnyFold(parts[1], "zsh") {
		t.Errorf("AC-loc-local-overlay-dir: .zshrc change landed under local/%q, expected the per-domain dir local/zsh/", parts[1])
	}

	// That per-domain overlay directory must be gitignored, not tracked.
	status := gitStatusIgnored(t, s.Repo)
	if !statusLineMatches(status, "!!", "local/") {
		t.Errorf("AC-loc-local-overlay-dir: repo local/ overlay dir is not gitignored.\ngit status --ignored:\n%s", status)
	}
	if gitIsTracked(t, s.Repo, rel) {
		t.Errorf("AC-loc-local-overlay-dir: captured overlay %q is tracked (must be gitignored)", rel)
	}
}

// findFileContaining returns the path of the first file under dir whose contents
// include needle, or "" if none.
func findFileContaining(t *testing.T, dir, needle string) string {
	t.Helper()
	found := ""
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(data), needle) {
			found = path
		}
		return nil
	})
	return found
}

// TestLocalMaterialised covers AC-loc-local-materialised: on apply, a .local
// overlay (repo local/zsh/zshrc.local) materialises to its real home path
// (~/.zshrc.local) with the overlay content, not staying only inside the repo.
func TestLocalMaterialised_AC_loc_local_materialised(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.SeedSharedManifest(t, baseManifest)
	s.WriteRepoFile(t, ".zshrc", "# shared\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# shared\n")
	overlayContent := "export MACHINE_LOCAL=1 # materialise me\n"
	s.WriteRepoFile(t, filepath.Join("local", "zsh", "zshrc.local"), overlayContent)

	if _, errOut, code := s.Ferry("apply"); code != 0 {
		t.Fatalf("AC-loc-local-materialised: apply exited %d; stderr:\n%s", code, errOut)
	}

	target := s.HomePath(".zshrc.local")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("AC-loc-local-materialised: overlay did not materialise to %s: %v", target, err)
	}
	if string(got) != overlayContent {
		t.Errorf("AC-loc-local-materialised: %s content = %q, want overlay %q", target, got, overlayContent)
	}
}

// dedupePaths returns the distinct, cleaned absolute forms of the given paths
// (resolving symlinks where possible) — used so an outside-repo check runs once
// per unique repo path even when two inputs name the same directory.
func dedupePaths(paths ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		c, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			c = filepath.Clean(p)
		}
		if r, e := filepath.EvalSymlinks(c); e == nil {
			c = r
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// TestEffectiveScopeOverlay covers AC-effective-scope-overlay: ferry.local.toml
// disabling a domain that ferry.toml enables makes the effective scope treat it as
// disabled. Shared sets iterm2 = true; local overrides iterm2 = false. We assert
// (reusing the terminal native-mechanism check) that NO iterm2 native-preference
// change is planned/applied — so an impl that ignores ferry.local.toml and applies
// iterm2 anyway FAILS. The dotfiles domain stays enabled and is deployed.
func TestEffectiveScopeOverlay_AC_effective_scope_overlay(t *testing.T) {
	t.Parallel()
	s := NewSandbox(t)
	s.WriteRepoFile(t, "ferry.toml", `[manage]
dotfiles = [".zshrc"]
iterm2 = true
`)
	s.WriteRepoFile(t, "ferry.local.toml", `[manage]
iterm2 = false
`)
	s.WriteRepoFile(t, ".zshrc", "# managed\n")
	s.WriteRepoFile(t, filepath.Join("dotfiles", ".zshrc"), "# managed\n")
	if err := os.WriteFile(s.ConfigTOMLPath(), []byte("repo = \""+s.Repo+"\"\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	// iterm2's terminal app/domain descriptor (reuse the terminal_test definition so
	// the diff check matches prose plans too, not just the pref ID).
	iterm := terminalApp{domain: "iterm2", prefID: "com.googlecode.iterm2", aliases: []string{"iterm2", "iterm 2", "iterm"}}

	// `defaults` recorder (catches a shell-out impl) + diff inspection + a plist
	// tripwire (catches a direct-CFPreferences/file-copy impl).
	defStubDir, defLog := makeDefaultsStub(t)
	pathOverride := "PATH=" + defStubDir + string(os.PathListSeparator) + os.Getenv("PATH")
	plistTarget := s.HomePath("Library", "Preferences", iterm.prefID+".plist")
	plistTW := s.SnapshotFile(t, plistTarget) // absent

	diffOut, diffErr, _ := s.FerryEnv([]string{pathOverride}, "diff")
	diffCombined := strings.ToLower(diffOut + diffErr)
	if _, errOut, code := s.FerryEnv([]string{pathOverride}, "apply"); code != 0 {
		t.Fatalf("AC-effective-scope-overlay: apply exited %d; stderr:\n%s", code, errOut)
	}

	// Enabled domain (dotfiles) still deployed.
	if _, err := os.Stat(s.HomePath(".zshrc")); err != nil {
		t.Errorf("AC-effective-scope-overlay: in-scope .zshrc not deployed: %v", err)
	}

	// De-scoped iterm2: NO native-preference change planned/applied (same strength
	// as the AC-terminal-config out-of-scope eval). macOS-native halves are
	// observable cross-platform via the stub + diff plan.
	if logReferences(defLog, iterm.prefID) || logReferences(defLog, iterm.domain) {
		t.Errorf("AC-effective-scope-overlay: `defaults` invoked for iterm2 despite ferry.local.toml disabling it (overlay ignored)")
	}
	plistTW.AssertUnchanged(t) // no iterm2 plist written
	// Reuse the strong diff helper: catches the pref ID AND a prose plan like
	// "would configure iterm2 preferences" without it. A de-scoped domain planned
	// in ANY form fails.
	if diffPlansTerminalChange(diffCombined, iterm) {
		t.Errorf("AC-effective-scope-overlay: diff plans an iterm2 preference change despite the local override (overlay ignored)\n%s", diffOut+diffErr)
	}
}

// --- local helpers for git status parsing ---

func gitStatusIgnored(t *testing.T, repo string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", "status", "--porcelain", "--ignored")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status --ignored: %v\n%s", err, out)
	}
	return string(out)
}

// gitIsTracked reports whether the given path is tracked (in the git index/HEAD).
func gitIsTracked(t *testing.T, repo, rel string) bool {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	cmd := exec.Command("git", "-C", repo, "ls-files", "--error-unmatch", rel)
	cmd.Env = gitEnv()
	return cmd.Run() == nil // exit 0 => tracked
}

// statusLineMatches reports whether any porcelain line has the given status code
// prefix and names the given file.
func statusLineMatches(status, code, file string) bool {
	for _, line := range strings.Split(status, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, code+" ") && strings.Contains(line, file) {
			return true
		}
	}
	return false
}
