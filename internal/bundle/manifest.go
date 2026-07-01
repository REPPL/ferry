package bundle

import (
	"fmt"
	"path"
	"strings"
)

// formatVersion is the current bundle format this ferry writes and is the newest
// it accepts on import. A bundle whose manifest declares a HIGHER version is
// refused with an upgrade message (a newer ferry wrote it). Bump only on an
// incompatible manifest/zip layout change.
const formatVersion = 1

// manifestMember is the in-zip name of the manifest, the sole non-payload member.
// It is excluded from the manifest↔payload equality comparison.
const manifestMember = "ferry-bundle.json"

// bundleEntry describes one payload file: its canonical forward-slash relative
// path, uncompressed size in bytes, and lowercase hex SHA256 of its content. JSON
// field names are the coordination contract with evals/bundle_test.go — do not
// rename without updating that file.
type bundleEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// bundleManifest is the parsed ferry-bundle.json. FormatVersion and FerryVersion
// are metadata every reader checks; IncludeLocal records whether the writer added
// the local layer (import re-gates it behind its own --include-local); Entries is
// the payload description. JSON field names are the eval contract.
type bundleManifest struct {
	FormatVersion int           `json:"format_version"`
	FerryVersion  string        `json:"ferry_version"`
	IncludeLocal  bool          `json:"include_local"`
	Entries       []bundleEntry `json:"entries"`
}

// canonicalRel validates and canonicalises a payload zip-member name to a safe
// forward-slash relative path, returning an error for anything that must never be
// written: an empty name, a backslash (a Windows-path smuggling a component), an
// absolute path, a `..` traversal segment (before OR after cleaning), a path that
// does not stay within the target when cleaned, or any `.ssh` / VCS-control
// component (case-folded, anywhere in the path). The returned path is path.Clean'd
// with forward slashes; the caller
// joins it onto the target dir. This is the lexical core shared by Validate
// (reject bad manifest entries) and Extract (zip-slip confinement).
func canonicalRel(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty entry path")
	}
	// A backslash never appears in a legitimate forward-slash zip member; treating
	// it as a separator prevents `a\..\..\etc` style smuggling on any OS.
	if strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("entry path %q contains a backslash", name)
	}
	// Reject absolute BEFORE cleaning: path.IsAbs on the raw name, plus a Windows
	// drive/volume guard (a leading `C:` looks relative to path but is absolute).
	if path.IsAbs(name) || hasDriveLetter(name) {
		return "", fmt.Errorf("entry path %q is absolute", name)
	}
	// Any `..` segment in the RAW path is illegitimate — no legitimate payload path
	// contains one, and rejecting pre-clean avoids relying on Clean's collapsing.
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", fmt.Errorf("entry path %q contains a `..` traversal segment", name)
		}
	}
	clean := path.Clean(name)
	// After cleaning, a leading `..` (or a bare `..`) means the path escapes; a `.`
	// or empty result means the entry named no file.
	if clean == "." || clean == "" {
		return "", fmt.Errorf("entry path %q resolves to no file", name)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("entry path %q escapes the target", name)
	}
	if err := rejectControlPath(clean); err != nil {
		return "", err
	}
	return clean, nil
}

// hasDriveLetter reports whether name begins with a Windows drive/volume prefix
// (e.g. `C:` or `C:\`). path.IsAbs would classify such a name as relative on a
// unix host, so a bundle crafted on/for Windows could smuggle an absolute path
// past a unix importer without this guard.
func hasDriveLetter(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	c := name[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// rejectControlPath refuses a clean relative path that contains a VCS/ferry-control
// directory (`.git`, `.hg`, `.svn`, `.bzr`) or a `.ssh` directory as ANY component,
// anywhere in the path — not merely the first. A bundled `.git/**` must never land
// (a `.git/hooks/*` would run on the later `git init`/commit); `.ssh` is ferry's
// hands-off contract at this layer.
//
// The match is EXACT per component after case-folding: a component that, lowercased,
// equals one of the reserved names is refused. Case-folding is the core fix — macOS's
// default case-INSENSITIVE filesystem treats `.Git`/`.SSH` as `.git`/`.ssh`, so a
// case-sensitive check let `.Git/hooks/pre-commit` slip through and land a live hook.
// Exact-component matching keeps `.github` and `.gitignore` ALLOWED: neither, folded,
// equals `.git`, so only true control dirs are refused.
func rejectControlPath(clean string) error {
	for _, seg := range strings.Split(clean, "/") {
		switch strings.ToLower(seg) {
		case ".git", ".hg", ".svn", ".bzr":
			return fmt.Errorf("entry path %q is a VCS/control path (refused)", clean)
		case ".ssh":
			return fmt.Errorf("entry path %q contains a .ssh component (refused)", clean)
		}
	}
	return nil
}
