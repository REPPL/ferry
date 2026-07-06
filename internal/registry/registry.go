// Package registry holds the one generic registry resolver ferry's data-driven
// registries share: a built-in default set, overlaid with the user's per-name
// declarations, then optionally restricted (and ordered) by a selection list.
//
// The agents domain resolves its harness and asset-mapping registries through
// it, and the config-file terminal domain resolves its terminal registry the
// same way — so "built-ins, then user additions sorted by name, or exactly the
// declared selection order" means one thing across every registry, defined once.
package registry

import "sort"

// Resolve computes a registry's effective, ordered entry set: the built-in
// entries, overlaid with the caller's per-name declarations in sorted-name
// order (an existing name is overridden field by field via overlay; a new name
// is appended), then filtered by the selection list when one is declared.
//
// Order is deterministic: built-ins first, then user-defined additions sorted
// by name — or exactly the declared selection order when selectionSet is true.
// overlay applies one user spec to the current entry (exists reports whether a
// built-in/prior entry of that name is being overridden) and enforces the
// registry's required-field rule; unknown formats the selection-not-found error
// for a selection naming an entry that is neither a built-in nor declared.
//
// T is the resolved entry type (e.g. a Harness); S is the user declaration type
// (e.g. a config.AgentsHarness).
func Resolve[T any, S any](
	builtins []T,
	nameOf func(T) string,
	decls map[string]S,
	overlay func(cur T, exists bool, name string, spec S) (T, error),
	selection []string,
	selectionSet bool,
	unknown func(name string) error,
) ([]T, error) {
	byName := map[string]T{}
	var order []string
	for _, b := range builtins {
		n := nameOf(b)
		byName[n] = b
		order = append(order, n)
	}

	names := make([]string, 0, len(decls))
	for n := range decls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		cur, exists := byName[n]
		if !exists {
			order = append(order, n)
		}
		merged, err := overlay(cur, exists, n, decls[n])
		if err != nil {
			return nil, err
		}
		byName[n] = merged
	}

	if !selectionSet {
		out := make([]T, 0, len(order))
		for _, n := range order {
			out = append(out, byName[n])
		}
		return out, nil
	}

	// An explicit selection restricts (and orders) the set; naming an unknown
	// entry is a config error, not a silent no-op.
	out := make([]T, 0, len(selection))
	for _, n := range selection {
		e, ok := byName[n]
		if !ok {
			return nil, unknown(n)
		}
		out = append(out, e)
	}
	return out, nil
}
