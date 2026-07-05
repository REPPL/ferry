// Package sshguard centralises ferry's lexical "is this path under ~/.ssh"
// containment checks. ferry's top security invariant is that it never reads,
// writes, or operates on anything at or under ~/.ssh; the guards that enforce it
// (dotfile deploy-target validation in internal/dotfile, the repo/clone-path
// guard in cmd, and the zsh plugin's path expansion in internal/plugin/zsh) all
// build on the same primitive: given $HOME and a candidate path, decide whether
// the candidate is $HOME/.ssh itself or a descendant of it — by PURE PATH
// arithmetic, never stat'ing, lstat'ing, readlink'ing, or EvalSymlinks'ing
// anything at or under ~/.ssh. This package is that primitive's single home.
package sshguard

import (
	"os"
	"path/filepath"
	"strings"
)

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
// Case is folded ONLY on the ".ssh" segment (see FirstSegmentIsSSH); the home
// parent components match EXACTLY. Both home and path must be clean absolute
// paths.
func UnderHomeSSH(home, path string) bool {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return FirstSegmentIsSSH(rel)
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
