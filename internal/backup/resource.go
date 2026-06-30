package backup

import "fmt"

// Resource is the hook by which a non-file managed resource — notably a macOS
// preference DOMAIN (plist / `defaults`) — plugs its own transaction into the
// engine. Such domains are mutated with `defaults import/export`, not atomic
// file renames, so the file-transaction core cannot cover them; instead each
// domain implements this interface and the engine drives it during scoped
// restore (and, in Wave 2, during apply).
//
// Implementations live alongside the macOS domain code in a later wave; this
// package defines the contract only and does NOT implement defaults/plist.
//
// Contract:
//   - Domain() is the stable scope name (e.g. "com.googlecode.iterm2") used by
//     ScopedRestore and by Register.
//   - Backup captures the resource's CURRENT state into an opaque, restorable
//     blob (e.g. the bytes of `defaults export <domain> -`). It must run BEFORE
//     the resource is mutated. A resource that does NOT yet exist pre-ferry (a
//     fresh machine where the domain was never configured) reports absent=true
//     with a nil blob — a normal expected state, NOT an error — so the engine
//     records an absent baseline (analogous to KindAbsent for files) and apply
//     can still proceed to create it. err is reserved for real failures.
//   - Restore returns the resource to a previously captured state. When absent
//     is true the baseline recorded the resource as not existing pre-ferry, so
//     Restore REMOVES/clears it (e.g. `defaults delete <domain>`) rather than
//     importing a blob; when absent is false it re-applies blob (e.g.
//     `defaults import`).
type Resource interface {
	Domain() string
	Backup() (blob []byte, absent bool, err error)
	Restore(blob []byte, absent bool) error
}

// ResourcePath maps a preference domain to the synthetic store "path" used to
// key its baseline/journal/snapshot blobs. It is deliberately NOT an absolute
// filesystem path (the file-restore code never touches it); the engine routes a
// resource's restore through its Resource.Restore hook, not restoreState. The
// "resource://" scheme cannot collide with a real absolute path (those start with
// "/"), so resource and file entries never share a store key. Exported so cmd/
// can pass a resource into ScopedRestore alongside file paths.
func ResourcePath(domain string) string {
	return "resource://" + domain
}

// isResourcePath reports whether a stored PathState belongs to a registered
// resource domain rather than a real file path.
func isResourcePath(p string) bool {
	const scheme = "resource://"
	return len(p) > len(scheme) && p[:len(scheme)] == scheme
}

// domainForResourcePath returns the domain encoded in a resource store path.
func domainForResourcePath(p string) string {
	return p[len("resource://"):]
}

// BackupResource captures the CURRENT state of the registered resource for the
// given domain (via Resource.Backup) into the immutable baseline-if-first and
// into this run's journal, symmetrically with BackupAndWrite for files. Call it
// under the apply lock, within a Begin/Commit run, BEFORE the resource is
// mutated. The captured blob is stored the same secret-safe way as file blobs.
func (e *Engine) BackupResource(r *run, domain string) error {
	if r == nil {
		return ErrNilRun
	}
	res, ok := e.resources[domain]
	if !ok {
		return fmt.Errorf("backup: no resource registered for domain %q", domain)
	}
	blob, absent, err := res.Backup()
	if err != nil {
		return err
	}
	// An absent resource records a KindAbsent baseline — "this domain did not
	// exist pre-ferry" — exactly like an absent file path. Restore then REMOVES
	// the domain to return to that state. Apply is NOT aborted: absence is the
	// expected state on a fresh machine, not a Backup failure.
	state := PathState{Path: ResourcePath(domain), Kind: KindAbsent}
	if !absent {
		state.Kind = KindFile
		state.Mode = filePerm
		state.HasBlob = true
	}
	// (1) Immutable baseline — write-once true pre-ferry resource state.
	if !e.HasBaseline(state.Path) {
		if err := e.writeBaseline(state, blob); err != nil {
			return err
		}
	}
	// (2) Per-run journal — record prior state + the "resource" action so an
	// incomplete run's rollback re-applies it through the Resource hook.
	return r.record(state, blob, "resource")
}
