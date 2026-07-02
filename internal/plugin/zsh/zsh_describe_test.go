package zsh

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/REPPL/ferry/internal/plugin"
)

// Describe's title clip must count RUNES: multibyte content (box-drawing
// banners, emoji) over the cap must clip to valid UTF-8, never mid-rune
// mojibake (review finding, 2026-07-02).
func TestDescribeClipsRunesNotBytes(t *testing.T) {
	p := New()
	// 44 runes but 130+ bytes: the OLD byte-count clip mangled this mid-rune;
	// rune-counting must leave it whole (44 <= 60).
	short := "export PS1=\"" + strings.Repeat("🚫", 30) + "\""
	got := p.Describe(plugin.Block{Kind: plugin.Other, Raw: []byte(short + "\n")})
	if !utf8.ValidString(got) {
		t.Fatalf("Describe produced invalid UTF-8: %q", got)
	}
	if strings.HasSuffix(got, "...") {
		t.Errorf("under-cap (44-rune) title was clipped: %q", got)
	}
	// 82 runes: over the cap — clipped to valid UTF-8 with the marker.
	long := "export PS1=\"" + strings.Repeat("🚫", 68) + "\""
	got = p.Describe(plugin.Block{Kind: plugin.Other, Raw: []byte(long + "\n")})
	if !utf8.ValidString(got) {
		t.Fatalf("Describe produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("over-cap title not clipped: %q", got)
	}
	banner := "# " + strings.Repeat("═", 70)
	got = p.Describe(plugin.Block{Kind: plugin.Comment, Raw: []byte(banner + "\n")})
	if !utf8.ValidString(got) {
		t.Fatalf("banner Describe produced invalid UTF-8: %q", got)
	}
}
