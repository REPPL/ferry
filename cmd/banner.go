package cmd

import (
	"io"
	"os"

	"github.com/fatih/color"
)

// Colour palette, shared by the banner and the status/diff/doctor output. All
// colouring goes through fatih/color rather than hand-written escape codes, so
// NO_COLOR, FORCE_COLOR, Windows, and non-terminal destinations are handled by
// the library. We additionally gate on the destination being an interactive
// stdout (see colourEnabled) so piping ferry into a file or a log never captures
// colour, regardless of the library's global state.
var (
	// Banner palette echoes the ferry logo: red boat with a yellow separating
	// line, blue water, green terminal prompt, yellow smoke/portholes, yellow
	// "ferry" wordmark, red version.
	colBoat   = color.New(color.FgRed, color.Bold)    // banner: the boat body
	colSep    = color.New(color.FgYellow, color.Bold) // banner: the boat's separating line
	colWave   = color.New(color.FgBlue, color.Bold)   // banner: the water/wake
	colFunnel = color.New(color.FgRed, color.Bold)    // banner: the funnels
	colSmoke  = color.New(color.FgYellow)             // banner: smoke puffs / portholes
	colPrompt = color.New(color.FgGreen, color.Bold)  // banner: the >_ terminal prompt
	colName   = color.New(color.FgYellow, color.Bold) // banner: the "ferry" wordmark
	colVer    = color.New(color.FgRed)                // banner: the version number
	colFaint  = color.New(color.Faint)                // banner: tagline / hints
	colGreen  = color.New(color.FgGreen)              // status: clean / pass / managed
	colYellow = color.New(color.FgYellow)             // status: drift / warn / would-change
	colRed    = color.New(color.FgRed)                // status: conflict / fail
)

// colourEnabled reports whether coloured output should be used when writing to w.
// It requires w to be os.Stdout, that stdout to be a character device (a TTY),
// and the library's environment gate (color.NoColor, set from NO_COLOR / not a
// terminal) to permit colour. Any other case renders plain text.
func colourEnabled(w io.Writer) bool {
	if w != os.Stdout || color.NoColor {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// colourFor returns a colouriser bound to w: when colourEnabled(w), it renders
// each string with the given *color.Color; otherwise it returns the string
// unchanged. status/diff/doctor call this to colour state words consistently.
func colourFor(w io.Writer) func(*color.Color, string) string {
	if !colourEnabled(w) {
		return func(_ *color.Color, s string) string { return s }
	}
	return func(c *color.Color, s string) string { return c.Sprint(s) }
}

// printBanner writes ferry's ASCII banner to w, coloured when the destination is
// an interactive terminal. It is shown for a bare `ferry` invocation (no
// subcommand) as a friendly landing screen; every subcommand keeps its own output.
func printBanner(w io.Writer) {
	paint := colourFor(w)
	boat := func(s string) string { return paint(colBoat, s) }
	sep := func(s string) string { return paint(colSep, s) }
	wave := func(s string) string { return paint(colWave, s) }
	funnel := func(s string) string { return paint(colFunnel, s) }
	smoke := func(s string) string { return paint(colSmoke, s) }
	prompt := func(s string) string { return paint(colPrompt, s) }
	name := func(s string) string { return paint(colName, s) }
	ver := func(s string) string { return paint(colVer, s) }
	cmd := func(s string) string { return paint(colYellow, s) }
	tag := func(s string) string { return paint(colFaint, s) }

	// A little ferry echoing the logo: two red funnels with smoke, green portholes,
	// a green >_ terminal prompt, a red boat body over a yellow separating line and
	// blue waves. The yellow "ferry" wordmark carries a red version beside it.
	lines := []string{
		"",
		"     " + smoke("o °") + " " + smoke("o °"),
		"    " + funnel("_[I][I]_") + "        " + name("ferry") + " " + ver(version),
		"   " + boat("/  ") + smoke("o o o") + boat("  \\___") + "   " + prompt(">_") + tag(" carries your setup"),
		"  " + boat("/") + sep("_______________") + boat("\\") + tag("    across machines"),
		"  " + boat("\\") + wave("~~~~~~~~~~~~~~~") + boat("/"),
		"   " + boat("`") + wave("~~~~~~~~~~~~~") + boat("'"),
		"",
		"  run " + cmd("ferry --help") + tag(" to get started,"),
		"  " + tag("or ") + cmd("ferry init") + tag(" on a new machine."),
	}
	for _, ln := range lines {
		io.WriteString(w, ln+"\n")
	}
}
