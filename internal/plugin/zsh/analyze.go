package zsh

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/plugin"
	"github.com/REPPL/ferry/internal/secret"
)

// Analyze returns secret / machine-specific / repairable findings over blocks,
// in deterministic order: SecretLine findings first (block order), then
// MachineSpecific (block order), then the repairables (block order, line order
// within a block). Secret KEY UNIQUENESS is resolved here (deterministic _2/_3
// suffixing) BEFORE Replacement is built, so placeholder and Put ref can never
// diverge (Codex r2-M1).
func (p *Plugin) Analyze(blocks []plugin.Block) []plugin.Finding {
	var out []plugin.Finding

	// --- SecretLine findings (always-on routing, never behind --repair). ---
	usedKeys := map[string]int{}
	for i, b := range blocks {
		for _, sp := range secretSpansInBlock(b) {
			key := uniqueKey(usedKeys, sp.key)
			ref := p.Domain() + "." + key
			out = append(out, plugin.Finding{
				Kind:   plugin.SecretLine,
				Block:  i,
				Detail: fmt.Sprintf("secret-shaped content (%s) — never committed; route to the secret store or drop it", sp.detail),
				Routes: []plugin.Route{plugin.SecretStore, plugin.Drop},
				Secret: &plugin.SecretExtraction{
					Key:         key,
					Value:       sp.value,
					Replacement: swapSpanAt(b.Raw, sp, secret.Placeholder(ref)),
				},
			})
		}
	}

	// --- MachineSpecific routing suggestions (declinable; Local suggested). ---
	for i, b := range blocks {
		if reason, ok := machineSpecific(b.Raw); ok {
			out = append(out, plugin.Finding{
				Kind:   plugin.MachineSpecific,
				Block:  i,
				Detail: fmt.Sprintf("machine-specific content (%s) — suggested route: local sidecar", reason),
				Routes: []plugin.Route{plugin.Local, plugin.Shared, plugin.Drop},
			})
		}
	}

	// --- Repairables (opt-in via --repair). ---
	for _, op := range p.repairOps(blocks) {
		out = append(out, plugin.Finding{
			Kind:       op.kind,
			Block:      op.block,
			Detail:     op.detail,
			Suggested:  op.suggested,
			Repairable: true,
		})
	}
	return out
}

// ApplyRepairs is the SINGLE WRITER for block text: it applies ALL accepted
// findings — SecretLine placeholder substitutions AND repairs — in one
// deterministic pass (ascending block, then line order). It never adds,
// removes, or reorders blocks; a full-block removal empties Raw instead.
func (p *Plugin) ApplyRepairs(blocks []plugin.Block, accepted []plugin.Finding) ([]plugin.Block, error) {
	out := make([]plugin.Block, len(blocks))
	copy(out, blocks)

	// Pass 1 — SECRET substitutions, POSITIONALLY: per block, each accepted
	// SecretLine finding is matched (by value, consumed in span order) to its
	// re-derived span and swapped AT that span's line range, in DESCENDING
	// line order so earlier spans stay valid. First-occurrence byte matching
	// is never used: an earlier byte-equal occurrence of the value (e.g. an
	// alias echoing it) is untouched.
	//
	// REACHABILITY ASSUMPTION (pinned by TestApplyRepairsIdenticalSecretsPerBlockAssumption):
	// every shipping surface routes secrets PER BLOCK — all of a block's
	// SecretLine findings are accepted together or not at all — so the
	// value-order matching below never has to disambiguate a PARTIAL
	// acceptance among byte-identical values in one block. If a future
	// per-finding secret UI accepts only one of two identical-value findings,
	// the FIRST span is patched (occurrence order), the remainder keeps its
	// raw value, and the pre-mutation re-gate aborts: fails SAFE, but such a
	// UI must add per-finding span identity before shipping.
	secretsByBlock := map[int][]plugin.Finding{}
	var repairsInOrder []plugin.Finding
	for _, f := range accepted {
		if f.Block < 0 || f.Block >= len(out) {
			return nil, fmt.Errorf("zsh: finding block index %d out of range (%d blocks)", f.Block, len(out))
		}
		switch f.Kind {
		case plugin.SecretLine:
			if f.Secret == nil {
				return nil, fmt.Errorf("zsh: SecretLine finding for block %d carries no extraction", f.Block)
			}
			secretsByBlock[f.Block] = append(secretsByBlock[f.Block], f)
		case plugin.MachineSpecific:
			// Pure routing — no text edit.
		default:
			repairsInOrder = append(repairsInOrder, f)
		}
	}
	blockIdxs := make([]int, 0, len(secretsByBlock))
	for i := range secretsByBlock {
		blockIdxs = append(blockIdxs, i)
	}
	sort.Ints(blockIdxs)
	for _, i := range blockIdxs {
		spans := secretSpansInBlock(out[i])
		type patch struct {
			sp  secretSpan
			ref string
		}
		var patches []patch
		used := make([]bool, len(spans))
		for _, f := range secretsByBlock[i] {
			matched := false
			for si, sp := range spans {
				if !used[si] && sp.value == f.Secret.Value {
					used[si] = true
					patches = append(patches, patch{sp: sp, ref: p.Domain() + "." + f.Secret.Key})
					matched = true
					break
				}
			}
			if !matched {
				return nil, fmt.Errorf("zsh: no matching secret span in block %d for the accepted finding (key %s)", i, f.Secret.Key)
			}
		}
		sort.Slice(patches, func(a, b int) bool { return patches[a].sp.startLine > patches[b].sp.startLine })
		for _, pt := range patches {
			out[i].Raw = swapSpanAt(out[i].Raw, pt.sp, secret.Placeholder(pt.ref))
		}
	}

	// Pass 2 — repairs, LINE-ANCHORED per finding (consent is per finding:
	// a declined second finding of the same kind in the same block must stay
	// byte-verbatim). Ops are recomputed on the POST-substitution blocks, so
	// their line anchors reflect the current text (a collapsed PEM span above
	// a repair line shifts it; the recompute re-anchors); each accepted
	// finding is matched to its own op, and a block's matched ops apply in
	// DESCENDING line order so removals cannot shift one another.
	ops := p.repairOps(out)
	matchedByBlock := map[int][]*repairOp{}
	for _, f := range repairsInOrder {
		op, ok := takeOpForFinding(ops, f)
		if !ok {
			return nil, fmt.Errorf("zsh: no %s repair detected on block %d matching the accepted finding", f.Kind, f.Block)
		}
		matchedByBlock[f.Block] = append(matchedByBlock[f.Block], op)
	}
	repairBlocks := make([]int, 0, len(matchedByBlock))
	for i := range matchedByBlock {
		repairBlocks = append(repairBlocks, i)
	}
	sort.Ints(repairBlocks)
	for _, i := range repairBlocks {
		blockOps := matchedByBlock[i]
		sort.Slice(blockOps, func(a, b int) bool { return blockOps[a].line > blockOps[b].line })
		for _, op := range blockOps {
			out[i].Raw = applyRepairOp(out[i].Raw, op)
		}
	}
	return out, nil
}

// --- secret extraction -----------------------------------------------------

// secretSpan is one extraction candidate within a block, ANCHORED to its line
// range: every edit (substitution, drop) is positional — never a
// first-byte-occurrence match, which an earlier byte-equal occurrence (e.g.
// an alias echoing the same value) would hijack.
type secretSpan struct {
	key        string // proposed (pre-dedupe) key
	value      string // the exact span bytes (single line, RHS, or PEM run)
	assignment bool   // true when value is an assignment RHS
	detail     string
	startLine  int // 1-based first line of the span WITHIN the block
	endLine    int // 1-based last line (== startLine except for PEM runs)
}

// assignmentLineRe matches a shell assignment line (`VAR=...` / `export VAR=...`)
// with NO spaces around '=' — the shape whose RHS is swappable in place.
var assignmentLineRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.+)$`)

// pemEndRe matches a PEM END line, closing the BEGIN..END secret span.
var pemEndRe = regexp.MustCompile(`(?i)END(?:\s+[A-Z0-9]+)*\s+PRIVATE\s+KEY`)

// secretSpansInBlock finds every secret span in a block via ferry's REAL gate
// detection (secret.ScanText) and extracts span-grained values: an
// assignment-shaped line yields its RHS keyed by the sanitized var name; PEM
// material widens to the contiguous BEGIN..END run under a positional key;
// any other flagged line yields the single line under a positional key.
// Findings inside an already-extracted span are suppressed (one span, one ref).
func secretSpansInBlock(b plugin.Block) []secretSpan {
	text := string(b.Raw)
	findings := secret.ScanText(text)
	if !findings.HasHigh() {
		return nil
	}
	lines := strings.Split(text, "\n")
	covered := map[int]bool{}
	var out []secretSpan
	for _, f := range findings {
		if f.Confidence != secret.High || f.Line < 1 || f.Line > len(lines) || covered[f.Line] {
			continue
		}
		line := lines[f.Line-1]
		absLine := b.Start + f.Line - 1
		if f.Rule == "pem-private-key" {
			end := f.Line
			for j := f.Line; j <= len(lines); j++ {
				end = j
				if pemEndRe.MatchString(lines[j-1]) {
					break
				}
			}
			for j := f.Line; j <= end; j++ {
				covered[j] = true
			}
			out = append(out, secretSpan{
				key:       fmt.Sprintf("secret_%d", absLine),
				value:     strings.Join(lines[f.Line-1:end], "\n"),
				detail:    "private-key block",
				startLine: f.Line,
				endLine:   end,
			})
			continue
		}
		covered[f.Line] = true
		if m := assignmentLineRe.FindStringSubmatch(line); m != nil && f.Rule != "wireguard-key" {
			out = append(out, secretSpan{
				key:        sanitizeKey(m[1]),
				value:      m[2],
				assignment: true,
				detail:     "credential assignment (" + strings.ToLower(m[1]) + ")",
				startLine:  f.Line,
				endLine:    f.Line,
			})
			continue
		}
		out = append(out, secretSpan{
			key:       fmt.Sprintf("secret_%d", absLine),
			value:     line,
			detail:    f.Detail,
			startLine: f.Line,
			endLine:   f.Line,
		})
	}
	return out
}

// sanitizeKey lowercases a var name into a store key ([a-z0-9_] only).
func sanitizeKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "secret"
	}
	return b.String()
}

// uniqueKey suffixes duplicate keys deterministically (key, key_2, key_3, ...).
func uniqueKey(used map[string]int, key string) string {
	used[key]++
	if used[key] == 1 {
		return key
	}
	return fmt.Sprintf("%s_%d", key, used[key])
}

// swapSpanAt replaces EXACTLY the span's line range in raw with the
// replacement text — positional, never a first-byte-occurrence match (an
// earlier byte-equal occurrence, e.g. an alias echoing the same value on a
// line above, is left untouched). An assignment span swaps only the RHS of
// its own line; any other span collapses its whole line range onto a single
// replacement line. Trailing separators (the last span line's newline, or its
// absence at block end) are preserved.
func swapSpanAt(raw []byte, sp secretSpan, replacement string) []byte {
	segs := splitKeepEnds(raw)
	if sp.startLine < 1 || sp.endLine < sp.startLine || sp.endLine > len(segs) {
		return raw
	}
	if sp.assignment {
		body, term := cutLineEnd(segs[sp.startLine-1])
		eq := strings.IndexByte(body, '=')
		if eq < 0 {
			return raw
		}
		var out []byte
		for i, seg := range segs {
			if i == sp.startLine-1 {
				out = append(out, body[:eq+1]...)
				out = append(out, replacement...)
				out = append(out, term...)
				continue
			}
			out = append(out, seg...)
		}
		return out
	}
	_, term := cutLineEnd(segs[sp.endLine-1])
	var out []byte
	for i := 0; i < sp.startLine-1; i++ {
		out = append(out, segs[i]...)
	}
	out = append(out, replacement...)
	out = append(out, term...)
	for i := sp.endLine; i < len(segs); i++ {
		out = append(out, segs[i]...)
	}
	return out
}

// cutLineEnd splits a splitKeepEnds segment into its body and its trailing
// "\n" terminator ("" when the segment ends the block without one).
func cutLineEnd(seg []byte) (body string, term string) {
	s := string(seg)
	if strings.HasSuffix(s, "\n") {
		return s[:len(s)-1], "\n"
	}
	return s, ""
}

// --- machine-specific ------------------------------------------------------

// machineMarkers are substrings that mark a block machine-specific (portability
// concern, not security): Homebrew prefixes, absolute tool paths, hostname use.
var machineMarkers = []struct{ marker, reason string }{
	{"/opt/homebrew", "Homebrew prefix (Apple Silicon)"},
	{"/usr/local/Homebrew", "Homebrew prefix (Intel)"},
	{"brew shellenv", "Homebrew shell environment"},
	{"$(hostname", "hostname-dependent value"},
	{"scutil --get", "macOS host identity"},
	{"/Volumes/", "mounted-volume path"},
}

func machineSpecific(raw []byte) (string, bool) {
	s := string(raw)
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		for _, m := range machineMarkers {
			if strings.Contains(t, m.marker) {
				return m.reason, true
			}
		}
	}
	return "", false
}

// --- repairs ----------------------------------------------------------------

// repairOp is one detected repair, ANCHORED to its own line: consent is
// per-finding, so accepting one op edits exactly its line — a declined second
// finding of the same kind in the same block stays byte-verbatim ("Declined =
// untouched"). kind+block+detail+suggested identify the op against a Finding.
type repairOp struct {
	kind      plugin.FindingKind
	block     int
	line      int // 0-based in-block line this op edits/removes
	detail    string
	suggested string
	remove    bool // remove the line (dup-path/dead-source) vs in-line edit (hardcoded-home)
	taken     bool
}

// homePathRe matches a hardcoded home directory path (/Users/<name> or
// /home/<name>) — the de-hardcode repair swaps it for $HOME.
var homePathRe = regexp.MustCompile(`(/Users|/home)/[A-Za-z0-9._-]+`)

// repairOps detects the repairable findings over blocks, in block order (and
// line order within a block): hardcoded-home, duplicate PATH export, dead
// `source`. The same detection backs Analyze (findings) and ApplyRepairs
// (edits), so they cannot diverge.
func (p *Plugin) repairOps(blocks []plugin.Block) []*repairOp {
	var ops []*repairOp
	seenPathLines := map[string]bool{}
	secretLines := map[int]map[int]bool{} // block -> 0-based in-block line -> covered by a secret span
	for i, b := range blocks {
		for _, sp := range secretSpansInBlock(b) {
			if secretLines[i] == nil {
				secretLines[i] = map[int]bool{}
			}
			for j := sp.startLine; j <= sp.endLine; j++ {
				secretLines[i][j-1] = true
			}
		}
	}

	for i, b := range blocks {
		lines := strings.Split(string(b.Raw), "\n")
		for li, l := range lines {
			t := strings.TrimSpace(l)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if secretLines[i][li] {
				continue // a secret line is routed, never line-repaired
			}
			// Hardcoded home path -> $HOME (in-line edit on THIS line only).
			// Identity: Detail quotes the ORIGINAL UNTRIMMED line — two
			// DIFFERENT originals that normalize to the SAME repaired form
			// (`/Users/x/dev` vs `/home/x/dev` both suggesting `$HOME/dev`)
			// stay distinct findings; Suggested carries the full repaired line
			// untrimmed; the trailing "(line N)" is display-only.
			if homePathRe.MatchString(l) {
				repaired := homePathRe.ReplaceAllString(l, "$$HOME")
				ops = append(ops, &repairOp{
					kind:      plugin.HardcodedHome,
					block:     i,
					line:      li,
					detail:    fmt.Sprintf("hardcoded home directory path in `%s` — portable form uses $HOME (line %d)", l, b.Start+li),
					suggested: repaired,
				})
				continue
			}
			// Duplicate PATH export (exact line seen before): remove THIS line.
			// Identity: Detail quotes the ORIGINAL UNTRIMMED line, so two
			// duplicate lines differing only in indentation are distinct.
			if strings.Contains(t, "PATH=") && (strings.HasPrefix(t, "export ") || strings.HasPrefix(t, "PATH=")) {
				if seenPathLines[t] {
					ops = append(ops, &repairOp{
						kind:   plugin.DuplicatePath,
						block:  i,
						line:   li,
						detail: fmt.Sprintf("duplicate PATH export `%s` — an identical line appears earlier; the first occurrence is kept (line %d)", l, b.Start+li),
						remove: true,
					})
				} else {
					seenPathLines[t] = true
				}
				continue
			}
			// Dead `source <target>` whose target is absent: remove THIS line.
			// Identity: the ORIGINAL UNTRIMMED line, like the other repairs.
			if target, ok := sourceTarget(t); ok {
				if resolved, ok := p.expandPath(target); ok {
					if _, err := os.Lstat(resolved); os.IsNotExist(err) {
						ops = append(ops, &repairOp{
							kind:   plugin.DeadSource,
							block:  i,
							line:   li,
							detail: fmt.Sprintf("`%s` sources a file that does not exist (line %d)", l, b.Start+li),
							remove: true,
						})
					}
				}
			}
		}
	}
	return ops
}

// repairLineSuffixRe strips the user-facing " (line N)" tail this package
// appends to every repair Detail. The LINE NUMBER is display identity only:
// the wizard's pre-edits (secret-line drops, PEM-span collapses, dropped-block
// emptying) can SHIFT lines between Analyze and ApplyRepairs, so op matching
// compares the CONTENT identity — the detail minus the line suffix, plus the
// untrimmed Suggested — which those edits never touch (they only affect
// secret lines and whole dropped blocks, never a repair line).
var repairLineSuffixRe = regexp.MustCompile(` \(line \d+\)$`)

func repairCoreDetail(detail string) string {
	return repairLineSuffixRe.ReplaceAllString(detail, "")
}

// takeOpForFinding claims the first unclaimed op matching the finding's
// CONTENT identity (kind, block, detail-minus-line-suffix, suggested). This is
// per-finding, ordinal-among-ties consent: findings that tie on the full
// content identity are byte-identical lines, for which either op yields
// byte-identical output — while two DIFFERENT duplicate-PATH lines, two
// hardcoded-home lines differing only in whitespace, or two distinct dead
// sources each carry distinct identities and can never claim each other's op.
func takeOpForFinding(ops []*repairOp, f plugin.Finding) (*repairOp, bool) {
	want := repairCoreDetail(f.Detail)
	for _, op := range ops {
		if !op.taken && op.kind == f.Kind && op.block == f.Block &&
			repairCoreDetail(op.detail) == want && op.suggested == f.Suggested {
			op.taken = true
			return op, true
		}
	}
	return nil, false
}

// applyRepairOp performs one line-anchored repair on raw: an in-line
// hardcoded-home substitution, or a single-line removal (newline included).
// A block left with no non-blank content empties entirely (Raw -> nil),
// honouring "emptied, not deleted".
func applyRepairOp(raw []byte, op *repairOp) []byte {
	segs := splitKeepEnds(raw)
	if op.line < 0 || op.line >= len(segs) {
		return raw
	}
	var out []byte
	for i, seg := range segs {
		if i != op.line {
			out = append(out, seg...)
			continue
		}
		if op.remove {
			continue
		}
		out = append(out, homePathRe.ReplaceAll(seg, []byte("$$HOME"))...)
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil
	}
	return out
}

// sourceTarget extracts the target of an UNGUARDED `source <target>` /
// `. <target>` line (a guarded `[ -f ... ] && source ...` line is deliberately
// not matched — its own guard already handles absence).
func sourceTarget(code string) (string, bool) {
	fields := strings.Fields(code)
	if len(fields) != 2 {
		return "", false
	}
	if fields[0] != "source" && fields[0] != "." {
		return "", false
	}
	return fields[1], true
}

// expandPath expands a leading ~/ or $HOME/ against the plugin's Home. Paths
// carrying other unexpandable variables (or relative paths) return ok=false —
// their existence cannot be judged safely. Anything under ~/.ssh is never
// probed (hands-off contract: not even an Lstat).
func (p *Plugin) expandPath(target string) (string, bool) {
	var resolved string
	switch {
	case strings.HasPrefix(target, "~/"):
		resolved = filepath.Join(p.Home, target[2:])
	case strings.HasPrefix(target, "$HOME/"):
		resolved = filepath.Join(p.Home, target[len("$HOME/"):])
	case strings.HasPrefix(target, "~") || strings.Contains(target, "$"):
		return "", false
	case filepath.IsAbs(target):
		resolved = target
	default:
		return "", false
	}
	// Never hand back a path at/under ~/.ssh (the caller Lstats the result, and
	// the hands-off contract forbids even that). The ".ssh" segment is compared
	// case-INSENSITIVELY: on the default case-insensitive macOS filesystem a
	// target like ~/.SSH/x maps to the real ~/.ssh, so it must be refused too.
	if p.Home != "" && underHomeSSH(p.Home, resolved) {
		return "", false
	}
	return resolved, true
}

// underHomeSSH reports whether path is home's ".ssh" directory itself or a
// descendant, folding case ONLY on the ".ssh" segment so a case-variant such as
// ".SSH" (which the kernel maps into ~/.ssh on a case-insensitive filesystem) is
// caught too. The parent components match exactly. It is pure path arithmetic —
// it never stats path or ~/.ssh.
func underHomeSSH(home, path string) bool {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	first := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		first = rel[:i]
	}
	return strings.EqualFold(first, ".ssh")
}
