// Package cmd defines ferry's command-line surface. A0 scaffolds the root
// command and stub subcommands for every documented verb; real logic lands in
// later waves.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is the build version. Overridden at release via -ldflags.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "ferry",
	Short: "Carries your terminal, dotfiles, and dependencies across machines",
	Long: `ferry carries your terminal setup across user accounts and machines.

Define your configuration once in a git repo; ferry reconciles any machine to
match it, and pulls local changes back when you want to harmonise them
everywhere.`,
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command. It is the single entry point called by main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ferry:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.SetVersionTemplate("ferry {{.Version}}\n")
}
