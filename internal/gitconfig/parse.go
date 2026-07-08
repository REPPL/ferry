// Package gitconfig holds ferry's git-INI-format knowledge that must NOT live in
// the generic dotfile / secret machinery: a byte-faithful git-config parser
// (Reassemble(Parse(x)) == x), the identity partition (user.email / user.name /
// user.signingkey / gpg.program / credential.helper and every [includeIf …]
// block are forced to the never-shared ~/.gitconfig.local layer), the
// credential.helper=store refusal, and the sub-value secret recogniser for
// URL-embedded tokens and http.extraHeader credentials.
//
// It CALLS the generic secret primitives (secret.ScanText,
// secret.IsNonPlaceholderSecret, secret.SwapColumns) but never teaches
// internal/secret any git syntax — the same boundary the tmux and zsh plugins
// keep. The parser is hand-written (no ini / go-git dependency): git-config's
// grammar is small and ferry needs byte-for-byte round-trip fidelity a general
// library would not promise.
package gitconfig

import "strings"

// Kind classifies a physical git-config line for the analysis passes. It never
// affects Reassemble — reassembly is a verbatim concatenation of every line's
// Raw bytes — so an unrecognised line (Junk) still round-trips exactly.
type Kind int

const (
	// Blank is a whitespace-only line.
	Blank Kind = iota
	// Comment is a line whose first non-whitespace rune is '#' or ';'.
	Comment
	// Section is a `[section]` / `[section "sub"]` / `[section.sub]` header line.
	Section
	// KeyValue is a `name = value` (or bare boolean `name`) assignment line.
	KeyValue
	// Junk is a non-blank, non-comment line that is neither a section header nor a
	// recognisable assignment. It is preserved verbatim and owns no section key.
	Junk
)

// Line is one physical line of a git-config file. Raw is the EXACT bytes of the
// line INCLUDING its trailing '\n' (the final line of a file with no trailing
// newline omits it), so Reassemble is a plain concatenation and
// Reassemble(Parse(x)) == x holds byte-for-byte — comments, blank lines, tabs,
// indentation, subsection headers, and multivalue keys included.
//
// Section (canonical, lowercased, no subsection), Subsection (raw), and Key
// (lowercased variable name, KeyValue lines only) are the ANALYSIS metadata the
// identity partition keys off; they are derived, never used for reassembly.
type Line struct {
	Raw        string
	Kind       Kind
	Section    string // canonical lowercased section name in effect for this line
	Subsection string // raw subsection text of the section in effect ("" if none)
	Key        string // lowercased variable name (KeyValue only)
}

// FullKey is the canonical dotted key of a KeyValue line: "<section>.<key>"
// (both lowercased). It is "" for a line the section context could not be
// resolved for (a KeyValue before any section header — invalid git config).
func (l Line) FullKey() string {
	if l.Kind != KeyValue || l.Section == "" {
		return ""
	}
	return l.Section + "." + l.Key
}

// Parse splits content into logical lines, classifying each and threading the
// current section context onto every line, modelling git-config's real grammar so
// the identity firewall sees every key git would:
//
//   - INLINE assignment: git parses `[section] key = value` (and `[section]key=x`)
//     as a header PLUS an assignment. Parse splits such a physical line into two
//     logical Lines — a Section Line (the `[section]` bytes) and a KeyValue Line
//     (the trailing `key = value` bytes) — so the trailing key is classified and
//     the firewall can drop an inline identity key (security-review F1).
//   - VALUE CONTINUATION: git joins a value across physical lines when a line ends
//     in an ODD run of trailing backslashes. Parse marks the continuation physical
//     line as a KeyValue INHERITING the owning key's section/name, so a
//     backslash-continued identity value is dropped with its key rather than
//     surviving as a fresh non-identity key (security-review F2).
//
// It is TOTAL: any byte sequence parses (an unrecognised line becomes Junk), and
// Reassemble(Parse(x)) == x for every input — the split preserves the physical
// line's exact bytes ACROSS the two logical Lines' Raw fields, and every other
// line keeps its bytes verbatim, so reassembly is byte-exact even though
// classification changed. Non-UTF-8 bytes pass through untouched inside Raw; a
// CRLF line keeps its '\r' inside Raw.
func Parse(content []byte) []Line {
	if len(content) == 0 {
		return nil
	}
	raws := splitKeepEnds(content)
	lines := make([]Line, 0, len(raws))

	curSection, curSub := "", ""
	// Continuation state: contActive means the previous physical value line ended
	// in an odd backslash run, so THIS physical line continues that value and
	// inherits contSection/contKey (never opening a new key).
	contActive := false
	contSection, contKey := "", ""

	for _, raw := range raws {
		if contActive {
			l := Line{Raw: raw, Kind: KeyValue, Section: contSection, Subsection: curSub, Key: contKey}
			lines = append(lines, l)
			contActive = endsInOddBackslash(raw)
			continue
		}

		trimmed := strings.TrimSpace(trimNL(raw))
		if trimmed != "" && trimmed[0] == '[' {
			headerRaw, restRaw, hasRest := splitInlineHeader(raw)
			if sec, sub, ok := parseSectionHeader(strings.TrimSpace(trimNL(headerRaw))); ok {
				curSection, curSub = sec, sub
				// When there is no inline rest, the Section line owns the WHOLE
				// physical line (including its newline / any trailing blank) so
				// reassembly stays byte-exact; only a real inline assignment splits the
				// physical line's bytes across two logical Lines.
				secRaw := raw
				if hasRest {
					secRaw = headerRaw
				}
				lines = append(lines, Line{Raw: secRaw, Kind: Section, Section: sec, Subsection: sub})
				if hasRest {
					rl := classifyBodyLine(restRaw, curSection, curSub)
					lines = append(lines, rl)
					if rl.Kind == KeyValue {
						contActive = endsInOddBackslash(restRaw)
						contSection, contKey = rl.Section, rl.Key
					}
				}
				continue
			}
			// A `[`-leading line with no usable section name (a bare `[]`) is Junk AND
			// resets the section context to "" so a following key is never
			// MIS-attributed to the previous section (defensive; git rejects such a
			// file). The WHOLE physical line is preserved as one Junk Line.
			lines = append(lines, Line{Raw: raw, Kind: Junk})
			curSection, curSub = "", ""
			continue
		}

		l := classifyBodyLine(raw, curSection, curSub)
		lines = append(lines, l)
		if l.Kind == KeyValue {
			contActive = endsInOddBackslash(raw)
			contSection, contKey = l.Section, l.Key
		}
	}
	return lines
}

// classifyBodyLine classifies a NON-header physical line (blank / comment /
// assignment / junk) under the current section context. A KeyValue requires a
// section context (a section-less assignment is Junk, matching git's error).
func classifyBodyLine(raw, section, subsection string) Line {
	l := Line{Raw: raw, Section: section, Subsection: subsection}
	trimmed := strings.TrimSpace(trimNL(raw))
	switch {
	case trimmed == "":
		l.Kind = Blank
	case trimmed[0] == '#' || trimmed[0] == ';':
		l.Kind = Comment
	default:
		if key, ok := parseKey(trimmed); ok && section != "" {
			l.Kind = KeyValue
			l.Key = key
		} else {
			l.Kind = Junk
		}
	}
	return l
}

// splitInlineHeader splits a `[`-leading physical line into the `[section]` header
// bytes and any trailing assignment/comment bytes git would parse as a separate
// unit. headerRaw + restRaw == raw byte-for-byte. hasRest is true only when
// restRaw carries non-blank content (a trailing assignment or comment). When the
// header does not close on this line (no `]`), the whole line is the header
// (restRaw empty) — parseSectionHeader parses it leniently.
func splitInlineHeader(raw string) (headerRaw, restRaw string, hasRest bool) {
	c := inlineCloseIdx(raw)
	if c < 0 {
		return raw, "", false
	}
	headerRaw = raw[:c+1]
	restRaw = raw[c+1:]
	return headerRaw, restRaw, strings.TrimSpace(trimNL(restRaw)) != ""
}

// inlineCloseIdx returns the byte index of the `]` that CLOSES the section header
// on this physical line, respecting a quoted subsection (a `]` inside `"…"` does
// not close the header) and never crossing a newline. It returns -1 when the
// header does not close on this line.
func inlineCloseIdx(raw string) int {
	open := strings.IndexByte(raw, '[')
	if open < 0 {
		return -1
	}
	inQuote := false
	for i := open + 1; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			inQuote = !inQuote
		case ']':
			if !inQuote {
				return i
			}
		case '\n':
			return -1
		}
	}
	return -1
}

// endsInOddBackslash reports whether a physical line's content (its bytes with the
// trailing newline / CR stripped) ends in an ODD number of backslashes — git's
// value-continuation signal (an even run is escaped backslashes, not a
// continuation).
func endsInOddBackslash(raw string) bool {
	body := trimNL(raw)
	n := 0
	for i := len(body) - 1; i >= 0 && body[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// trimNL strips a single trailing '\n' and then a trailing '\r' (a CRLF line
// ending), leaving the line's content bytes.
func trimNL(s string) string {
	s = strings.TrimSuffix(s, "\n")
	return strings.TrimSuffix(s, "\r")
}

// Reassemble concatenates every line's Raw bytes. Because Parse keeps each
// line's exact bytes (trailing newline included), Reassemble(Parse(x)) == x for
// every input — the round-trip property fuzzed in parse_test.go and required by
// the plan's STOP condition.
func Reassemble(lines []Line) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Raw)
	}
	return []byte(b.String())
}

// splitKeepEnds splits content into physical lines, each KEEPING its trailing
// '\n' (the final line may lack one). The concatenation of the result equals
// content exactly.
func splitKeepEnds(content []byte) []string {
	var out []string
	start := 0
	for i, b := range content {
		if b == '\n' {
			out = append(out, string(content[start:i+1]))
			start = i + 1
		}
	}
	if start < len(content) {
		out = append(out, string(content[start:]))
	}
	return out
}

// parseSectionHeader parses a trimmed `[section]` / `[section "subsection"]` /
// `[section.sub]` header. section is lowercased (git section names are
// case-insensitive); subsection is returned VERBATIM (a "sub" subsection is
// case-sensitive, a .sub dotted one is not — for identity matching only the
// section name and, for includeIf, its mere presence matter).
//
// A missing closing ']' is parsed LENIENTLY from what follows '[' (up to the end
// of the line) rather than rejected: git itself errors on `[user` ("missing ]"),
// but ferry must still classify that section so an identity key on the following
// line cannot slip past the firewall into the shared repo (security-review F1).
// ok is false only when no section name can be recovered at all (a bare `[]`),
// which the caller treats as a context-resetting Junk line. Either way the line's
// Raw bytes are preserved and Reassemble(Parse(x)) == x still holds.
func parseSectionHeader(trimmed string) (section, subsection string, ok bool) {
	if !strings.HasPrefix(trimmed, "[") {
		return "", "", false
	}
	inner := trimmed[1:]
	if close := strings.IndexByte(inner, ']'); close >= 0 {
		inner = inner[:close]
	}
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return "", "", false
	}
	if q := strings.IndexByte(inner, '"'); q >= 0 {
		// [section "subsection"] — subsection is the quoted remainder.
		sec := strings.TrimSpace(inner[:q])
		sub := inner[q+1:]
		if end := strings.LastIndexByte(sub, '"'); end >= 0 {
			sub = sub[:end]
		}
		return strings.ToLower(sec), sub, true
	}
	if dot := strings.IndexByte(inner, '.'); dot >= 0 {
		// [section.subsection] — deprecated dotted form, case-insensitive.
		return strings.ToLower(inner[:dot]), inner[dot+1:], true
	}
	return strings.ToLower(inner), "", true
}

// parseKey extracts the lowercased variable name from a trimmed assignment line
// ("name = value", "name", or "name = value ; comment"). The name runs up to the
// first '=', whitespace, or inline comment marker. ok is false when no name
// character precedes those delimiters.
func parseKey(trimmed string) (key string, ok bool) {
	end := len(trimmed)
	for i, r := range trimmed {
		if r == '=' || r == ' ' || r == '\t' || r == '#' || r == ';' {
			end = i
			break
		}
	}
	name := strings.TrimSpace(trimmed[:end])
	if name == "" {
		return "", false
	}
	return strings.ToLower(name), true
}
