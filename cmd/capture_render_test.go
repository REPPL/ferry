package cmd

// Ship-review round-1 regression tests for the capture reverse-render fixes:
// masked gated hunk output (Codex C1), positional span patching (Codex M1),
// unclean-span read-only rejection (Claude M1), and positional-ref collision
// suffixing (Claude M2).

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/secret"
)

// Codex CRITICAL: a gate-tripping NEW secret in a placeholder-aware capture
// must NEVER print its raw value — the hunk output shown BEFORE the consent
// prompt is masked with the would-be placeholder.
func TestCaptureOne_GatedHunkOutputMasked(t *testing.T) {
	store := secret.OpenAt(t.TempDir())
	src := "# curated\nalias gs='git status'\n"
	live := src + "export NEW_TOKEN=" + synthWizardSecret + "\n"

	var out bytes.Buffer
	wrote, err := captureOne(captureCtx{
		out:              &out,
		in:               bufio.NewReader(strings.NewReader("y\nr\n")), // accept hunk, REJECT consent
		name:             ".zshrc",
		repoBytes:        []byte(src),
		liveBytes:        []byte(live),
		secretStore:      store,
		placeholderAware: true,
	})
	if err != nil {
		t.Fatalf("captureOne: %v", err)
	}
	if wrote {
		t.Error("rejected consent still wrote")
	}
	if strings.Contains(out.String(), synthWizardSecret) {
		t.Errorf("gated capture output printed the raw secret value (scrollback leak):\n%s", out.String())
	}
	// The hunk was still shown — masked with a placeholder-shaped stand-in.
	if !strings.Contains(out.String(), "hunk 1/1") || !strings.Contains(out.String(), "{{ferry.secret") {
		t.Errorf("masked hunk output missing (want the hunk with a placeholder mask):\n%s", out.String())
	}
}

// captureSecretMasks masks per LINE (hunks render line by line), so a
// multi-line PEM value masks even though renderHunk prefixes every line.
func TestCaptureSecretMasks_MultiLinePEM(t *testing.T) {
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nc3ludGhVbml0Qm9keVpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\n-----END OPENSSH PRIVATE KEY-----"
	text := "# key\n" + pem + "\n"
	masks := captureSecretMasks(text, "zshrc")
	h := hunk{newLines: strings.Split(pem, "\n")}
	rendered := maskCaptureText(renderHunk(h), masks)
	if strings.Contains(rendered, "BEGIN OPENSSH") {
		t.Errorf("masked hunk still carries key material:\n%s", rendered)
	}
	if !strings.Contains(rendered, "{{ferry.secret") {
		t.Errorf("mask did not substitute the placeholder:\n%s", rendered)
	}
}

// Codex MAJOR: patching is positional by line range — an EARLIER byte-equal
// occurrence of the span content is never patched in the flagged span's stead.
func TestPatchSpanLines_Positional(t *testing.T) {
	dup := "export NEW_TOKEN=" + synthWizardSecret
	text := dup + "\n# separator\n" + dup + "\ntail\n"
	sp := secret.SecretSpan{StartLine: 3, EndLine: 3, Value: dup}
	got := patchSpanLines(text, sp, `{{ferry.secret "zshrc.secret_3"}}`)
	want := dup + "\n# separator\n" + `{{ferry.secret "zshrc.secret_3"}}` + "\ntail\n"
	if got != want {
		t.Errorf("positional patch wrong.\ngot:  %q\nwant: %q", got, want)
	}
	// A multi-line range collapses to ONE placeholder line.
	multi := "a\nb\nc\nd\n"
	got = patchSpanLines(multi, secret.SecretSpan{StartLine: 2, EndLine: 3, Value: "b\nc"}, "PH")
	if got != "a\nPH\nd\n" {
		t.Errorf("multi-line positional patch = %q", got)
	}
	// Out-of-range spans leave the text untouched.
	if patchSpanLines("a\n", secret.SecretSpan{StartLine: 5, EndLine: 6}, "PH") != "a\n" {
		t.Error("out-of-range span mutated the text")
	}
}

// Claude MAJOR: an UNTERMINATED private-key block above an existing
// placeholder line cannot be isolated cleanly — consent takes the read-only
// path: NO store write, placeholder preserved, nothing returned for writing.
func TestConsentSpanStoreRoute_UncleanSpanReadOnly(t *testing.T) {
	storeDir := t.TempDir()
	store := secret.OpenAt(storeDir)
	captured := "# curated\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"c3ludGhVbml0Qm9keVpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\n" +
		`{{ferry.secret "zsh.github_token"}}` + "\n" +
		"alias keep='me'\n"

	var out bytes.Buffer
	_, _, ok, err := consentSpanStoreRoute(bufio.NewReader(strings.NewReader("x\n")), &out, store, ".zshrc", "zshrc", captured, secret.FlaggedSpans)
	if err != nil {
		t.Fatalf("consentSpanStoreRoute: %v", err)
	}
	if ok {
		t.Fatal("an unclean (unterminated-PEM) span was accepted for storing")
	}
	if entries, _ := os.ReadDir(storeDir); len(entries) != 0 {
		t.Errorf("a store write occurred for an unclean span: %v", entries)
	}
	if !strings.Contains(strings.ToLower(out.String()), "read-only") {
		t.Errorf("no read-only report in the output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "c3ludGhVbml0Qm9keV") {
		t.Errorf("output printed span material:\n%s", out.String())
	}
}

// The consent path still works for a clean single-line span — and never
// stores a value containing a placeholder.
func TestConsentSpanStoreRoute_CleanSpanStores(t *testing.T) {
	storeDir := t.TempDir()
	store := secret.OpenAt(storeDir)
	line := "export NEW_TOKEN=" + synthWizardSecret
	captured := "# curated\n" + `{{ferry.secret "zsh.github_token"}}` + "\n" + line + "\n"

	var out bytes.Buffer
	patched, _, ok, err := consentSpanStoreRoute(bufio.NewReader(strings.NewReader("x\n")), &out, store, ".zshrc", "zshrc", captured, secret.FlaggedSpans)
	if err != nil || !ok {
		t.Fatalf("consent failed: ok=%v err=%v\n%s", ok, err, out.String())
	}
	if strings.Contains(patched, synthWizardSecret) {
		t.Errorf("patched content still carries the value:\n%s", patched)
	}
	if !strings.Contains(patched, `{{ferry.secret "zsh.github_token"}}`) {
		t.Errorf("the EXISTING placeholder was disturbed:\n%s", patched)
	}
	v, found, gerr := store.Get("zshrc.secret_3")
	if gerr != nil || !found || v != line {
		t.Errorf("stored span = (%q, %v, %v), want the exact line under zshrc.secret_3", v, found, gerr)
	}
}

// Claude MAJOR: positional refs collide across captures (line numbers shift);
// a ref holding a DIFFERENT value is never silently overwritten — the new
// span suffixes _2, _3... A byte-identical value reuses its ref.
func TestFreeSecretRef_CollisionSuffixing(t *testing.T) {
	store := secret.OpenAt(t.TempDir())
	if err := store.Put("zshrc.secret_5", "old-value"); err != nil {
		t.Fatal(err)
	}
	ref, err := freeSecretRef(store, "zshrc", 5, "new-value")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "zshrc.secret_5_2" {
		t.Errorf("collision ref = %q, want zshrc.secret_5_2", ref)
	}
	if err := store.Put("zshrc.secret_5_2", "third-value"); err != nil {
		t.Fatal(err)
	}
	ref, err = freeSecretRef(store, "zshrc", 5, "new-value")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "zshrc.secret_5_3" {
		t.Errorf("double-collision ref = %q, want zshrc.secret_5_3", ref)
	}
	// Same value: reuse the base ref (idempotent overwrite).
	ref, err = freeSecretRef(store, "zshrc", 5, "old-value")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "zshrc.secret_5" {
		t.Errorf("same-value ref = %q, want the base ref", ref)
	}
	// A previously stored secret survives a later differing-value capture.
	if v, ok, _ := store.Get("zshrc.secret_5"); !ok || v != "old-value" {
		t.Errorf("the original stored value was disturbed: (%q, %v)", v, ok)
	}
}

// incompletePEMSpan classifies BEGIN-without-END as unclean; a terminated
// block and a plain token line are clean.
func TestIncompletePEMSpan(t *testing.T) {
	if !incompletePEMSpan("-----BEGIN OPENSSH PRIVATE KEY-----\nbody") {
		t.Error("unterminated PEM classified clean")
	}
	if incompletePEMSpan("-----BEGIN OPENSSH PRIVATE KEY-----\nbody\n-----END OPENSSH PRIVATE KEY-----") {
		t.Error("terminated PEM classified unclean")
	}
	if incompletePEMSpan("export NEW_TOKEN=" + synthWizardSecret) {
		t.Error("plain token line classified as PEM")
	}
}

// Ship-review round-2 #3 (Codex): store [x] then local [l] — after the span
// consent patches the captured composition, the zsh local-route delta must be
// derived from the PATCHED content: the sidecar gets the placeholder, the
// store entry is referenced, and the local capture is NOT refused (no
// orphaned store entry).
func TestCaptureOne_StoreThenLocalCarriesPlaceholder(t *testing.T) {
	storeDir := t.TempDir()
	store := secret.OpenAt(storeDir)
	repoPath := t.TempDir()
	src := "# curated\nalias gs='git status'\n"
	newLine := "export NEW_TOKEN=" + synthWizardSecret
	live := src + "\n# machine token\n" + newLine + "\n"

	var out bytes.Buffer
	wrote, err := captureOne(captureCtx{
		out:              &out,
		in:               bufio.NewReader(strings.NewReader("y\nx\nl\n")), // accept hunk, store consent, LOCAL route
		repoPath:         repoPath,
		name:             ".zshrc",
		repoBytes:        []byte(src),
		liveBytes:        []byte(live),
		secretStore:      store,
		placeholderAware: true,
	})
	if err != nil {
		t.Fatalf("captureOne: %v", err)
	}
	if !wrote {
		t.Fatalf("store-then-local capture was refused (orphaned store entry?)\n%s", out.String())
	}
	// The store holds the new span...
	found := false
	entries, _ := os.ReadDir(storeDir)
	for _, e := range entries {
		data, _ := os.ReadFile(storeDir + "/" + e.Name())
		if strings.Contains(string(data), synthWizardSecret) {
			found = true
		}
	}
	if !found {
		t.Error("the consented span was not stored")
	}
	// ...and the LOCAL overlay carries the PLACEHOLDER, never the value.
	overlay, err := os.ReadFile(localOverlayPath(repoPath, "zshrc"))
	if err != nil {
		t.Fatalf("local overlay not written: %v\n%s", err, out.String())
	}
	if strings.Contains(string(overlay), synthWizardSecret) {
		t.Errorf("the raw value reached the local overlay:\n%s", overlay)
	}
	if !strings.Contains(string(overlay), "{{ferry.secret") {
		t.Errorf("the local overlay lacks the placeholder (delta not derived from the patched composition):\n%s", overlay)
	}
	if !strings.Contains(string(overlay), "# machine token") {
		t.Errorf("the non-secret delta line was lost:\n%s", overlay)
	}
	// And the raw value never printed.
	if strings.Contains(out.String(), synthWizardSecret) {
		t.Errorf("capture output printed the raw value:\n%s", out.String())
	}
}

// Ship-review round-3 #1 (Claude, CRITICAL): the NON-zsh whole-file LOCAL
// route must write the SPAN-PATCHED composition — never cc.liveBytes, whose
// raw new secret the user just stored. y/x/l on a placeholder-bearing
// non-zsh source: the local overlay carries the placeholder, never the value,
// and the printed message names exactly the file that was written.
func TestCaptureOne_WholeFileLocalWritesPatchedComposition(t *testing.T) {
	storeDir := t.TempDir()
	store := secret.OpenAt(storeDir)
	repoPath := t.TempDir()
	// A NON-zsh, non-include whole-file dotfile (.npmrc: whole-file local overlay,
	// no include point, generic line-grained secret extractor).
	src := "registry=https://registry.npmjs.org/\n# stored key\n" + `{{ferry.secret "npmrc.secret_9"}}` + "\n"
	if err := store.Put("npmrc.secret_9", "synthetic-old-value"); err != nil {
		t.Fatal(err)
	}
	newLine := "export NEW_TOKEN=" + synthWizardSecret
	live := src + newLine + "\n"

	var out bytes.Buffer
	wrote, err := captureOne(captureCtx{
		out:              &out,
		in:               bufio.NewReader(strings.NewReader("y\nx\nl\n")), // accept, store consent, LOCAL route
		repoPath:         repoPath,
		name:             ".npmrc",
		repoBytes:        []byte(src),
		liveBytes:        []byte(live),
		secretStore:      store,
		placeholderAware: true,
	})
	if err != nil {
		t.Fatalf("captureOne: %v", err)
	}
	if !wrote {
		t.Fatalf("store-then-local whole-file capture refused\n%s", out.String())
	}
	dest := filepath.Join(repoPath, "local", "npmrc", "npmrc")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("whole-file local overlay not written at %s: %v\n%s", dest, err, out.String())
	}
	if strings.Contains(string(data), synthWizardSecret) {
		t.Errorf("CRITICAL: the raw secret value reached the repo worktree at %s:\n%s", dest, data)
	}
	if !strings.Contains(string(data), "{{ferry.secret") || !strings.Contains(string(data), "registry=https://registry.npmjs.org/") {
		t.Errorf("local overlay is not the patched composition:\n%s", data)
	}
	// The printed message names exactly what was written.
	if !strings.Contains(out.String(), "captured -> local (local/npmrc/npmrc") {
		t.Errorf("capture message does not match the written file:\n%s", out.String())
	}
	// The consented value is in the store, and never printed.
	found := false
	entries, _ := os.ReadDir(storeDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(storeDir, e.Name()))
		if strings.Contains(string(data), synthWizardSecret) {
			found = true
		}
	}
	if !found {
		t.Error("the consented span was not stored")
	}
	if strings.Contains(out.String(), synthWizardSecret) {
		t.Errorf("capture output printed the raw value:\n%s", out.String())
	}
}

// Ship-review round-3 #6: [x] consent followed by a route REJECTION keeps the
// store entry (safe direction) and tells the user the capture was NOT written
// and where the stored value remains — never an overstating message.
func TestCaptureOne_ConsentThenRejectNotesOrphanedRef(t *testing.T) {
	storeDir := t.TempDir()
	store := secret.OpenAt(storeDir)
	src := "# curated\n" + `{{ferry.secret "zsh.github_token"}}` + "\n"
	if err := store.Put("zsh.github_token", "synthetic-old-value"); err != nil {
		t.Fatal(err)
	}
	live := src + "export NEW_TOKEN=" + synthWizardSecret + "\n"

	var out bytes.Buffer
	wrote, err := captureOne(captureCtx{
		out:              &out,
		in:               bufio.NewReader(strings.NewReader("y\nx\nr\n")), // accept, store consent, then REJECT the route
		repoPath:         t.TempDir(),
		name:             ".zshrc",
		repoBytes:        []byte(src),
		liveBytes:        []byte(live),
		secretStore:      store,
		placeholderAware: true,
	})
	if err != nil {
		t.Fatalf("captureOne: %v", err)
	}
	if wrote {
		t.Fatal("rejected route still wrote")
	}
	// The stored entry remains (NOT deleted) and the refusal names it.
	if v, ok, _ := store.Get("zshrc.secret_3"); !ok || !strings.Contains(v, synthWizardSecret) {
		t.Errorf("the consented store entry vanished (must be kept; retry reuses it): (%q, %v)", v, ok)
	}
	if !strings.Contains(out.String(), "capture was not written") || !strings.Contains(out.String(), "zshrc.secret_3") {
		t.Errorf("no honest not-written notice naming the stored ref:\n%s", out.String())
	}
	if strings.Contains(out.String(), synthWizardSecret) {
		t.Errorf("output printed the raw value:\n%s", out.String())
	}
}

// Ship-review round-4 #3: last-applied for rendered (secret-bearing) effective
// bytes is hashed IN MEMORY — no temp file of rendered secrets ever lands in
// $TMPDIR — and records exactly the hash the old staged path recorded.
func TestRecordEffectiveLastAppliedNoTempFile(t *testing.T) {
	tmpWatch := t.TempDir()
	t.Setenv("TMPDIR", tmpWatch)

	effective := []byte("# rendered\nexport GITHUB_TOKEN=" + synthWizardSecret + "\n")
	home := t.TempDir()
	livePath := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(livePath, effective, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := dotfile.Target{Name: "zshrc", Home: livePath}
	if err := recordEffectiveLastApplied(target, store, effective); err != nil {
		t.Fatalf("recordEffectiveLastApplied: %v", err)
	}
	// NO temp file was created (the rendered secret never touched $TMPDIR).
	if entries, _ := os.ReadDir(tmpWatch); len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("rendered bytes were staged to $TMPDIR: %v", names)
	}
	// The recorded hash equals what the file-staged UpdateLastApplied records
	// for the same bytes (parity with the pre-change behavior).
	gotHash, ok := store.LastApplied("zshrc")
	if !ok {
		t.Fatal("last-applied did not advance on a full reproduction")
	}
	refStore, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(t.TempDir(), "staged")
	if err := os.WriteFile(repoPath, effective, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dotfile.UpdateLastApplied(dotfile.Target{Name: "zshrc", Home: livePath, Repo: repoPath}, refStore); err != nil {
		t.Fatal(err)
	}
	wantHash, ok := refStore.LastApplied("zshrc")
	if !ok || gotHash != wantHash {
		t.Errorf("in-memory hash %q != staged-path hash %q (ok=%v)", gotHash, wantHash, ok)
	}
	// A live file that does NOT reproduce the effective bytes leaves the
	// record put (partial-capture contract unchanged).
	store2, err := dotfile.OpenStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := recordEffectiveLastApplied(target, store2, []byte("different\n")); err != nil {
		t.Fatal(err)
	}
	if _, ok := store2.LastApplied("zshrc"); ok {
		t.Error("last-applied advanced despite live != effective")
	}
}

// Ship-review round-4 #4: a MID-LOOP store.Put failure names the refs already
// stored (values never printed), consistent with notifyStoredNotWritten.
func TestConsentSpanStoreRoute_PartialPutFailureNamesStoredRefs(t *testing.T) {
	storeDir := t.TempDir()
	store := secret.OpenAt(storeDir)

	// Two distinct new secrets on separate lines; spans store in DESCENDING
	// line order, so the SECOND Put call handles the earlier line.
	other := "FERRYUNITz8X4f0S2u9Z3v1J7p5Q6bTeLkWy"
	captured := "# curated\n" +
		"export NEW_TOKEN=" + synthWizardSecret + "\n" +
		"alias keep='me'\n" +
		"export OTHER_TOKEN=" + other + "\n"

	orig := storePut
	calls := 0
	storePut = func(s *secret.Store, ref, value string) error {
		calls++
		if calls == 2 {
			return fmt.Errorf("injected store failure")
		}
		return s.Put(ref, value)
	}
	t.Cleanup(func() { storePut = orig })

	var out bytes.Buffer
	_, _, ok, err := consentSpanStoreRoute(bufio.NewReader(strings.NewReader("x\n")), &out, store, ".zshrc", "zshrc", captured, secret.FlaggedSpans)
	if ok {
		t.Fatal("partial Put failure reported success")
	}
	if err == nil {
		t.Fatal("partial Put failure returned no error")
	}
	// The error names the ref that DID land (line 4 stores first, descending).
	if !strings.Contains(err.Error(), "zshrc.secret_4") || !strings.Contains(err.Error(), "already stored") {
		t.Errorf("error does not name the already-stored ref: %v", err)
	}
	// No value in the error or output.
	combined := err.Error() + out.String()
	if strings.Contains(combined, synthWizardSecret) || strings.Contains(combined, other) {
		t.Errorf("a raw value leaked into the error/output:\n%s", combined)
	}
	// The first (successful) Put is really in the store — kept, not rolled back.
	if v, found, _ := store.Get("zshrc.secret_4"); !found || !strings.Contains(v, other) {
		t.Errorf("the already-stored span vanished: (%q, %v)", v, found)
	}
}

// TestCaptureOne_PromptLabelsVisible pins the fix for the invisible-prompt bug
// (first field report, 2026-07-02; present since v0.2.x): prompt() dropped its
// label, so an interactive capture showed the hunk and then silently blocked on
// stdin with NO question visible. The evals script stdin and assert end-state,
// so only a label-presence assertion can catch this class.
func TestCaptureOne_PromptLabelsVisible(t *testing.T) {
	store := secret.OpenAt(t.TempDir())
	src := "# curated\nalias gs='git status'\n"
	live := src + "alias gd='git diff'\n"

	var out bytes.Buffer
	if _, err := captureOne(captureCtx{
		out:         &out,
		in:          bufio.NewReader(strings.NewReader("y\nr\n")), // accept hunk, reject route
		name:        ".zshrc",
		repoBytes:   []byte(src),
		liveBytes:   []byte(live),
		secretStore: store,
	}); err != nil {
		t.Fatalf("captureOne: %v", err)
	}
	for _, label := range []string{
		"accept this hunk? [y]es / [n]o (default n): ",
		"route this change? [s]hared / [l]ocal / [r]eject (default r): ",
	} {
		if !strings.Contains(out.String(), label) {
			t.Errorf("prompt label %q not printed — the user would face a blank, waiting terminal:\n%s", label, out.String())
		}
	}
}

// TestApplyHunks_PreservesTrailingNewlineShape is the F02 regression gate: an
// all-accepted composition must equal the live file byte-for-byte, including its
// trailing-newline shape. Otherwise a live file with no final newline composes to
// live+"\n", which the shared route writes and then wedges the target in a
// permanent, unclearable StateConflict.
func TestApplyHunks_PreservesTrailingNewlineShape(t *testing.T) {
	repo := "a\nb\nc\n"

	// Live WITHOUT a trailing newline: composition must not append one.
	live := "a\nX\nc"
	hunks := diffHunks(repo, live)
	accepted := make([]bool, len(hunks))
	for i := range accepted {
		accepted[i] = true
	}
	if got := applyHunks(repo, hunks, accepted, endsWithNewline([]byte(live))); got != live {
		t.Errorf("no-trailing-newline: composition = %q, want live %q", got, live)
	}

	// Live WITH a trailing newline: composition must keep it.
	live2 := "a\nX\nc\n"
	hunks2 := diffHunks(repo, live2)
	accepted2 := make([]bool, len(hunks2))
	for i := range accepted2 {
		accepted2[i] = true
	}
	if got := applyHunks(repo, hunks2, accepted2, endsWithNewline([]byte(live2))); got != live2 {
		t.Errorf("trailing-newline: composition = %q, want live %q", got, live2)
	}
}
