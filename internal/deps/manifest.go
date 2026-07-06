package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/REPPL/ferry/internal/platform"
)

// Manifest describes the dependency manifest that applies on THIS machine: the
// detected package manager, the shared manifest file, and its per-machine
// gitignored overlay (if present). It is selected by runtime.GOOS plus the
// detected manager — never assumed to be brew.
type Manifest struct {
	// Manager is the detected package manager this manifest targets.
	Manager platform.PackageManager
	// GOOS is the runtime.GOOS the manifest was selected for ("darwin"/"linux").
	GOOS string
	// Shared is the absolute path to the committed manifest file
	// (deps/Brewfile.<goos> or deps/apt.txt). It is the manifest's identity even
	// when the file does not yet exist on disk.
	Shared string
	// Local is the absolute path to the per-machine gitignored overlay
	// (deps/Brewfile.<goos>.local). Empty for apt (no overlay convention).
	Local string
}

// SelectManifest returns the manifest that applies on THIS machine: it chooses
// the file by the host runtime.GOOS and the DETECTED package manager.
//
//   - brew  -> deps/Brewfile.<goos> (+ deps/Brewfile.<goos>.local overlay)
//   - apt   -> deps/apt.txt (no overlay)
//   - none  -> error: no package manager present (the caller REPORTS this; it is
//     never a reason to bootstrap a manager)
//
// depsDir is the repo's deps/ directory. The returned paths are absolute under
// depsDir; their on-disk existence is the caller's concern (Install handles a
// missing manifest gracefully).
func SelectManifest(depsDir string) (Manifest, error) {
	return selectManifest(depsDir, runtime.GOOS, platform.DetectPackageManager())
}

// selectManifest is the testable core: GOOS and the detected manager are
// explicit so tests cover every (GOOS, manager) pair without a real machine.
func selectManifest(depsDir, goos string, mgr platform.PackageManager) (Manifest, error) {
	if depsDir == "" {
		return Manifest{}, fmt.Errorf("deps: empty deps directory")
	}
	switch mgr {
	case platform.ManagerBrew:
		base := "Brewfile." + goos
		return Manifest{
			Manager: mgr,
			GOOS:    goos,
			Shared:  filepath.Join(depsDir, base),
			Local:   filepath.Join(depsDir, base+".local"),
		}, nil
	case platform.ManagerApt:
		return Manifest{
			Manager: mgr,
			GOOS:    goos,
			Shared:  filepath.Join(depsDir, "apt.txt"),
			Local:   "", // apt has no per-machine overlay convention
		}, nil
	default:
		return Manifest{}, ErrNoPackageManager
	}
}

// Entries reads the manifest and returns its parsed entries, layering the
// per-machine .local overlay AFTER the shared file (local entries appended last,
// so apply --deps installs them last). A missing shared OR local file is not an
// error — it contributes no entries. The slice preserves source order.
func (m Manifest) Entries() ([]string, error) {
	entries, err := parseManifestFile(m.Shared, m.Manager)
	if err != nil {
		return nil, err
	}
	if m.Local != "" {
		local, err := parseManifestFile(m.Local, m.Manager)
		if err != nil {
			return nil, err
		}
		entries = append(entries, local...)
	}
	return entries, nil
}

// ParseManifest parses a single manifest file (no overlay layering) into its
// entries, interpreting it per the given manager's format. A non-existent file
// yields an empty slice and no error — an absent manifest is a valid empty one.
func ParseManifest(path string, mgr platform.PackageManager) ([]string, error) {
	return parseManifestFile(path, mgr)
}

func parseManifestFile(path string, mgr platform.PackageManager) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	// Symlink-guard the repo-side manifest BEFORE os.ReadFile: a symlinked apt.txt
	// or Brewfile (e.g. deps/apt.txt -> ~/.ssh/config), OR a symlinked deps/
	// directory (deps -> ~/.ssh), is refused, never read through. ferry only writes
	// regular files under deps/, so a symlink is illegitimate. The guard walks from
	// the REPO ROOT (deps/'s parent, = filepath.Dir(filepath.Dir(path))) so the deps
	// component itself is Lstat'd, not just the manifest file.
	safe, err := safeRepoManifest(filepath.Dir(filepath.Dir(path)), path)
	if err != nil {
		return nil, err
	}
	path = safe
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("deps: read manifest %s: %w", path, err)
	}
	switch mgr {
	case platform.ManagerApt:
		return parseAptLines(string(data))
	default:
		// brew (and any future bundle-style manager): keep each meaningful
		// Brewfile line verbatim (brew/cask/tap/mas/font ...) so the entry set is
		// the real manifest content the installed-set diff is checked against.
		return parseBrewfileLines(string(data))
	}
}

// parseBrewfileLines returns the non-blank, non-comment lines of a Brewfile,
// trimmed of surrounding whitespace, AFTER gating each directive through the
// allow-list (ValidateBrewfileDirective). We do NOT decode the Ruby DSL — the
// entry is the directive line as written (e.g. `brew "zoxide"`, `cask "iterm2"`),
// which is exactly what we diff the installed set against and what brew bundle
// itself consumes. A cloned config repo's Brewfile is UNTRUSTED input and
// `brew bundle --file=` runs install-time code, so any directive outside the
// allow-list (a URL/custom-tap arg, a local-path formula, an args:/postflight
// block, or an arbitrary Ruby directive) REFUSES the whole manifest — fail
// closed, mirroring parseAptLines.
func parseBrewfileLines(s string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := ValidateBrewfileDirective(line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, nil
}

// allowedBrewDirectives is the Brewfile directive allow-list ferry will hand to
// `brew bundle`. It is a SUPERSET of what `brew bundle dump` emits
// (brew/cask/mas/tap/vscode) so a capture->apply round-trip of ferry's own dump
// always passes; whalebrew is included per the dump-superset contract. Any other
// first token — arbitrary Ruby (`system`, `if`), a `cask_args` global, or a
// third-party bundle plugin's directive (`npm`, `uv`) — is refused.
var allowedBrewDirectives = map[string]bool{
	"brew": true, "cask": true, "mas": true, "tap": true, "vscode": true, "whalebrew": true,
}

// dangerousBrewOptions are Brewfile option keys that run arbitrary install-time
// code (or fetch/build from an attacker source). Their presence anywhere on a
// directive line refuses it. `brew "x", link: false` and `mas "x", id: N` — the
// benign options `brew bundle dump` emits — are deliberately NOT in this list.
var dangerousBrewOptions = []string{"args:", "postinstall:", "preinstall:", "postflight", "preflight", "requires:", "require "}

// ValidateBrewfileDirective enforces the Brewfile allow-list on ONE already-
// trimmed, non-blank, non-comment directive line. It refuses, with an error that
// names the offending line, anything a cloned repo could weaponise:
//
//   - a first token outside allowedBrewDirectives (arbitrary Ruby / plugin dirs);
//   - a URL argument ("://") — a custom-tap URL or remote formula;
//   - a code-execution option (dangerousBrewOptions: args:/postflight/…);
//   - a first argument that is not a quoted, name-shaped token (a local-path
//     formula like `brew "./x.rb"` / `brew "~/x"` / `brew "../x"`).
//
// mas is special-cased: its first argument is a free-text app NAME (spaces and
// punctuation are normal) and the numeric `id:` is what drives the install, so
// the name is not charset-checked but a numeric `id:` is REQUIRED. tap must be
// exactly `user/repo` (two segments), never a three-segment or URL form.
func ValidateBrewfileDirective(line string) error {
	kw, rest := splitFirstField(line)
	if !allowedBrewDirectives[kw] {
		return fmt.Errorf("deps: refusing Brewfile directive %q (line %q): only brew/cask/mas/tap/vscode/whalebrew are allowed — a cloned repo's Brewfile is untrusted and `brew bundle` runs install-time code", kw, line)
	}
	if strings.Contains(rest, "://") {
		return fmt.Errorf("deps: refusing Brewfile line %q: a URL argument (custom tap or remote formula) can pull and run attacker code", line)
	}
	for _, bad := range dangerousBrewOptions {
		if strings.Contains(rest, bad) {
			return fmt.Errorf("deps: refusing Brewfile line %q: the %q option can run arbitrary install-time code", line, strings.TrimSpace(bad))
		}
	}
	if kw == "mas" {
		if !masEntryRe.MatchString(rest) || hasRubyInterpolation(rest) {
			return fmt.Errorf("deps: refusing Brewfile line %q: a `mas` entry must be `mas \"Name\", id: <number>` with no Ruby `#{...}` interpolation in the name", line)
		}
		return nil
	}
	name, tail, ok := firstQuotedArg(rest)
	if !ok {
		return fmt.Errorf("deps: refusing Brewfile line %q: expected a quoted name argument", line)
	}
	if hasRubyInterpolation(name) {
		return fmt.Errorf("deps: refusing Brewfile line %q: the quoted name contains Ruby `#{...}` interpolation, which `brew bundle` evaluates as code", line)
	}
	if !isBrewNameShaped(name) {
		return fmt.Errorf("deps: refusing Brewfile line %q: %q is not a plain package/tap name (a local path or odd characters are not allowed)", line, name)
	}
	if kw == "tap" && strings.Count(name, "/") != 1 {
		return fmt.Errorf("deps: refusing Brewfile line %q: a tap must be exactly `user/repo`", line)
	}
	// END-ANCHOR the directive: after the quoted name, the ONLY thing allowed is a
	// benign `, key: <literal>` option list. Without this, arbitrary Ruby riding
	// after a valid `keyword "name"` prefix (`brew "git"; system "id"`,
	// `cask "x" and system(…)`, `brew "git" if system(…)`, `brew "git".tap{ … }`)
	// would pass the first-token/first-arg checks and then be instance_eval'd by
	// `brew bundle`. Only true/false, an integer, a symbol, or a quoted string are
	// accepted as option values — none can execute code.
	if !benignBrewOptionTail(tail) {
		return fmt.Errorf("deps: refusing Brewfile line %q: trailing content after the quoted name is not a plain `, option: value` list — arbitrary Ruby would be run by `brew bundle`", line)
	}
	return nil
}

// brewOptionTailRe matches the text AFTER a directive's quoted name: either empty
// or a sequence of `, key: <literal>` pairs (link: false, restart_service: :changed,
// id: 123). Option VALUES are restricted to code-free literals — true/false, an
// integer, a :symbol, or a double-quoted string — so no method call, block, or
// statement separator can survive. dangerousBrewOptions (args:/postinstall:/…) are
// rejected by an earlier substring check; this anchor additionally refuses any
// UNKNOWN trailing tokens (`; system …`, ` if …`, ` and …`, `.tap{ … }`).
var brewOptionTailRe = regexp.MustCompile(`^(\s*,\s*[A-Za-z_]+:\s*(true|false|[0-9]+|:[A-Za-z_]+|"[^"]*"))*\s*$`)

// benignBrewOptionTail reports whether tail is empty or ONLY a benign option list.
// A double-quoted option VALUE that carries Ruby `#{...}` interpolation is refused
// even though its shape matches brewOptionTailRe: `brew bundle` evaluates it as code.
func benignBrewOptionTail(tail string) bool {
	return brewOptionTailRe.MatchString(tail) && !hasRubyInterpolation(tail)
}

// hasRubyInterpolation reports whether an accepted double-quoted Brewfile literal
// carries a Ruby string-interpolation marker. `brew bundle --file=` evaluates the
// whole Brewfile as Ruby, and a DOUBLE-quoted string interpolates at evaluation time
// in three forms: `#{expr}` (an arbitrary expression — RCE, e.g. `#{system('id')}`),
// `#@ivar`/`#@@cvar`, and `#$global`. Any accepted `"..."` literal carrying one of
// these would inject Ruby, so every accept-point of a double-quoted literal (option
// value, mas app name, brew/cask/tap/vscode name) rejects it via this check. A plain
// `#` not followed by `{`, `@`, or `$` is inert and left alone, so a legitimate name
// such as "C# Tools" is unaffected.
func hasRubyInterpolation(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '#' {
			switch s[i+1] {
			case '{', '@', '$':
				return true
			}
		}
	}
	return false
}

// masEntryRe matches a `mas "<app name>", id: <digits>` entry (the app name is
// free text; the numeric id is what drives the install).
var masEntryRe = regexp.MustCompile(`^"[^"]*",\s*id:\s*[0-9]+\s*$`)

// splitFirstField splits a line into its first whitespace-delimited token and the
// trimmed remainder.
func splitFirstField(line string) (first, rest string) {
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		return line[:i], strings.TrimSpace(line[i+1:])
	}
	return line, ""
}

// firstQuotedArg returns the content of the FIRST double-quoted argument in rest
// and the tail — the text AFTER that argument's closing quote (used to end-anchor
// the directive). ok is false when rest has no complete double-quoted argument.
func firstQuotedArg(rest string) (name, tail string, ok bool) {
	open := strings.IndexByte(rest, '"')
	if open < 0 {
		return "", "", false
	}
	rel := strings.IndexByte(rest[open+1:], '"')
	if rel < 0 {
		return "", "", false
	}
	end := open + 1 + rel // index of the closing quote
	return rest[open+1 : end], rest[end+1:], true
}

// isBrewNameShaped reports whether name is a plain package/tap/extension name: no
// leading path indicator ("/", "~", "."), no "..", and only the characters brew
// package/tap/cask/vscode names use ([A-Za-z0-9] plus @ . _ + - / :). This admits
// `node@18`, `user/repo`, `ms-python.python`, and `user/repo/formula`, while
// rejecting `./x.rb`, `~/x`, `../x`, and any shell/URL punctuation.
func isBrewNameShaped(name string) bool {
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") || strings.HasPrefix(name, ".") {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '@' || r == '.' || r == '_' || r == '+' || r == '-' || r == '/' || r == ':':
		default:
			return false
		}
	}
	return true
}

// parseAptLines returns the package names in an apt.txt: one package per line,
// blank lines and # comments ignored, inline trailing comments stripped.
//
// Every surviving entry is validated as a package NAME (validateAptName): an
// entry that starts with "-" or carries a character outside the apt name charset
// is REFUSED with an error that names the offending line. This is a trust
// boundary — apt.txt comes from a cloned config repo, and `ferry apply --deps`
// runs apt-get as root. Without this, a line like `-oDPkg::Pre-Invoke::=touch
// /tmp/x` would reach apt-get as an option and execute as root. Rejecting the
// whole manifest (rather than silently dropping the line) fails closed.
func parseAptLines(s string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if err := ValidateAptName(line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, nil
}

// ValidateAptName refuses an apt package-name entry that is not a plain package
// name. It guards two apt boundaries that both run apt-get as root: the cloned
// config repo's apt.txt on install (parseAptLines), and the ferry-written
// deps-installed.txt on `restore --packages` uninstall.
//
// Every character must be in the apt package-name charset: ASCII letters,
// digits, and "+", "-", ".", ":", "~" (the characters apt permits in package
// names, epochs, and versions). Anything else — spaces, "=", "/", shell
// metacharacters — is refused. On top of the charset, the entry is anchored to
// the Debian package-name shape:
//
//   - It must START with an ASCII alphanumeric. A leading "-" reads as an
//     apt-get option, not a package; a leading "~" or "." lets apt do
//     pattern/regex matching and select unintended packages.
//   - It must NOT END with "-". apt-get treats a trailing "-" on a positional
//     package specifier as its REMOVE modifier, parsed during package
//     resolution rather than option parsing — so the "--" separator gives no
//     protection, and an entry such as `ufw-` would run `apt-get install -y --
//     ufw-` and REMOVE ufw as root under `sudo ferry apply --deps`.
//
// This still admits g++, python3.11, foo:amd64, libfoo-dev, and zsh. The error
// names the offending line so the user can fix the manifest.
func ValidateAptName(name string) error {
	if name == "" {
		return fmt.Errorf("deps: refusing empty apt manifest entry")
	}
	for _, r := range name {
		if !isAptNameRune(r) {
			return fmt.Errorf("deps: refusing apt manifest entry %q: character %q is not allowed in a package name (allowed: letters, digits, and + - . : ~)", name, string(r))
		}
	}
	if first := name[0]; !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z' || first >= '0' && first <= '9') {
		return fmt.Errorf("deps: refusing apt manifest entry %q: a package name must start with a letter or digit (a leading %q, %q, or %q is read by apt-get as an option or a pattern selector, not a package)", name, "-", "~", ".")
	}
	if strings.HasSuffix(name, "-") {
		return fmt.Errorf("deps: refusing apt manifest entry %q: a trailing %q is apt-get's REMOVE modifier, not part of a package name", name, "-")
	}
	return nil
}

// ValidateAptRemoveName validates an entry for the apt UNINSTALL rail
// (`restore --packages`, which runs `apt-get remove` as root). It applies every
// ValidateAptName rule and additionally refuses a trailing "+", apt-get's INSTALL
// modifier. Like the trailing "-" REMOVE modifier, a trailing "+" is parsed
// during package resolution rather than option parsing, so the "--" separator
// gives no protection: a tampered deps-installed.txt entry such as
// `openssh-server+` would run `apt-get remove -y -- openssh-server+` and INSTALL
// openssh-server as root instead of removing it.
//
// This reject is remove-rail-only. On the install rail a trailing "+" is a no-op
// (the package is being installed anyway) and `g++` — a real package whose name
// ends in "+" — must stay installable, so ValidateAptName keeps it. The cost is
// that `g++` cannot be uninstalled through `restore --packages`: apt would
// resolve `g++` as a package (full-token-first) rather than a modifier, but
// ferry cannot distinguish that from a tampered `pkg+` without querying dpkg, so
// it fails closed and leaves the record intact. Uninstall g++ manually if needed.
func ValidateAptRemoveName(name string) error {
	if err := ValidateAptName(name); err != nil {
		return err
	}
	if strings.HasSuffix(name, "+") {
		return fmt.Errorf("deps: refusing apt uninstall entry %q: a trailing %q is apt-get's INSTALL modifier, not part of a package name", name, "+")
	}
	return nil
}

// isAptNameRune reports whether r may appear in an apt package-name entry.
func isAptNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '+' || r == '-' || r == '.' || r == ':' || r == '~':
		return true
	default:
		return false
	}
}
