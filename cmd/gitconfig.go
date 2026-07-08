package cmd

import (
	"fmt"

	"github.com/REPPL/ferry/internal/gitconfig"
)

// gitconfigBare is the bare dotfile name of ~/.gitconfig. git rides the dotfiles
// list as an include-sidecar dotfile (like tmux), but adds the identity firewall
// and the git-INI [include] overlay directive keyed off this name.
const gitconfigBare = "gitconfig"

// sharedGitTransform is the identity firewall applied to the SHARED git-config
// bytes on BOTH the deploy composition and the shared-capture write: for
// ~/.gitconfig it strips every identity key (user.email/name/signingkey,
// gpg.program, credential.helper) and every [includeIf …] block so a machine's
// commit identity can never reach the shared ~/.gitconfig or the shared repo
// (plan §3.1; the STOP condition "git leaks an identity key to the shared repo").
// For every other dotfile it is a byte-for-byte no-op, and for an
// already-identity-free gitconfig it returns the input unchanged (so the git
// round-trip stays byte-stable). It is applied BEFORE ferry's [include] directive
// is appended, so the injected overlay line is preserved.
func sharedGitTransform(bare string, raw []byte) []byte {
	if bare != gitconfigBare {
		return raw
	}
	return gitconfig.SharedContent(raw)
}

// gitCredentialHelperWarnings returns a warning when a git-config sets
// `credential.helper = store` — the backend that writes credentials as PLAINTEXT
// into ~/.git-credentials. ferry never carries ~/.git-credentials (it is treated
// like ~/.ssh — untouchable) and the helper must be `osxkeychain`; the warning
// tells the user to switch. It scans every source that composes the deployed
// git-config (the shared source and, when present, the per-machine overlay).
func gitCredentialHelperWarnings(bare string, sources ...[]byte) []string {
	if bare != gitconfigBare {
		return nil
	}
	for _, src := range sources {
		if gitconfig.CredentialHelperStore(src) {
			return []string{fmt.Sprintf(
				"warning: .gitconfig sets credential.helper = store, which writes credentials as PLAINTEXT to ~/.git-credentials. ferry never carries ~/.git-credentials; switch to `credential.helper = osxkeychain`.")}
		}
	}
	return nil
}
