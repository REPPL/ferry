package plugin

import "strings"

// SecretSpanWindow locates the secret span inside raw by DIFFING raw against
// the extraction's Replacement (which is raw with exactly that span swapped
// for its placeholder): the 0-based line window [start, end) where the two
// disagree, computed as the longest common prefix and suffix of their line
// sequences. This is POSITIONAL — an earlier byte-equal occurrence of the
// secret value on another line never matches — and plugin-agnostic (no
// placeholder parsing). ok is false when replacement does not identify a
// non-empty window (e.g. Replacement == raw).
func SecretSpanWindow(raw, replacement []byte) (start, end int, ok bool) {
	rl := strings.Split(string(raw), "\n")
	pl := strings.Split(string(replacement), "\n")
	shorter := len(rl)
	if len(pl) < shorter {
		shorter = len(pl)
	}
	prefix := 0
	for prefix < shorter && rl[prefix] == pl[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < shorter-prefix && rl[len(rl)-1-suffix] == pl[len(pl)-1-suffix] {
		suffix++
	}
	start, end = prefix, len(rl)-suffix
	if start >= end {
		return 0, 0, false
	}
	return start, end, true
}

// DropSecretSpans removes from raw the span lines each Replacement identifies
// (SecretSpanWindow against the SAME original raw), in descending window
// order so removals cannot shift one another — the wizard-layer edit backing
// the Drop route on a block's SecretLine findings. A window that cannot be
// located (or overlaps an already-removed one) is skipped: the un-dropped
// secret then fails safe at the pre-mutation re-gate. If the block is left
// with no non-blank content it empties entirely (Raw -> nil), honouring the
// never-add/remove/reorder invariant (emptied, not deleted).
func DropSecretSpans(raw []byte, replacements [][]byte) []byte {
	type window struct{ start, end int }
	var wins []window
	for _, repl := range replacements {
		if s, e, ok := SecretSpanWindow(raw, repl); ok {
			wins = append(wins, window{start: s, end: e})
		}
	}
	if len(wins) == 0 {
		return raw
	}
	// Descending, non-overlapping removal (selection sort keeps this tiny).
	for i := 0; i < len(wins); i++ {
		for j := i + 1; j < len(wins); j++ {
			if wins[j].start > wins[i].start {
				wins[i], wins[j] = wins[j], wins[i]
			}
		}
	}
	lines := strings.Split(string(raw), "\n")
	removedBelow := len(lines) + 1
	for _, w := range wins {
		if w.end > removedBelow {
			continue // overlaps a removed window: skip (fail safe at the re-gate)
		}
		lines = append(lines[:w.start], lines[w.end:]...)
		removedBelow = w.start
	}
	out := strings.Join(lines, "\n")
	if strings.TrimSpace(out) == "" {
		return nil
	}
	return []byte(out)
}
