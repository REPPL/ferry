// Package sshguard centralises ferry's lexical "is this path under ~/.ssh"
// containment checks. ferry's top security invariant is that it never reads,
// writes, or operates on anything at or under ~/.ssh; the guards that enforce it
// (dotfile deploy-target validation in internal/dotfile, the repo/clone-path
// guard in cmd, and the zsh plugin's path expansion in internal/plugin/zsh) all
// build on the same primitive: given $HOME and a candidate path, decide whether
// the candidate is $HOME/.ssh itself or a descendant of it — by PURE PATH
// arithmetic, never stat'ing, lstat'ing, readlink'ing, or EvalSymlinks'ing
// anything at or under ~/.ssh. This package is that primitive's single home.
//
// It also hosts the shared symlink-RESOLVING home-containment guard
// (ResolvedContainment): the single check every WRITE boundary re-runs so a
// symlinked intermediate parent cannot redirect a lexically-valid write outside
// $HOME or into ~/.ssh. That walk lstat's/readlink's only components strictly
// ABOVE ~/.ssh and decides anything at/under ~/.ssh by pure path arithmetic —
// the same paranoid discipline as UnderHomeSSHExact, which already EvalSymlink's
// the trusted $HOME ancestor.
package sshguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscapesHome is returned by ResolvedContainment when a target does not
// resolve strictly within $HOME — an absolute escape, a `..` climb, or a
// symlinked intermediate parent that resolves outside $HOME. A managed target
// must live inside $HOME; anything else is a path-traversal attempt.
var ErrPathEscapesHome = errors.New("path escapes $HOME")

// ErrForbiddenSSHPath is returned by ResolvedContainment when a target IS, or
// resolves to, ~/.ssh or a path under it. ferry's top security invariant is that
// it never touches ~/.ssh, so any target that reaches it (directly or through a
// symlinked parent) is refused before any read/back-up/write can happen.
var ErrForbiddenSSHPath = errors.New("refusing a target under ~/.ssh (ferry never touches ~/.ssh)")

// SSHDirName is the conventional name of the ssh directory under $HOME.
const SSHDirName = ".ssh"

// FirstSegmentIsSSH reports whether the first path segment of a clean
// HOME-relative path is ~/.ssh, folding case so ".SSH", ".Ssh", etc. all match.
// rel must be a clean HOME-relative path with no leading "..". Folding is
// required because on a case-insensitive filesystem (the macOS default) the
// kernel maps ".SSH/config" into the real ~/.ssh; folding also refuses ".SSH/..."
// on a case-sensitive filesystem, which is acceptable fail-closed behaviour.
func FirstSegmentIsSSH(rel string) bool {
	first := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		first = rel[:i]
	}
	return strings.EqualFold(first, SSHDirName)
}

// UnderHomeSSH reports whether path is home's ~/.ssh directory itself or a
// descendant of it, by pure path arithmetic — it never stats path or ~/.ssh.
// Case is folded on EVERY component (the home parents AND the ".ssh" leaf): on a
// case-insensitive filesystem (the macOS default) a wrong-case HOME parent (e.g.
// /Users/ALICE vs /Users/alice) names the SAME directory, so a candidate such as
// /Users/ALICE/.ssh/config still resolves inside the real ~/.ssh and must be
// caught. filepath.Rel compares case-sensitively, so folding only the ".ssh"
// leaf would let such a candidate escape the guard. Folding the parents also
// over-refuses a genuinely-distinct wrong-case HOME on a case-SENSITIVE
// filesystem, which is acceptable fail-closed behaviour. Both home and path must
// be clean absolute paths.
func UnderHomeSSH(home, path string) bool {
	homeSegs := segments(home)
	pathSegs := segments(path)
	// path must have at least one component (the ".ssh" leaf) beyond home.
	if len(pathSegs) <= len(homeSegs) {
		return false
	}
	for i := range homeSegs {
		if !strings.EqualFold(homeSegs[i], pathSegs[i]) {
			return false
		}
	}
	return strings.EqualFold(pathSegs[len(homeSegs)], SSHDirName)
}

// segments splits a clean absolute path into its non-empty components, dropping
// the leading separator. The path is Clean'd first so "." and ".." are already
// collapsed and no empty interior components remain.
func segments(p string) []string {
	trimmed := strings.TrimPrefix(filepath.Clean(p), string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

// UnderHomeSSHExact reports whether the clean absolute path p is $HOME/.ssh or a
// descendant of it, computing $HOME/.ssh by STRING join (os.UserHomeDir + ".ssh")
// so ~/.ssh itself is NEVER stat'd, read, or EvalSymlink'd. Unlike UnderHomeSSH
// it does NOT fold case on the ".ssh" segment: its only callers — the config and
// deps "saferead" paths — reach it AFTER already refusing every repo-side symlink
// unconditionally, so its result never decides a refusal (it only sharpens the
// error message), and the exact-case behaviour is preserved deliberately rather
// than folded. HOME (a trusted ancestor strictly ABOVE ~/.ssh) is EvalSymlink'd
// so the compare is on the same real filesystem as an already-resolved candidate.
func UnderHomeSSHExact(p string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	if resolvedHome, herr := filepath.EvalSymlinks(home); herr == nil {
		home = resolvedHome
	}
	homeSSH := filepath.Clean(filepath.Join(home, SSHDirName))
	if p == homeSSH {
		return true, nil
	}
	rel, err := filepath.Rel(homeSSH, p)
	if err != nil {
		return false, nil
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)), nil
}

// maxSymlinkHops bounds symlink resolution in the containment walk so a cyclic
// chain (a -> b -> a) cannot loop forever; exceeding it fails closed.
const maxSymlinkHops = 40

// ResolvedContainment enforces the home-containment rules on dest WITH symlinks
// resolved: every EXISTING component between $HOME and dest's PARENT directory is
// walked, and a symlinked component must resolve to a location still strictly
// within $HOME and not at/under ~/.ssh — otherwise a write to the
// lexically-valid dest would land through the link outside $HOME
// (ErrPathEscapesHome) or inside ~/.ssh (ErrForbiddenSSHPath).
//
// The LEAF itself is deliberately NOT resolved: a symlink at the destination is
// a distinct, observable state the write path refuses on its own (the dotfile
// domain returns UnexpectedKindError, and atomic writes replace-not-follow), and
// adopt must still be able to BUILD a Target for a bridge symlink to enumerate
// and migrate it.
//
// The walk is LEXICAL in the ~/.ssh-paranoid style of the rest of ferry: each
// component is Lstat'd (never following the component itself), a symlink's
// target TEXT is read with os.Readlink (no follow) and resolved by pure path
// arithmetic, and NOTHING at/under ~/.ssh is ever Lstat'd, Readlink'd, or
// EvalSymlink'd — the moment path math lands a component there, the walk refuses
// by string compare alone. Only $HOME itself (a trusted ancestor strictly above
// ~/.ssh) is EvalSymlink'd, so an absolute link target in resolved form (e.g.
// macOS /var -> /private/var) compares on the same real filesystem. A
// not-yet-existing tail (fresh machine) is fine: there is no symlink left to
// traverse.
//
// It presumes dest has already passed the LEXICAL containment guard (a `..`
// climb or an at-/under-~/.ssh lexical target); callers pair it with a lexical
// check first. It is the SINGLE resolved-containment guard reused by the dotfile
// deploy-target boundary and the backup engine's write/restore boundaries.
func ResolvedContainment(home, dest string) error {
	cleanHome := filepath.Clean(home)
	resolvedHome := cleanHome
	if r, err := filepath.EvalSymlinks(cleanHome); err == nil {
		resolvedHome = r
	}

	// inHome/underSSH compare against BOTH the raw and resolved $HOME so a link
	// target written in either form is judged correctly.
	inHome := func(p string) bool {
		return strictlyUnder(cleanHome, p) || strictlyUnder(resolvedHome, p)
	}
	// underSSH folds case on the ~/.ssh segment, consistent with the lexical
	// guard: on a case-insensitive filesystem a component resolving to ~/.SSH is
	// the same directory as ~/.ssh, so it must be refused too.
	underSSH := func(p string) bool {
		return UnderHomeSSH(cleanHome, p) || UnderHomeSSH(resolvedHome, p)
	}

	// Walk the PARENT chain only (see the leaf note above). A parent equal to
	// $HOME has no intermediate components to validate.
	parent := filepath.Dir(filepath.Clean(dest))
	if parent == cleanHome || parent == resolvedHome {
		return nil
	}
	rel, err := filepath.Rel(cleanHome, parent)
	if err != nil {
		return ErrPathEscapesHome
	}
	segs := strings.Split(rel, string(filepath.Separator))
	cur := resolvedHome
	hops := 0
	for _, seg := range segs {
		if seg == "" || seg == "." {
			continue
		}
		next := filepath.Join(cur, seg)
		// Never Lstat at/under ~/.ssh: conclude by string compare alone.
		if underSSH(next) {
			return ErrForbiddenSSHPath
		}
		// Resolve this component through its WHOLE symlink chain (a link to a
		// link must not slip through via the kernel's own resolution on the next
		// Lstat), validating every hop.
		for {
			fi, lerr := os.Lstat(next)
			if lerr != nil {
				if errors.Is(lerr, fs.ErrNotExist) {
					// A not-yet-existing tail: no symlink left to traverse. The
					// remaining components are pure lexical joins, already
					// validated lexically.
					return nil
				}
				// ELOOP / permission / anything else: fail closed.
				return lerr
			}
			if fi.Mode()&fs.ModeSymlink == 0 {
				break
			}
			hops++
			if hops > maxSymlinkHops {
				return ErrPathEscapesHome // fail closed on a pathological chain
			}
			targetTxt, rerr := os.Readlink(next)
			if rerr != nil {
				return rerr
			}
			if !filepath.IsAbs(targetTxt) {
				targetTxt = filepath.Join(filepath.Dir(next), targetTxt)
			}
			targetTxt = filepath.Clean(targetTxt)
			if underSSH(targetTxt) {
				return ErrForbiddenSSHPath
			}
			if !inHome(targetTxt) {
				return ErrPathEscapesHome
			}
			next = targetTxt
		}
		// Continue the walk from the fully resolved (in-$HOME) component.
		cur = next
	}
	return nil
}

// strictlyUnder reports whether p is a strict descendant of base (not base
// itself), by pure path arithmetic. Both must be clean absolute paths.
func strictlyUnder(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
