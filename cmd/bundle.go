package cmd

import (
	"github.com/spf13/cobra"
)

var bundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "Move the config repo offline as a portable bundle",
	Long: `Companion commands for moving the config repo offline as a portable bundle.

When you can't clone the repo on the destination — a second user account on the
same machine, or a machine with no network path to the repo — a bundle carries
the setup across instead. "bundle export" writes a portable, secret-scanned .zip
of the repo's tracked shared files and prints its reproducible SHA256;
"bundle import <bundle>" validates that .zip fully and ingests it into a fresh
config repo on the other side. Secrets, ~/.ssh, and the per-machine local layer
are never carried unless you opt in with --include-local.`,
}

func init() {
	bundleCmd.AddCommand(exportCmd, importCmd)
	rootCmd.AddCommand(bundleCmd)
}
