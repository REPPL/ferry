package cmd

import "strings"

// overlayMarker is the exact comment line ferry injects above a per-machine
// overlay include directive, for EVERY include-style file format. It is the
// shape-keyed strip's anchor (internal/dotfile isFerryOverlayInclude keys off
// the line that FOLLOWS it) and MUST stay byte-identical to the marker the
// dotfile package recognises.
const overlayMarker = "# ferry: per-machine overlay, sourced last so it wins"

// directiveSpec is the per-file-format description of ferry's injected
// per-machine overlay include directive. The include-sidecar mechanism is
// otherwise format-agnostic — it always injects ferry's marker comment plus one
// include line that sources the `.local` sidecar LAST so it wins — so the ONLY
// thing that varies between zsh and tmux is the SYNTAX of that include line.
// directiveSpec makes that syntax DATA, selected per bare dotfile name
// (directiveSpecFor), so a new include-style format is a new spec value rather
// than a new branch through appendSourceDirective / stripSourceDirective /
// sourceDirectivePresent.
//
// This is deliberately the FILE-KEYED half of the two-strip contract: render +
// namesFile drive appendSourceDirective and the capture-write stripSourceDirective
// (both of which know the exact overlay FILE name). The SHAPE-keyed half lives
// separately in internal/dotfile (isFerryOverlayInclude), keyed off the marker +
// the include line's structure, and is NOT merged with this — the two strips
// remain distinct functions with distinct keying.
type directiveSpec struct {
	// marker is the comment line injected above the include directive.
	marker string
	// render builds the include directive ferry injects for overlay basename
	// `file` (e.g. ".tmux.conf.local"), sourced last so the sidecar wins. It may
	// span MULTIPLE lines: the git-INI directive is a two-line `[include]` block
	// (a section header line plus a `path = …` line), where zsh/tmux are one line.
	render func(file string) string
	// namesFile reports whether an uncommented CODE line is the include directive
	// (in this format) that names overlay `file` — the recogniser the file-keyed
	// strip and the idempotency check use to drop/detect ONLY ferry's own line. For
	// a multi-line directive it matches the SINGLE line that names the file (git's
	// `path = ~/<file>`); the preceding header line is dropped alongside it via
	// header (below).
	namesFile func(code, file string) bool
	// header, when non-empty, is a section-header line that PRECEDES the
	// file-naming line in a multi-line directive (git's `[include]`). The
	// file-keyed strip drops it together with the file-naming line so no orphan
	// header survives; single-line directives (zsh/tmux) leave it "" and are
	// byte-identical to before. It is compared trimmed.
	header string
}

// shellDirective is the zsh (and generic shell) include directive:
//
//	[ -f ~/.zshrc.local ] && source ~/.zshrc.local
//
// Its render/marker are byte-identical to what ferry has always injected, so the
// zsh round-trip stays byte-for-byte stable (TestFn5_ZshRoundTripByteStable_Baseline
// and the overlay-bypass guard eval are the proof).
var shellDirective = directiveSpec{
	marker: overlayMarker,
	render: func(file string) string { return "[ -f ~/" + file + " ] && source ~/" + file },
	namesFile: func(code, file string) bool {
		if !strings.Contains(code, file) {
			return false
		}
		lc := strings.ToLower(code)
		return strings.Contains(lc, "source ") || strings.Contains(code, ". ")
	},
}

// tmuxDirective is the tmux include directive:
//
//	source-file -q ~/.tmux.conf.local
//
// `source-file -q` sources the per-machine file if it exists and stays quiet if
// it does not — the tmux analogue of the shell `[ -f … ] && source …` guard.
var tmuxDirective = directiveSpec{
	marker: overlayMarker,
	render: func(file string) string { return "source-file -q ~/" + file },
	namesFile: func(code, file string) bool {
		return strings.Contains(code, file) && strings.Contains(code, "source-file")
	},
}

// gitDirective is the git-INI include directive — a TWO-line block:
//
//	[include]
//		path = ~/.gitconfig.local
//
// git applies includes inline and last-wins, so an [include] appended LAST makes
// the machine-local ~/.gitconfig.local override — the native equivalent of
// ferry's per-machine overlay. Unlike the shell/tmux one-liners, the directive
// spans a section header (`[include]`) plus a tab-indented `path` line; render
// emits both, namesFile matches the `path` line, and header ("[include]") is the
// preceding line the file-keyed strip drops alongside it.
var gitDirective = directiveSpec{
	marker: overlayMarker,
	render: func(file string) string { return "[include]\n\tpath = ~/" + file },
	namesFile: func(code, file string) bool {
		// Match ferry's own `path = ~/<file>` line PRECISELY: key "path", value
		// exactly "~/" + file. A user's [includeIf] path to a different file, or a
		// path to some other target, is left untouched.
		eq := strings.IndexByte(code, '=')
		if eq < 0 {
			return false
		}
		key := strings.ToLower(strings.TrimSpace(code[:eq]))
		val := strings.TrimSpace(code[eq+1:])
		return key == "path" && val == "~/"+file
	},
	header: "[include]",
}

// directiveSpecFor selects the include directive syntax for a bare dotfile name.
// It is only consulted for include-sidecar domains (usesIncludeSidecar): tmux
// gets the tmux `source-file` directive, git the git-INI `[include]` block, every
// other include-style dotfile (the zsh family) the shell `source` directive.
func directiveSpecFor(bare string) directiveSpec {
	switch bare {
	case "tmux.conf":
		return tmuxDirective
	case "gitconfig":
		return gitDirective
	}
	return shellDirective
}

// overlayDomainDir maps a bare dotfile name to its per-machine overlay directory
// under <repo>/local/. Include-style families are grouped by tool (zsh's three
// bares share local/zsh/; tmux uses local/tmux/); every other dotfile overlays
// under its own bare name. It is the single source of truth both apply
// (resolveOverlaySource) and capture (localOverlayPath) resolve the overlay path
// through, so the two directions can never disagree on where a sidecar lives.
func overlayDomainDir(bare string) string {
	switch bare {
	case "zshrc", "zshenv", "zprofile":
		return "zsh"
	case "tmux.conf":
		return "tmux"
	case "gitconfig":
		return "git"
	}
	return bare
}
