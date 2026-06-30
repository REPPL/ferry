package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// runDiff is the read-only preview of what apply would change. It reuses apply's
// planning (buildPlan) but takes NO lock and writes NOTHING — a tripwire-safe
// preview. Each pending target is named with its action.
func runDiff(c *cobra.Command, _ []string) error {
	ctx, err := loadContext()
	if err != nil {
		return err
	}

	plan, warnings, err := buildPlan(ctx)
	if err != nil {
		return err
	}

	out := c.OutOrStdout()
	for _, w := range warnings {
		fmt.Fprintln(out, w)
	}
	printPlan(out, plan)
	return nil
}
