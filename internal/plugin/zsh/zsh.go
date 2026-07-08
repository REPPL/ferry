// Package zsh is ferry's first config plugin (v0.3.0): it detects, parses,
// analyzes, repairs, and seeds ~/.zshrc through the generic plugin interface.
// It deploys through the EXISTING include-sidecar dotfile machinery — no new
// deploy mechanism.
package zsh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/REPPL/ferry/internal/dotfile"
	"github.com/REPPL/ferry/internal/plugin"
)

// Plugin implements plugin.Plugin for the zsh domain. Home is the directory
// Analyze uses to resolve `source` targets for the dead-source repair; New
// defaults it to the process home. v1 reads ~/.zshrc only (ZDOTDIR is out of
// scope, same as the shipped v0.2.1 reader).
type Plugin struct {
	Home string
}

// New returns the zsh plugin with Home defaulted to the user's home directory.
func New() *Plugin {
	home, _ := os.UserHomeDir()
	return &Plugin{Home: home}
}

// Domain names the plugin's secret-store domain.
func (p *Plugin) Domain() string { return "zsh" }

// Detect locates ~/.zshrc with readExistingZshrc's discipline: Lstat, never
// resolve symlinks; a symlinked or non-regular entry is unmanageable; a regular
// file whose read fails is Unreadable (r5-M1); near-empty content allows the
// starter path.
func (p *Plugin) Detect(home string) (plugin.Detection, error) {
	path := filepath.Join(home, ".zshrc")
	det := plugin.Detection{Path: path}
	li, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			det.Reason = plugin.Absent
			return det, nil
		}
		// Present but not statable: treat as unreadable (continue-without-managing).
		det.Reason = plugin.Unreadable
		return det, nil
	}
	if li.Mode()&os.ModeSymlink != 0 {
		det.Reason = plugin.Symlink
		return det, nil
	}
	if !li.Mode().IsRegular() {
		det.Reason = plugin.Irregular
		return det, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		det.Reason = plugin.Unreadable
		return det, nil
	}
	if dotfile.IsNearEmpty(data) {
		det.Reason = plugin.NearEmpty
		return det, nil
	}
	det.Present = true
	det.Reason = plugin.OK
	return det, nil
}

// Describe renders a one-line human explanation of a block for the adopt UI.
func (p *Plugin) Describe(b plugin.Block) string {
	first := ""
	for _, l := range strings.Split(string(b.Raw), "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		first = t
		break
	}
	// Clip by RUNES, not bytes: multibyte content (box-drawing banners, emoji)
	// must never be split mid-rune into mojibake (found in review 2026-07-02;
	// the preview helper already clips rune-wise).
	if r := []rune(first); len(r) > 60 {
		first = string(r[:57]) + "..."
	}
	return fmt.Sprintf("%s: %s", b.Kind, first)
}
