package ghcli

import "regexp"

// redact masks credential-shaped substrings in EXTERNAL gh/git output (stdout or
// stderr) BEFORE it is spliced into any error ferry returns or message it prints.
// gh/git normally never print a token, but a future/edge version could emit one on
// stderr (a token, an Authorization/Bearer header, or a credentialed URL); ferry
// must never surface it. This is a defensive scrub, not a parser: it errs toward
// over-masking. Apply it to ALL external stdout/stderr at the boundary.
func redact(s string) string {
	s = tokenRe.ReplaceAllString(s, "***")
	s = authHeaderRe.ReplaceAllString(s, "Authorization: ***")
	s = bearerRe.ReplaceAllString(s, "$1 ***")
	s = userinfoRe.ReplaceAllString(s, "$1://***@")
	return s
}

var (
	// tokenRe matches GitHub token prefixes plus the following token characters.
	// Covers ghp_/gho_/ghs_/ghu_/ghr_ (classic scoped) and github_pat_ (fine-grained).
	tokenRe = regexp.MustCompile(`(?:gh[porsu]_|github_pat_)[A-Za-z0-9_]+`)

	// authHeaderRe masks EVERYTHING after an "Authorization:" header up to end of
	// line — the value may itself be "Bearer <token>" or "token <token>", so we
	// consume the whole remainder rather than a single word (which would leave the
	// token behind the scheme keyword).
	authHeaderRe = regexp.MustCompile(`(?i)authorization:\s*[^\r\n]*`)

	// bearerRe masks the credential after a standalone Bearer/token scheme keyword
	// (not preceded by an Authorization: header, which authHeaderRe already ate),
	// keeping the keyword ($1) so the shape stays legible.
	bearerRe = regexp.MustCompile(`(?i)\b(bearer|token)\s+\S+`)

	// userinfoRe masks userinfo in a URL: scheme://user[:pass]@host -> scheme://***@host.
	// This catches a credential embedded in a clone/remote URL on stderr.
	userinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*)://[^/\s@]+@`)
)
