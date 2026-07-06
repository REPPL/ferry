// Command gendocs generates the committed CLI reference under docs/reference/cli
// from ferry's cobra command tree. It is a standalone main package the ferry
// binary never imports, so its doc-only dependencies (cobra/doc, go-md2man,
// blackfriday) stay out of the shipped binary.
//
// The generated pages carry NO "Auto generated ... on <date>" footer: the
// auto-gen tag is disabled on the root and recursively on every subcommand so
// the output is deterministic and a CI currency check can diff it byte-for-byte.
package main

import (
	"fmt"
	"os"

	"github.com/REPPL/ferry/cmd"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

const outDir = "docs/reference/cli"

// disableAutoGenTag turns off the timestamped footer on cmd and all of its
// descendants so generated Markdown is deterministic across runs.
func disableAutoGenTag(cmd *cobra.Command) {
	cmd.DisableAutoGenTag = true
	for _, sub := range cmd.Commands() {
		disableAutoGenTag(sub)
	}
}

func main() {
	root := cmd.Root()
	disableAutoGenTag(root)

	// Clean-regenerate: remove the tree first so a page for a command that no
	// longer exists is deleted rather than left as a stale orphan (GenMarkdownTree
	// only ever writes, never deletes). This makes the CI currency check able to
	// catch a removed command, not just an added or changed one.
	if err := os.RemoveAll(outDir); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}

	if err := doc.GenMarkdownTree(root, outDir); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}
