package registry

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// entry is a minimal resolved-entry type standing in for a real registry entry
// (e.g. a Harness) so these tests exercise Resolve directly, not via consumers.
type entry struct {
	name string
	val  string
}

// spec is a minimal user-declaration type: an override for val, where the empty
// string means "leave unchanged" (mirroring how real overlays treat unset
// fields).
type spec struct {
	val string
}

func nameOf(e entry) string { return e.name }

// overlay applies one spec to the current entry. A new name (exists == false)
// starts from a zero entry carrying its name; a set val overrides; an entry
// that ends up with no val is a config error, mirroring the required-field rule
// real registries enforce.
func overlay(cur entry, exists bool, name string, s spec) (entry, error) {
	if !exists {
		cur = entry{name: name}
	}
	if s.val != "" {
		cur.val = s.val
	}
	if cur.val == "" {
		return entry{}, fmt.Errorf("%s: val is required", name)
	}
	return cur, nil
}

func unknown(name string) error {
	return fmt.Errorf("selection names %q, which is unknown", name)
}

func builtins() []entry {
	return []entry{
		{name: "alpha", val: "a"},
		{name: "bravo", val: "b"},
		{name: "charlie", val: "c"},
	}
}

// resolve is a thin, fully-typed wrapper so each test reads as a table row
// rather than a seven-argument call.
func resolve(decls map[string]spec, selection []string, selectionSet bool) ([]entry, error) {
	return Resolve(builtins(), nameOf, decls, overlay, selection, selectionSet, unknown)
}

func TestResolveBuiltinsOnly(t *testing.T) {
	got, err := resolve(nil, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := builtins()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("built-ins in registry order\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveOverrideBuiltinKeepsPosition(t *testing.T) {
	// Overriding a built-in updates it field-by-field in place: bravo keeps its
	// second slot, only its val changes; the other built-ins are untouched.
	got, err := resolve(map[string]spec{"bravo": {val: "B!"}}, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []entry{
		{name: "alpha", val: "a"},
		{name: "bravo", val: "B!"},
		{name: "charlie", val: "c"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override in place\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveEmptySpecLeavesBuiltinUnchanged(t *testing.T) {
	// A declaration that sets no fields overlays without changing anything: the
	// built-in survives because it already satisfies the required-field rule.
	got, err := resolve(map[string]spec{"alpha": {}}, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, builtins()) {
		t.Fatalf("empty spec should leave built-ins unchanged, got: %v", got)
	}
}

func TestResolveNewEntriesAppendedSortedByName(t *testing.T) {
	// New names are appended after every built-in and ordered by name,
	// deterministically, regardless of map iteration order.
	decls := map[string]spec{
		"zulu":  {val: "z"},
		"delta": {val: "d"},
		"echo":  {val: "e"},
	}
	got, err := resolve(decls, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []entry{
		{name: "alpha", val: "a"},
		{name: "bravo", val: "b"},
		{name: "charlie", val: "c"},
		{name: "delta", val: "d"},
		{name: "echo", val: "e"},
		{name: "zulu", val: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("new entries appended sorted\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveOverrideAndAddTogether(t *testing.T) {
	// A mix of overriding a built-in and adding a new name: the override stays
	// in place, the addition lands after the built-ins.
	decls := map[string]spec{
		"charlie": {val: "C!"},
		"delta":   {val: "d"},
	}
	got, err := resolve(decls, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []entry{
		{name: "alpha", val: "a"},
		{name: "bravo", val: "b"},
		{name: "charlie", val: "C!"},
		{name: "delta", val: "d"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override + add\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveNewEntryMissingRequiredFieldErrors(t *testing.T) {
	// Adding an unknown name whose spec sets no val trips the overlay's
	// required-field rule; the error propagates unchanged.
	_, err := resolve(map[string]spec{"delta": {}}, nil, false)
	if err == nil {
		t.Fatal("expected required-field error, got nil")
	}
	if want := "delta: val is required"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestResolveOverlayErrorHaltsResolution(t *testing.T) {
	// An overlay error is returned immediately with a nil entry slice, not
	// swallowed or partially applied.
	got, err := resolve(map[string]spec{"delta": {}, "echo": {}}, nil, false)
	if err == nil {
		t.Fatal("expected overlay error, got nil")
	}
	if got != nil {
		t.Fatalf("entries should be nil on error, got: %v", got)
	}
}

func TestResolveSelectionRestrictsAndReorders(t *testing.T) {
	// An explicit selection both trims the set and imposes its own order,
	// overriding the natural built-in order.
	got, err := resolve(nil, []string{"charlie", "alpha"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []entry{
		{name: "charlie", val: "c"},
		{name: "alpha", val: "a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection restrict+reorder\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveSelectionCanNameNewEntry(t *testing.T) {
	// A selection may name a user-added entry, not only built-ins.
	got, err := resolve(map[string]spec{"delta": {val: "d"}}, []string{"delta", "bravo"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []entry{
		{name: "delta", val: "d"},
		{name: "bravo", val: "b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection of new entry\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveSelectionUnknownNameErrors(t *testing.T) {
	// A selection naming an entry that is neither a built-in nor declared is a
	// config error routed through the unknown callback, not a silent no-op.
	_, err := resolve(nil, []string{"alpha", "missing"}, true)
	if err == nil {
		t.Fatal("expected unknown-selection error, got nil")
	}
	if want := unknown("missing").Error(); err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestResolveEmptySelectionYieldsEmptySet(t *testing.T) {
	// An explicit but empty selection (selectionSet true, no names) restricts
	// the registry to nothing — distinct from an unset selection, which keeps
	// every entry.
	got, err := resolve(nil, []string{}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty selection should yield no entries, got: %v", got)
	}
}

func TestResolveUnsetSelectionKeepsAllBuiltins(t *testing.T) {
	// The selectionSet flag, not the slice's emptiness, decides restriction: a
	// nil selection with selectionSet false keeps the full set.
	got, err := resolve(nil, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, builtins()) {
		t.Fatalf("unset selection should keep all built-ins, got: %v", got)
	}
}

func TestResolveNoBuiltinsAllUserDefined(t *testing.T) {
	// With no built-ins at all, the registry is entirely user-defined and still
	// resolves in sorted-name order.
	decls := map[string]spec{
		"yankee": {val: "y"},
		"xray":   {val: "x"},
	}
	got, err := Resolve(nil, nameOf, decls, overlay, nil, false, unknown)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []entry{
		{name: "xray", val: "x"},
		{name: "yankee", val: "y"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("all-user-defined\n got: %v\nwant: %v", got, want)
	}
}

func TestResolveEmptyRegistryUnsetSelection(t *testing.T) {
	// No built-ins and no declarations with no selection resolves to the empty
	// set without error.
	got, err := Resolve(nil, nameOf, nil, overlay, nil, false, unknown)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty set, got: %v", got)
	}
}

func TestResolveSelectionErrorTakesPriorityOverOrder(t *testing.T) {
	// The unknown error fires on the first offending selection name; sanity-check
	// it is the callback's error value that surfaces.
	sentinel := errors.New("sentinel")
	_, err := Resolve(builtins(), nameOf, nil, overlay, []string{"nope"}, true,
		func(string) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}
