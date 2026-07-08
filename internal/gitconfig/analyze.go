package gitconfig

import "strings"

// identityKeys are the git config keys that must NEVER reach the shared
// ~/.gitconfig (plan §3.1): sharing one machine's identity would silently
// corrupt another machine's commit authorship, signing, or credential access.
// They are forced to the never-shared ~/.gitconfig.local layer.
//
//   - user.email / user.name — commit authorship.
//   - user.signingkey — the signing key id / public-key path (identity, NOT a
//     secret: it is safe as plaintext in the LOCAL file, never routed through the
//     secret store).
//   - gpg.program — the local signing binary path.
//   - credential.helper — the local credential backend (must be osxkeychain,
//     never `store`; see CredentialHelperStore).
//   - credential.username — the account login name (account identity, not a
//     secret and not commit identity: kept plaintext-local, never routed to the
//     secret store). FullKey collapses the subsection, so this drops both a bare
//     credential.username AND a per-host credential.<host>.username.
var identityKeys = map[string]bool{
	"user.email":          true,
	"user.name":           true,
	"user.signingkey":     true,
	"gpg.program":         true,
	"credential.helper":   true,
	"credential.username": true,
}

// isIdentityKey reports whether a canonical dotted key (lowercased,
// "section.name") is machine identity that must live only in ~/.gitconfig.local.
func isIdentityKey(fullKey string) bool { return identityKeys[fullKey] }

// isIdentitySection reports whether a section's keys are ALL identity — so a
// header left with no non-identity keys after the partition is dropped whole,
// never left as an orphan `[user]` / `[gpg]` / `[credential]` block in the shared
// output. A [credential "https://host"] SUBSECTION is NOT treated as
// identity-only here (its non-helper keys — e.g. `provider`, `useHttpPath` — are
// shared), so only the bare, subsection-less credential section drops its header.
// Note the identity KEY `credential.helper` is dropped for EVERY host (FullKey
// collapses the subsection), the conservative direction: a per-host helper never
// reaches the shared repo even though its subsection header may remain.
func isIdentitySection(section, subsection string) bool {
	switch section {
	case "user", "gpg":
		return true
	case "credential":
		return subsection == ""
	}
	return false
}

// isIdentityBlockSection reports whether an ENTIRE section block routes to the
// local layer regardless of its keys: an [includeIf …] block carries per-
// directory identity (plan §3.1 / R8) and must never appear in the shared
// ~/.gitconfig. A plain [include] (ferry's own overlay, or a user's shared
// include) is NOT dropped.
func isIdentityBlockSection(section string) bool {
	return section == "includeif"
}

// SharedContent partitions a git-config's bytes into the SHARED representation:
// every identity key (user.email/name/signingkey, gpg.program,
// credential.helper) and every [includeIf …] block is dropped, and any section
// header left empty by that drop is dropped too. Everything else — aliases,
// core.*, init.defaultBranch, [url]/[http] ergonomics, unconditional [include],
// comments, blank lines, indentation — is preserved BYTE-FOR-BYTE (the shared
// output is a subsequence of the input's lines, each verbatim).
//
// This is the identity firewall enforced on the DEPLOY composition and the
// shared-capture write: even a git-config that mistakenly carries identity in the
// committed shared source can never deploy it into the shared ~/.gitconfig or be
// written back to the shared repo. On a config that already holds no identity it
// is a no-op and returns the input byte-for-byte (Reassemble(Parse(x)) == x with
// nothing dropped), so the git round-trip stays byte-stable.
func SharedContent(content []byte) []byte {
	lines := Parse(content)
	drop := identityDropSet(lines)
	kept := make([]Line, 0, len(lines))
	for i, l := range lines {
		if drop[i] {
			continue
		}
		kept = append(kept, l)
	}
	return Reassemble(kept)
}

// identityDropSet marks, per line index, whether the line belongs to the LOCAL
// (dropped-from-shared) identity partition: an identity key line, any line inside
// an [includeIf …] block, or a section header whose section becomes empty once
// its identity keys are removed. It is the single source of truth SharedContent
// partitions on (it keeps the un-marked lines). The machine identity layer itself
// is NOT reconstructed here — it comes from the local/git/gitconfig.local overlay
// the user maintains — so a "LocalContent" complement would be dead scaffolding.
func identityDropSet(lines []Line) map[int]bool {
	drop := make(map[int]bool, len(lines))

	// Group line indices by their owning section header (-1 = pre-section).
	headerAt := -1
	members := map[int][]int{}
	for i, l := range lines {
		if l.Kind == Section {
			headerAt = i
			members[i] = append(members[i], i)
			continue
		}
		members[headerAt] = append(members[headerAt], i)
	}

	for h, idxs := range members {
		if h < 0 {
			continue
		}
		hdr := lines[h]
		// An [includeIf …] block routes to local WHOLE — header, keys, and its
		// interleaved comment/blank lines.
		if isIdentityBlockSection(hdr.Section) {
			for _, i := range idxs {
				drop[i] = true
			}
			continue
		}
		// Otherwise drop identity KEY lines individually.
		droppedKV, keptKV := 0, 0
		for _, i := range idxs {
			l := lines[i]
			if l.Kind != KeyValue {
				continue
			}
			if isIdentityKey(l.FullKey()) {
				drop[i] = true
				droppedKV++
			} else {
				keptKV++
			}
		}
		// If the section is an identity-only section AND every kv it held was
		// dropped, drop the now-empty header (and its trailing blank/comment lines
		// that no longer front any kept key) so no orphan [user]/[gpg]/[credential]
		// block survives in the shared output.
		if keptKV == 0 && droppedKV > 0 && isIdentitySection(hdr.Section, hdr.Subsection) {
			for _, i := range idxs {
				if lines[i].Kind == Section || lines[i].Kind == Comment || lines[i].Kind == Blank {
					drop[i] = true
				}
			}
		}
	}
	return drop
}

// CredentialHelperStore reports whether content sets `credential.helper = store`
// — the backend that writes credentials as PLAINTEXT into ~/.git-credentials.
// ferry refuses to carry it (it must be `osxkeychain`); a caller warns and drops
// it. Detection is exact: only a real credential.helper KeyValue whose value's
// first token is `store` trips it, so a `credential.helper = osxkeychain` line or
// a helper named `store-something` is left alone.
func CredentialHelperStore(content []byte) bool {
	for _, l := range Parse(content) {
		if l.Kind != KeyValue || l.FullKey() != "credential.helper" {
			continue
		}
		if valueFirstToken(l.Raw) == "store" {
			return true
		}
	}
	return false
}

// valueFirstToken returns the first whitespace-delimited token of a KeyValue
// line's value (the bytes after the first '='), lowercased. It is used only to
// classify credential.helper's backend name.
func valueFirstToken(raw string) string {
	body := strings.TrimRight(raw, "\n")
	body = strings.TrimRight(body, "\r")
	eq := strings.IndexByte(body, '=')
	if eq < 0 {
		return ""
	}
	val := strings.TrimSpace(body[eq+1:])
	if val == "" {
		return ""
	}
	// Strip a surrounding quote pair so a quoted `helper = "store"` is recognised
	// too (security-review F4); an inner-token comparison follows.
	tok := strings.Fields(val)[0]
	tok = strings.Trim(tok, `"'`)
	return strings.ToLower(tok)
}
