package secret

import (
	"strings"
	"testing"
)

func TestFlaggedSpansSingleLine(t *testing.T) {
	token := "FERRYUNITq7W3e9R1t8Y2u0I6o4P5aSdKjZx"
	text := "# ok\nexport GITHUB_TOKEN=" + token + "\nalias gs='git status'\n"
	spans := FlaggedSpans(text)
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	sp := spans[0]
	if sp.StartLine != 2 || sp.EndLine != 2 {
		t.Errorf("span lines = %d..%d, want 2..2", sp.StartLine, sp.EndLine)
	}
	if sp.Value != "export GITHUB_TOKEN="+token {
		t.Errorf("Value = %q, want the exact flagged line", sp.Value)
	}
}

func TestFlaggedSpansPEMWidening(t *testing.T) {
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaFVuaXRCb2R5WlpaWlpaWlpaWlpaWlpaWlpaWlpa\n-----END OPENSSH PRIVATE KEY-----"
	text := "# key\n" + pem + "\nexport AFTER=ok\n"
	spans := FlaggedSpans(text)
	if len(spans) != 1 {
		t.Fatalf("want 1 widened span, got %d: %+v", len(spans), spans)
	}
	sp := spans[0]
	if sp.StartLine != 2 || sp.EndLine != 4 {
		t.Errorf("span lines = %d..%d, want 2..4 (BEGIN..END widening)", sp.StartLine, sp.EndLine)
	}
	if sp.Value != pem {
		t.Errorf("Value != exact BEGIN..END run:\n%q", sp.Value)
	}
}

func TestFlaggedSpansClean(t *testing.T) {
	if spans := FlaggedSpans("# nothing here\nalias gs='git status'\n"); spans != nil {
		t.Errorf("clean text produced spans: %+v", spans)
	}
}

func TestRenderWithMapIdentityWithoutPlaceholders(t *testing.T) {
	s := OpenAt(t.TempDir())
	content := "line one\nline two\n"
	res, err := s.RenderWithMap(content)
	if err != nil || res.Skip {
		t.Fatalf("err=%v skip=%v", err, res.Skip)
	}
	if res.Rendered != content {
		t.Errorf("identity render changed content: %q", res.Rendered)
	}
	for i, seg := range res.Segments {
		if seg.SrcLine != i+1 || seg.RenderedStart != i+1 || seg.RenderedEnd != i+1 || seg.Placeholder {
			t.Errorf("segment %d not 1:1: %+v", i, seg)
		}
	}
}

// r9-M2: two placeholders on ONE source line render into one atomic segment
// whose whole rendered range maps back to exactly that source line.
func TestRenderWithMapMultiPlaceholderOneLine(t *testing.T) {
	store := OpenAt(t.TempDir())
	if err := store.Put("zsh.token_a", "aaa111"); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("zsh.token_b", "bbb222"); err != nil {
		t.Fatal(err)
	}
	content := "# top\nexport A={{ferry.secret \"zsh.token_a\"}} B={{ferry.secret \"zsh.token_b\"}}\ntail\n"
	res, err := store.RenderWithMap(content)
	if err != nil || res.Skip {
		t.Fatalf("err=%v skip=%v missing=%v", err, res.Skip, res.Missing)
	}
	if !strings.Contains(res.Rendered, "export A=aaa111 B=bbb222") {
		t.Errorf("both placeholders on the line were not rendered:\n%s", res.Rendered)
	}
	// Segment 2 (source line 2) must be ONE placeholder segment covering ONE
	// rendered line.
	seg := res.Segments[1]
	if !seg.Placeholder || seg.SrcLine != 2 || seg.RenderedStart != 2 || seg.RenderedEnd != 2 {
		t.Errorf("multi-placeholder line segment wrong: %+v", seg)
	}
}

// r8-C1: a multi-line stored value (PEM) expands its ONE source line's segment
// to the full rendered range; following lines shift but stay 1:1.
func TestRenderWithMapMultiLineValueSegments(t *testing.T) {
	store := OpenAt(t.TempDir())
	pem := "-----BEGIN X-----\nbody1\nbody2\n-----END X-----"
	if err := store.Put("zsh.secret_2", pem); err != nil {
		t.Fatal(err)
	}
	content := "# key\n{{ferry.secret \"zsh.secret_2\"}}\nafter\n"
	res, err := store.RenderWithMap(content)
	if err != nil || res.Skip {
		t.Fatalf("err=%v skip=%v", err, res.Skip)
	}
	want := "# key\n" + pem + "\nafter\n"
	if res.Rendered != want {
		t.Errorf("rendered = %q, want %q", res.Rendered, want)
	}
	seg := res.Segments[1]
	if !seg.Placeholder || seg.SrcLine != 2 || seg.RenderedStart != 2 || seg.RenderedEnd != 5 {
		t.Errorf("PEM segment wrong (want rendered 2..5 -> src 2): %+v", seg)
	}
	after := res.Segments[2]
	if after.Placeholder || after.SrcLine != 3 || after.RenderedStart != 6 || after.RenderedEnd != 6 {
		t.Errorf("post-PEM segment wrong: %+v", after)
	}
}

// A missing ref mirrors RenderPlaceholders: Skip=true, refs listed, NO output.
func TestRenderWithMapMissingRefSkips(t *testing.T) {
	store := OpenAt(t.TempDir())
	res, err := store.RenderWithMap("x {{ferry.secret \"zsh.gone\"}}\n")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skip || len(res.Missing) != 1 || res.Missing[0] != "zsh.gone" {
		t.Errorf("missing-ref result wrong: %+v", res)
	}
	if res.Rendered != "" {
		t.Errorf("Skip result carries rendered output: %q", res.Rendered)
	}
}

// Ship-review Claude M1: PEM widening never swallows a placeholder line — an
// UNTERMINATED BEGIN header above an existing placeholder stops the span
// BEFORE the placeholder-bearing line, so a span Value never contains a
// ferry placeholder (storing one would nest it inside a stored value).
func TestFlaggedSpansStopAtPlaceholderLine(t *testing.T) {
	text := "# curated\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"c3ludGhVbml0Qm9keVpaWlpaWlpaWlpaWlpaWlpaWlpaWlpa\n" +
		"{{ferry.secret \"zsh.github_token\"}}\n" +
		"alias keep='me'\n"
	spans := FlaggedSpans(text)
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d: %+v", len(spans), spans)
	}
	sp := spans[0]
	if sp.StartLine != 2 || sp.EndLine != 3 {
		t.Errorf("span = %d..%d, want 2..3 (widening must stop before the placeholder line)", sp.StartLine, sp.EndLine)
	}
	if strings.Contains(sp.Value, "{{ferry.secret") {
		t.Errorf("span Value contains a placeholder (would nest inside a stored value): %q", sp.Value)
	}
	// A TERMINATED run below a placeholder still widens to its END normally.
	terminated := "{{ferry.secret \"zsh.a\"}}\n" +
		"-----BEGIN OPENSSH PRIVATE KEY-----\nbody\n-----END OPENSSH PRIVATE KEY-----\n"
	spans = FlaggedSpans(terminated)
	if len(spans) != 1 || spans[0].StartLine != 2 || spans[0].EndLine != 4 {
		t.Errorf("terminated PEM below a placeholder: %+v, want one span 2..4", spans)
	}
}
