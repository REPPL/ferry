package cmd

// Capture reverse-render (PLAN "Capture reverse-render", F3-2): before gating
// and diffing a placeholder-bearing source, capture renders the repo source IN
// MEMORY (RenderWithMap) and RENDER-AND-SPLICES the live bytes back into
// source coordinates — placeholder segments are ATOMIC (all rendered lines
// unchanged => the single source placeholder line re-emits; ANY changed line
// => the whole segment stands as live content and gates as NEW). This is
// position-anchored, never blind value substitution (r7-M1), and keyed by the
// placeholders present in the source, so it covers wizard refs (`zsh.*`) AND
// the pre-existing capture namespaces (`zshrc.captured`, ...) alike (r2-M2).

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/REPPL/ferry/internal/gitconfig"
	"github.com/REPPL/ferry/internal/secret"
	"github.com/REPPL/ferry/internal/tmux"
)

// spanExtractor returns the flagged secret spans of a capture candidate's text.
// It is the seam that lets a format-specific recogniser (tmux's quoted
// `set -g @token '…'` value) replace the generic line-grained secret.FlaggedSpans
// for a domain that needs sub-value, column-grained isolation.
type spanExtractor func(text string) []secret.SecretSpan

// captureSpanExtractor selects the secret-span recogniser for a bare dotfile
// name. tmux uses the column-grained tmux recogniser so ONLY the quoted option
// value is stored/patched (the `set -g @token '…'` prefix and quotes survive
// byte-for-byte); every other dotfile uses the generic line-grained extractor.
func captureSpanExtractor(bare string) spanExtractor {
	switch bare {
	case "tmux.conf":
		return tmux.SecretSpans
	case gitconfigBare:
		// git tokens live inside a URL userinfo (url.*.insteadOf) or after
		// Bearer/Basic in http.extraHeader — the git recogniser isolates ONLY the
		// token so the surrounding URL/header syntax survives byte-for-byte.
		return gitconfig.SecretSpans
	}
	return secret.FlaggedSpans
}

// spanRoutable reports whether a capture candidate can route a NEW secret
// span-by-span (store just the span, patch only that span to a placeholder,
// preserve the curated remainder) rather than being blocked whole-file. A
// placeholder-bearing source is span-routable (the r6-M1 path); so is tmux, whose
// column-grained recogniser can isolate the secret inside a quoted option value
// even in an otherwise plaintext source.
func spanRoutable(bare string, placeholderAware bool) bool {
	return placeholderAware || bare == "tmux.conf" || bare == gitconfigBare
}

// spliceReverseRender rebuilds the live bytes in SOURCE coordinates: unchanged
// 1:1 lines take the repo-source line (placeholders intact — a placeholder can
// only ever reappear at its own source position); changed/inserted lines take
// the live line verbatim. srcText is the placeholder-bearing source, rendered/
// segs come from RenderWithMap(srcText), live is the current live file.
func spliceReverseRender(srcText string, rendered []byte, segs []secret.RenderSegment, live []byte) []byte {
	rl := splitLines(string(rendered))
	ll := splitLines(string(live))
	sl := splitLines(srcText)

	// Align rendered lines to live lines through the SAME LCS diff capture
	// uses everywhere: mapRL[r] = live index of an UNCHANGED rendered line r,
	// -1 for a changed/deleted one.
	hunks := diffHunks(string(rendered), string(live))
	mapRL := make([]int, len(rl))
	for i := range mapRL {
		mapRL[i] = -1
	}
	r, l := 0, 0
	for _, h := range hunks {
		for r < h.repoStart && r < len(rl) && l < len(ll) {
			mapRL[r] = l
			r++
			l++
		}
		r = h.repoEnd
		l += len(h.newLines)
	}
	for r < len(rl) && l < len(ll) {
		mapRL[r] = l
		r++
		l++
	}

	// For every INTACT placeholder segment (all rendered lines matched,
	// contiguously), replace its live-line range with the single source line.
	type replacement struct {
		liveStart, liveEnd int // 0-based inclusive
		srcLine            string
	}
	var reps []replacement
	for _, seg := range segs {
		if !seg.Placeholder {
			continue
		}
		a, b := seg.RenderedStart-1, seg.RenderedEnd-1
		if a < 0 || b >= len(rl) || seg.SrcLine-1 >= len(sl) {
			continue
		}
		intact := true
		for i := a; i <= b && intact; i++ {
			if mapRL[i] < 0 {
				intact = false
			}
		}
		for i := a; i < b && intact; i++ {
			if mapRL[i+1] != mapRL[i]+1 {
				intact = false
			}
		}
		if !intact {
			continue // wholly changed: the live content stands and gates as new
		}
		reps = append(reps, replacement{liveStart: mapRL[a], liveEnd: mapRL[b], srcLine: sl[seg.SrcLine-1]})
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].liveStart < reps[j].liveStart })

	var out []string
	pos := 0
	for _, rp := range reps {
		if rp.liveStart < pos {
			continue // overlap safety; segments are disjoint by construction
		}
		out = append(out, ll[pos:rp.liveStart]...)
		out = append(out, rp.srcLine)
		pos = rp.liveEnd + 1
	}
	out = append(out, ll[pos:]...)
	res := strings.Join(out, "\n")
	if len(live) > 0 && live[len(live)-1] == '\n' {
		res += "\n"
	}
	return []byte(res)
}

// prepareReverseRender runs the placeholder-aware pre-processing for one
// capture leg. Given the placeholder-bearing source and the live bytes it
// returns:
//   - compare: the bytes to classify/diff against (rendered when every ref
//     resolves, the raw source otherwise — the conservative baseline);
//   - reviewLive: the live bytes to review (reverse-rendered when rendering
//     succeeded, verbatim otherwise);
//   - aware: reverse-render active (all refs resolved);
//   - missingRef: the r8-M2 verbatim fallback is in effect (refs noted on errOut).
func prepareReverseRender(store *secret.Store, label string, source, live []byte, errOut io.Writer) (compare, reviewLive []byte, aware, missingRef bool, err error) {
	srcText := string(source)
	if len(secret.DetectPlaceholders(srcText)) == 0 {
		return source, live, false, false, nil
	}
	res, rerr := store.RenderWithMap(srcText)
	if rerr != nil {
		return nil, nil, false, false, rerr
	}
	if res.Skip {
		fmt.Fprintf(errOut, "note: %s references secret(s) not in this machine's store (missing: %s); capturing against the raw source — populate the store and re-run for placeholder-aware capture\n",
			label, strings.Join(res.Missing, ", "))
		return source, live, false, true, nil
	}
	rendered := []byte(res.Rendered)
	return rendered, spliceReverseRender(srcText, rendered, res.Segments, live), true, false, nil
}

// captureSecretMasks builds line-grained value->placeholder mask pairs for
// every flagged span in text, so hunk/preview output NEVER prints a gated
// secret value (the wizard's preview-masking contract mirrored on capture:
// scrollback is output — the value must not print BEFORE the consent prompt,
// or ever). Multi-line span values mask per line, because hunks render line by
// line. refPrefix names the would-be positional ref shown as the mask.
func captureSecretMasks(text, refPrefix string) []maskPair {
	var masks []maskPair
	// Mask with the BROAD line-grained detector, never a domain's narrow
	// column extractor: preview masking must cover EVERY high-confidence line the
	// gate caught (a secret that a domain recogniser does not isolate for storage
	// must still never print), and over-masking a preview is always safe. Storage
	// precision (a tmux column span) is a separate concern handled at the store
	// site (consentSpanStoreRoute's extractor).
	for _, sp := range secret.FlaggedSpans(text) {
		ph := secret.Placeholder(fmt.Sprintf("%s.secret_%d", refPrefix, sp.StartLine))
		for _, l := range strings.Split(sp.Value, "\n") {
			if strings.TrimSpace(l) == "" {
				continue
			}
			masks = append(masks, maskPair{value: l, placeholder: ph})
		}
	}
	return masks
}

// maskCaptureText applies mask pairs to a piece of capture output.
func maskCaptureText(text string, masks []maskPair) string {
	for _, m := range masks {
		text = strings.ReplaceAll(text, m.value, m.placeholder)
	}
	return text
}

// reportReadOnlyBlock is the r9-M1 rule: when the gate fires in the
// missing-ref fallback on a placeholder-bearing source, the whole-file store
// escape is NOT offered (it would overwrite the curated source with a single
// placeholder). The block is reported READ-ONLY, with guidance.
func reportReadOnlyBlock(out io.Writer, label string) {
	fmt.Fprintf(out, "  %s: SECRET / credential material detected (e.g. a private key or token).\n", label)
	fmt.Fprintf(out, "  This change is BLOCKED from the repo, and this source references secrets missing from this machine's store,\n")
	fmt.Fprintf(out, "  so the block is READ-ONLY here (no store route is offered — it would overwrite the curated source).\n")
	fmt.Fprintf(out, "  Populate the secret store (~/.config/ferry/secrets-local) for the missing ref(s), then re-run `ferry capture`.\n")
}

// consentSpanStoreRoute is the r6-M1 span-grained blocked path for a
// placeholder-bearing source: the gate fired on the COMPOSED capture (a NEW
// secret). With consent ([x], the existing capture key surface), each flagged
// span's Value is stored under a positional ref and ONLY that span is patched
// to its placeholder — the curated remainder (existing placeholders included)
// is preserved, and stored values never contain placeholders.
func consentSpanStoreRoute(in *bufio.Reader, out io.Writer, store *secret.Store, label, refPrefix, captured string, spansOf spanExtractor) (patched string, refs []string, ok bool, err error) {
	fmt.Fprintf(out, "  %s: SECRET / credential material detected in the accepted change (e.g. a private key or token).\n", label)
	fmt.Fprintf(out, "  It is BLOCKED from the repo entirely (both shared and local). It is never committed.\n")
	fmt.Fprintf(out, "  Only the out-of-band path is offered: [r]eject, or route ONLY the new secret span(s) to the out-of-repo secret store [x].\n")
	ans := prompt(in, out, "route blocked change? [r]eject / secret-store [x] (default r): ")
	if ans != "x" {
		fmt.Fprintf(out, "  %s: rejected; secret kept out of the repo\n", label)
		return "", nil, false, nil
	}
	spans := spansOf(captured)
	if len(spans) == 0 {
		fmt.Fprintf(out, "  %s: rejected; the blocked span could not be isolated\n", label)
		return "", nil, false, nil
	}
	// A span that cannot be isolated CLEANLY is never stored: a value carrying
	// a ferry placeholder would leave a literal placeholder in the deployed
	// file after apply's single render pass, and an INCOMPLETE private-key
	// block (BEGIN without END) has no trustworthy boundary. Either shape is
	// reported read-only — reject, nothing written, secret handled out of band.
	for _, sp := range spans {
		if len(secret.DetectPlaceholders(sp.Value)) > 0 || incompletePEMSpan(sp.Value) {
			fmt.Fprintf(out, "  %s: the blocked secret span cannot be isolated cleanly (an incomplete private-key block or placeholder-adjacent content); the gate block is READ-ONLY here — nothing was written. Handle the secret out of band, then re-run `ferry capture`.\n", label)
			return "", nil, false, nil
		}
	}
	// Patch POSITIONALLY, by line range, in REVERSE span order so earlier line
	// numbers stay valid — never a first-occurrence byte replace (an earlier
	// byte-equal occurrence must not be patched in the flagged span's stead).
	// Refs are probed before Put: a ref already holding a DIFFERENT value gets
	// a _2/_3... suffix — a previously stored secret is never silently
	// overwritten (positional line numbers shift across captures).
	refs = make([]string, len(spans))
	for i, sp := range spans {
		ref, rerr := freeSecretRef(store, refPrefix, sp.StartLine, sp.Value)
		if rerr != nil {
			return "", nil, false, rerr
		}
		refs[i] = ref
	}
	for i := len(spans) - 1; i >= 0; i-- {
		sp := spans[i]
		if perr := storePut(store, refs[i], sp.Value); perr != nil {
			// Honesty on a MID-LOOP failure (spans store in descending order,
			// so refs[i+1:] already landed): name what remains in the store —
			// values never printed — consistent with notifyStoredNotWritten.
			if stored := refs[i+1:]; len(stored) > 0 {
				return "", nil, false, fmt.Errorf("write to secret store for %s failed: %w — NOTE: the capture was not written, but %s were already stored and remain available in ~/.config/ferry/secrets-local (a re-run reuses them; remove them if unwanted)",
					refs[i], perr, strings.Join(stored, ", "))
			}
			return "", nil, false, fmt.Errorf("write to secret store: %w", perr)
		}
		captured = patchSpanLines(captured, sp, secret.Placeholder(refs[i]))
		fmt.Fprintf(out, "  %s: new secret span stored out-of-band as %s and patched to a placeholder in the PENDING capture\n", label, refs[i])
	}
	if secret.IsBlockedFromRepo(captured) {
		fmt.Fprintf(out, "  %s: content is still secret-shaped after span patching; rejected\n", label)
		notifyStoredNotWritten(out, label, refs)
		return "", nil, false, nil
	}
	return captured, refs, true, nil
}

// notifyStoredNotWritten keeps the store honest when a span consent Put values
// but the capture was subsequently refused/rejected: the entries are NOT
// deleted (safe direction — a retry reuses them via freeSecretRef), and the
// user is told exactly what remains where instead of an overstating
// "captured" message.
func notifyStoredNotWritten(out io.Writer, label string, refs []string) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(out, "  %s: NOTE — the capture was not written, but the consented secret value(s) remain in the out-of-repo store under %s (a re-run reuses them; remove them from ~/.config/ferry/secrets-local if unwanted)\n",
		label, strings.Join(refs, ", "))
}

// incompletePEMSpan reports whether a span value opens a private-key block
// (BEGIN marker) without closing it (no END line) — an unterminated key whose
// true extent is unknowable; storing/patching it would be a guess.
func incompletePEMSpan(value string) bool {
	if !secret.HasKeyMarker([]byte(value)) {
		return false
	}
	for _, l := range strings.Split(value, "\n") {
		if pemEndMarker.MatchString(l) {
			return false
		}
	}
	return true
}

// pemEndMarker matches a PEM END line (the span terminator).
var pemEndMarker = regexp.MustCompile(`(?i)END(?:\s+[A-Z0-9]+)*\s+PRIVATE\s+KEY`)

// storePut is the store-write seam for consentSpanStoreRoute. A variable so
// the partial-failure unit test can inject a failure mid-loop; production
// always calls Store.Put.
var storePut = func(store *secret.Store, ref, value string) error {
	return store.Put(ref, value)
}

// freeSecretRef returns the first collision-free positional ref for a new
// span: <prefix>.secret_<line>, suffixed _2, _3, ... while the candidate ref
// already holds a DIFFERENT value (a byte-identical value reuses its ref —
// Put is then an idempotent overwrite).
func freeSecretRef(store *secret.Store, prefix string, line int, value string) (string, error) {
	base := fmt.Sprintf("%s.secret_%d", prefix, line)
	ref := base
	for i := 2; ; i++ {
		existing, ok, err := store.Get(ref)
		if err != nil {
			return "", err
		}
		if !ok || existing == value {
			return ref, nil
		}
		ref = fmt.Sprintf("%s_%d", base, i)
	}
}

// patchSpanLines replaces a flagged span with a placeholder, preserving
// everything else — including any earlier byte-equal occurrence of the same
// content — and the text's trailing-newline shape.
//
// A COLUMN-grained span (HasColumns: a tmux quoted option value) replaces ONLY
// the byte range [StartCol, EndCol) of its single line via secret.SwapColumns,
// so the `set -g @token '…'` prefix and the surrounding quotes are byte-identical
// after patching. A whole-line span replaces its entire 1-based inclusive line
// range with the single placeholder line, as before.
func patchSpanLines(text string, sp secret.SecretSpan, placeholder string) string {
	lines := strings.Split(text, "\n")
	if sp.StartLine < 1 || sp.EndLine < sp.StartLine || sp.EndLine > len(lines) {
		return text
	}
	if sp.HasColumns() && sp.StartLine == sp.EndLine {
		swapped, err := secret.SwapColumns(lines[sp.StartLine-1], sp.StartCol, sp.EndCol, placeholder)
		if err != nil {
			return text // out-of-range offsets: leave the text untouched (never a partial write)
		}
		lines[sp.StartLine-1] = swapped
		return strings.Join(lines, "\n")
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[:sp.StartLine-1]...)
	out = append(out, placeholder)
	out = append(out, lines[sp.EndLine:]...)
	return strings.Join(out, "\n")
}

// renderForLastApplied renders a placeholder-bearing captured composition so
// last-applied records the RENDERED-EFFECTIVE bytes (r4-M2): the same bytes
// apply deploys and status classifies against. Rendering only fails on a
// missing ref, in which case the raw content is returned (conservative:
// last-applied then simply does not advance).
func renderForLastApplied(store *secret.Store, content []byte) []byte {
	res, err := store.RenderPlaceholders(string(content))
	if err != nil || res.Skip {
		return content
	}
	return []byte(res.Rendered)
}
