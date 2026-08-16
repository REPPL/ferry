package secret

import "strings"

// IsPathBlockedFromRepo reports whether any COMPONENT of a forward-slash relative
// path is itself a high-confidence secret — a token used as a filename or a
// directory name. It is the path counterpart of IsBlockedFromRepo (which gates
// file CONTENT): a path is carried in manifests, refusal messages, and git output
// long after the file it names, so a secret-shaped component must be kept out of
// the repo just as a secret-shaped line is.
//
// Each component is scanned as an OPAQUE VALUE (GateValue), never the whole
// relative path at once: the separators and the ordinary components around a
// token dilute the entropy heuristic, so a whole-path scan would miss what a
// per-component scan catches.
//
// Bundle EXPORT and bundle IMPORT/validate both gate through this ONE function so
// their path predicates cannot drift — a path export withholds is a path import
// refuses.
func IsPathBlockedFromRepo(slash string) bool {
	for _, seg := range strings.Split(slash, "/") {
		if GateValue(seg).BlockedFromRepo {
			return true
		}
	}
	return false
}
