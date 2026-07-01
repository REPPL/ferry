package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// versionCmd prints ferry's version. Plain `ferry version` matches the single
// line `ferry --version` prints; `ferry version --verbose` (or `-v`) additionally
// reports the Go toolchain and the host OS/arch so a bug report can carry the full
// build context. The cobra --version flag on root is left untouched (one line).
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print ferry's version (add --verbose for build details)",
	Long: `Print ferry's version.

Plain "ferry version" prints one line: ferry <version>. Add --verbose (-v) to also
report the Go toolchain and the host OS/architecture — handy for bug reports.`,
	Args: cobra.NoArgs,
	RunE: runVersion,
}

func init() {
	versionCmd.Flags().BoolP("verbose", "v", false, "also show the Go toolchain and host OS/arch")
	rootCmd.AddCommand(versionCmd)
}

func runVersion(c *cobra.Command, _ []string) error {
	out := c.OutOrStdout()
	verbose, _ := c.Flags().GetBool("verbose")

	if !verbose {
		// Match the plain `ferry --version` line exactly.
		fmt.Fprintf(out, "ferry %s\n", version)
		return nil
	}

	fmt.Fprintf(out, "ferry %s\n", version)
	fmt.Fprintf(out, "  go:       %s\n", runtime.Version())
	fmt.Fprintf(out, "  platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return nil
}
