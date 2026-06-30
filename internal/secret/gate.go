package secret

// Route is a capture-time routing destination for an accepted change.
type Route string

const (
	// RouteShared writes the change to the committed repo (shared across machines).
	RouteShared Route = "shared"
	// RouteLocal writes the change to local/<domain>/ — gitignored, but still
	// plaintext on disk INSIDE the repo worktree.
	RouteLocal Route = "local"
	// RouteReject discards the change.
	RouteReject Route = "reject"
	// RouteSecretStore sends the value to the out-of-repo secret store
	// (~/.config/ferry/secrets-local), never under the repo.
	RouteSecretStore Route = "secret-store"
)

// GateDecision is the capture gate's verdict on a candidate change.
type GateDecision struct {
	// Findings is everything the scanner detected (may be empty).
	Findings Findings
	// BlockedFromRepo is true when a high-confidence secret was found: the change
	// must NOT be routed to shared OR local (both are in the repo worktree).
	BlockedFromRepo bool
	// AllowedRoutes is the set of routes the capture path may offer the user. For
	// a blocked change this is {reject, secret-store} only; otherwise it is the
	// normal {shared, local, reject}.
	AllowedRoutes []Route
}

// GateText runs the text scanner over a candidate change and returns the routing
// decision. Use this for text domains (dotfiles, wg .conf, shell rc).
func GateText(content string) GateDecision {
	return decide(ScanText(content))
}

// GateValue runs the whole-value scanner over an opaque candidate value and
// returns the routing decision. Use this for binary/opaque domains (plist).
func GateValue(value string) GateDecision {
	return decide(ScanValue(value))
}

// decide turns findings into a routing decision. A high-confidence finding
// blocks BOTH shared and local — because local/ lives inside the repo worktree,
// routing a secret there still violates "secrets never in the repo". The only
// remaining routes are reject and the out-of-repo secret store.
func decide(fs Findings) GateDecision {
	if fs.HasHigh() {
		return GateDecision{
			Findings:        fs,
			BlockedFromRepo: true,
			AllowedRoutes:   []Route{RouteReject, RouteSecretStore},
		}
	}
	return GateDecision{
		Findings:        fs,
		BlockedFromRepo: false,
		AllowedRoutes:   []Route{RouteShared, RouteLocal, RouteReject},
	}
}

// IsBlockedFromRepo reports whether content contains a high-confidence secret
// and so must be kept out of BOTH repo routes (shared and local). It is the
// convenience predicate for the capture path; GateText/GateValue return the
// fuller decision with findings and allowed routes.
func IsBlockedFromRepo(content string) bool {
	return ScanText(content).HasHigh()
}
