package cmd

// Ship-review round-9 regression tests for the terminal preference-domain
// convergence defects:
//
//   - A3: a [s]hared capture behind an existing per-machine LOCAL overlay never
//     converged. The shared route wrote <domain>/<id>.plist, but every comparison
//     (status and capture alike) resolves through terminalRepoStatusSource, where
//     the overlay WINS — so the domain reported drift forever, capture re-offered
//     it forever, and apply kept importing the stale overlay.
//   - A4: a secret-routed capture writes a {{ferry.secret ...}} placeholder into
//     the repo, but the comparison was a RAW byte compare against the live export.
//     A placeholder never equals a plist, so the domain reported drift forever
//     while apply (which renders placeholders) considered it in sync.
//
// The end-to-end capture/status paths are darwin-only (`defaults export`), so the
// coverage here is at the extracted helper seams both platforms compile.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REPPL/ferry/internal/secret"
)

// syntheticExport is a stand-in for a `defaults export <id> -` blob: opaque XML
// with a trailing newline, exactly the shape the live comparison sees.
const syntheticExport = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\">\n<dict>\n\t<key>Foo</key>\n\t<string>bar</string>\n</dict>\n</plist>\n"

// A3: after a shared accept, the shared copy holds the new bytes AND the local
// overlay that would otherwise keep winning is gone — so the very next
// comparison resolves to the shared copy and the domain converges.
func TestAcceptTerminalShared_SupersedesLocalOverlay(t *testing.T) {
	repo := t.TempDir()
	const prefID = "com.apple.Terminal"
	overlay := terminalLocalDest(repo, "terminal", prefID)
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("stale overlay\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := acceptTerminalShared(&out, repo, "terminal", prefID, []byte(syntheticExport)); err != nil {
		t.Fatalf("acceptTerminalShared: %v", err)
	}

	shared := terminalRepoDest(repo, "terminal", prefID)
	got, err := os.ReadFile(shared)
	if err != nil {
		t.Fatalf("shared copy not written at %s: %v", shared, err)
	}
	if string(got) != syntheticExport {
		t.Errorf("shared copy = %q, want the accepted export", got)
	}
	// The overlay must be GONE: while it exists it wins the comparison, so the
	// domain would report drift forever and apply would re-import the stale bytes.
	if _, serr := os.Lstat(overlay); !os.IsNotExist(serr) {
		t.Errorf("the local overlay %s survived the shared accept (it would shadow the shared copy forever): stat err = %v", overlay, serr)
	}
	// And the resolution both status and capture use now points at the shared copy.
	if src := terminalRepoStatusSource(repo, "terminal", prefID); src != shared {
		t.Errorf("after the shared accept the compare source = %q, want the shared copy %q", src, shared)
	}
	// The removal is reported, naming the path and why.
	msg := out.String()
	if !strings.Contains(msg, "captured -> shared") {
		t.Errorf("no shared-capture line in the output:\n%s", msg)
	}
	if !strings.Contains(msg, relTo(repo, overlay)) || !strings.Contains(msg, "superseded") {
		t.Errorf("the removed overlay is not reported by path and reason:\n%s", msg)
	}
}

// A3: an overlay ferry refuses to read (a symlink out of the repo) never wins the
// comparison, so nothing is removed — and the refusal is not turned into an error.
func TestAcceptTerminalShared_LeavesSymlinkedOverlay(t *testing.T) {
	repo := t.TempDir()
	const prefID = "com.googlecode.iterm2"
	outside := filepath.Join(t.TempDir(), "elsewhere.plist")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := terminalLocalDest(repo, "iterm2", prefID)
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, overlay); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := acceptTerminalShared(&out, repo, "iterm2", prefID, []byte(syntheticExport)); err != nil {
		t.Fatalf("acceptTerminalShared: %v", err)
	}
	if _, serr := os.Lstat(overlay); serr != nil {
		t.Errorf("a refused (symlinked) overlay was removed; it never wins the comparison: %v", serr)
	}
	if _, serr := os.Lstat(outside); serr != nil {
		t.Errorf("the symlink target outside the repo was touched: %v", serr)
	}
}

// A4: a placeholder-bearing repo blob whose secret IS present renders to the live
// export, so the comparison is equal and the domain converges.
func TestTerminalRepoCompareBytes_RendersPlaceholders(t *testing.T) {
	store := secret.OpenAt(t.TempDir())
	const ref = "com.apple.Terminal.captured"
	if err := store.Put(ref, syntheticExport); err != nil {
		t.Fatal(err)
	}
	repoBytes := terminalPlaceholderBlob(ref)

	got := terminalRepoCompareBytes(store, repoBytes)
	if string(got) != syntheticExport {
		t.Errorf("rendered repo bytes = %q, want the live export %q (the domain reports drift forever otherwise)", got, syntheticExport)
	}
}

// A4: a MISSING referenced secret falls back to the raw compare — conservative,
// no behaviour change and no error on the read-only path.
func TestTerminalRepoCompareBytes_MissingRefFallsBackToRaw(t *testing.T) {
	store := secret.OpenAt(t.TempDir())
	repoBytes := terminalPlaceholderBlob("com.apple.Terminal.captured")

	got := terminalRepoCompareBytes(store, repoBytes)
	if string(got) != string(repoBytes) {
		t.Errorf("missing ref: compare bytes = %q, want the raw repo bytes %q", got, repoBytes)
	}
	if string(got) == syntheticExport {
		t.Error("a missing secret rendered anyway")
	}
}

// A4: blobs with no placeholders, and a nil store, are passed through untouched —
// the existing comparison semantics are unchanged for every non-secret domain.
func TestTerminalRepoCompareBytes_PassThrough(t *testing.T) {
	store := secret.OpenAt(t.TempDir())
	if got := terminalRepoCompareBytes(store, []byte(syntheticExport)); string(got) != syntheticExport {
		t.Errorf("plain repo bytes were altered: %q", got)
	}
	if got := terminalRepoCompareBytes(nil, []byte(syntheticExport)); string(got) != syntheticExport {
		t.Errorf("nil store: repo bytes were altered: %q", got)
	}
	if got := terminalRepoCompareBytes(store, nil); len(got) != 0 {
		t.Errorf("absent repo copy rendered to %q, want empty", got)
	}
}

// A4: the placeholder blob a secret-routed capture writes must render back to the
// export BYTE-FOR-BYTE — an extra trailing newline is enough to keep the domain
// reporting drift forever on the unfiltered (Apple Terminal) side.
func TestTerminalPlaceholderBlob_RoundTripsExport(t *testing.T) {
	store := secret.OpenAt(t.TempDir())
	const ref = "com.apple.Terminal.captured"
	for _, live := range []string{syntheticExport, strings.TrimSuffix(syntheticExport, "\n")} {
		if err := store.Put(ref, live); err != nil {
			t.Fatal(err)
		}
		rendered, missing, skip, err := renderSecrets(store, terminalPlaceholderBlob(ref))
		if err != nil || skip {
			t.Fatalf("render: skip=%v missing=%v err=%v", skip, missing, err)
		}
		if string(rendered) != live {
			t.Errorf("placeholder blob rendered to %q, want the exported blob %q", rendered, live)
		}
	}
}
