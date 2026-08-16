package cmd

import "testing"

// groupRisky once bucketed every FileDomain it did not name into "dotfiles",
// so the walkthrough's group header and bulk consent named a domain the user
// was not reviewing. These tests pin the fix: every domain groups under its
// own name, in registry order, and an item missing its fileDomain stamp
// (unreachable in production — the plan driver stamps every FileDomain item,
// and preference items are filtered out before grouping) degrades to the
// visible dotfiles bucket instead of an unnamed group.
func TestGroupRiskyGroupsEveryDomainUnderItsOwnName(t *testing.T) {
	risky := []planItem{
		{domain: "iterm2-profiles:p.json", fileDomain: "iterm2-profiles"},
		{domain: "emacs:init.el", fileDomain: "emacs"},
		{domain: "dotfiles:.zshrc", fileDomain: "dotfiles"},
		{domain: "keybindings:DefaultKeyBinding.dict", fileDomain: "keybindings"},
		{domain: "agents:AGENTS.md", fileDomain: "agents"},
		{domain: "terminals:alacritty.toml", fileDomain: "terminals"},
	}
	groups := groupRisky(risky)

	wantOrder := []string{"dotfiles", "agents", "terminals", "keybindings", "emacs", "iterm2-profiles"}
	if len(groups) != len(wantOrder) {
		t.Fatalf("got %d groups, want %d: %+v", len(groups), len(wantOrder), groups)
	}
	for i, want := range wantOrder {
		if groups[i].name != want {
			t.Errorf("group[%d] = %q, want %q (registry order)", i, groups[i].name, want)
		}
		if len(groups[i].items) != 1 {
			t.Errorf("group %q has %d items, want 1", groups[i].name, len(groups[i].items))
		}
	}
}

func TestGroupRiskyNeverLumpsOtherDomainsIntoDotfiles(t *testing.T) {
	risky := []planItem{
		{domain: "emacs:init.el", fileDomain: "emacs"},
		{domain: "keybindings:DefaultKeyBinding.dict", fileDomain: "keybindings"},
	}
	for _, g := range groupRisky(risky) {
		if g.name == "dotfiles" {
			t.Fatalf("emacs/keybindings items grouped under %q — the bulk consent would name a domain the user is not reviewing", g.name)
		}
	}
}

func TestGroupRiskyUnstampedItemsDegradeToDotfilesBucket(t *testing.T) {
	// Hand-built state: no production path emits a risky item without a
	// fileDomain stamp. The defensive default must keep such an item visible
	// under a named bucket rather than an unnamed group.
	risky := []planItem{
		{domain: "unstamped item", fileDomain: ""},
	}
	groups := groupRisky(risky)
	if len(groups) != 1 || groups[0].name != "dotfiles" {
		t.Fatalf("unstamped item grouped as %+v, want the dotfiles fallback bucket", groups)
	}
}
