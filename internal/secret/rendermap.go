package secret

import "strings"

// RenderSegment maps a rendered line-range back to exactly one source line.
// Segments are COALESCED PER SOURCE LINE (Codex r9-M2): a source line carrying
// one or more placeholders produces ONE atomic segment whose entire rendered
// range (possibly many lines, for a multi-line stored value — Codex r8-C1)
// maps back to that single source line; every other line maps 1:1.
type RenderSegment struct {
	// SrcLine is the 1-based source line this segment maps back to.
	SrcLine int
	// RenderedStart/RenderedEnd are the 1-based, inclusive rendered line range.
	RenderedStart, RenderedEnd int
	// Placeholder is true when the source line contains at least one
	// placeholder — such a segment is ATOMIC for reverse-render: all rendered
	// lines unchanged => emit the source line; ANY changed => wholly changed.
	Placeholder bool
}

// RenderMapResult is RenderWithMap's outcome: the rendered text plus the
// segment map, or Skip with the missing refs (mirroring RenderPlaceholders).
type RenderMapResult struct {
	Rendered string
	Segments []RenderSegment
	Missing  []string
	Skip     bool
}

// RenderWithMap renders every placeholder in content (like RenderPlaceholders)
// and ALSO returns the segment map linking rendered lines back to source
// lines. If ANY referenced secret is missing it returns Skip == true with the
// missing refs and NO rendered output — callers fall back to the raw source.
// The rendered text is built line-by-line, so a stored multi-line value
// expands its source line's segment to the full rendered range.
func (s *Store) RenderWithMap(content string) (RenderMapResult, error) {
	refs := DetectPlaceholders(content)
	if len(refs) == 0 {
		// No placeholders: 1:1 identity map.
		res := RenderMapResult{Rendered: content}
		lines := strings.Split(content, "\n")
		for i := range lines {
			res.Segments = append(res.Segments, RenderSegment{
				SrcLine: i + 1, RenderedStart: i + 1, RenderedEnd: i + 1,
			})
		}
		return res, nil
	}

	values := make(map[string]string, len(refs))
	var missing []string
	for _, ref := range refs {
		v, ok, err := s.Get(ref)
		if err != nil {
			return RenderMapResult{}, err
		}
		if !ok {
			missing = append(missing, ref)
			continue
		}
		values[ref] = v
	}
	if len(missing) > 0 {
		return RenderMapResult{Missing: missing, Skip: true}, nil
	}

	srcLines := strings.Split(content, "\n")
	var renderedLines []string
	var segs []RenderSegment
	for i, line := range srcLines {
		hasPlaceholder := placeholderRe.MatchString(line)
		rendered := line
		if hasPlaceholder {
			rendered = placeholderRe.ReplaceAllStringFunc(line, func(match string) string {
				m := placeholderRe.FindStringSubmatch(match)
				return values[m[1]]
			})
		}
		start := len(renderedLines) + 1
		expanded := strings.Split(rendered, "\n")
		renderedLines = append(renderedLines, expanded...)
		segs = append(segs, RenderSegment{
			SrcLine:       i + 1,
			RenderedStart: start,
			RenderedEnd:   start + len(expanded) - 1,
			Placeholder:   hasPlaceholder,
		})
	}
	return RenderMapResult{
		Rendered: strings.Join(renderedLines, "\n"),
		Segments: segs,
	}, nil
}
