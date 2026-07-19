package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/REPPL/ferry/internal/backup"
	"github.com/REPPL/ferry/internal/config"
	"github.com/REPPL/ferry/internal/work"
)

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Carry in-flight project work between accounts as an explicit handover",
	Long: `Carry a project's in-flight work between accounts as an explicit baton pass.

A project's work state — the session handover note, the run journal, the coding
agent's per-project memory, and the redacted transcript store — never travels
with the project repo. "work pack" bundles it into a cargo store on shared or
portable media; "work receive" lands the latest cargo on another account,
backup-first and behind divergence guards; "work status" shows cargo, claims,
and drift; "work restore" reverts exactly the last receive. Work state is not
configuration: it has an owner, changes every session, and never merges
silently — so these are handover verbs, not a reconcile domain.

The cargo store is configured per machine in ~/.config/ferry/config.toml:

    [work]
    store = "/Users/Shared/ferry-cargo"`,
}

var workPackCmd = &cobra.Command{
	Use:   "pack <project-dir>",
	Short: "Bundle this project's work state into the cargo store",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkPack,
}

var workReceiveCmd = &cobra.Command{
	Use:   "receive <project-dir>",
	Short: "Land the latest cargo for this project here",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkReceive,
}

var workStatusCmd = &cobra.Command{
	Use:   "status [<project-dir>]",
	Short: "Show cargo, claims, divergence, and store size",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWorkStatus,
}

var workPruneCmd = &cobra.Command{
	Use:   "prune [<project-dir>]",
	Short: "Apply the cargo retention policy now",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runWorkPrune,
}

var workRestoreCmd = &cobra.Command{
	Use:   "restore <project-dir>",
	Short: "Revert exactly the last work receive on this account",
	Args:  cobra.ExactArgs(1),
	RunE:  runWorkRestore,
}

func init() {
	workCmd.PersistentFlags().Bool("allow-sync-root", false, "accept a cargo store under a cloud-synced directory (iCloud/Dropbox/…) for this run")
	workPackCmd.Flags().StringArray("exclude", nil, "leave a named item out of the cargo (repeatable; recorded in the manifest)")
	workPackCmd.Flags().Bool("allow-empty", false, "permit a pack without the handover note (memory/transcript-only cargo)")
	workPackCmd.Flags().StringArray("acknowledge", nil, "let a secret-flagged file travel as-is, named item/path (repeatable; pinned to its current content)")
	workReceiveCmd.Flags().Bool("force", false, "override the divergence and superseded guards, replacing local work state")
	workReceiveCmd.Flags().String("bundle", "", "receive the bundle with this exact SHA256 (resolves an equal-seq tie)")
	workPruneCmd.Flags().Int("keep", 0, "keep the last N bundles (default: [work] keep, else 5)")
	workPruneCmd.Flags().String("bundle", "", "remove exactly the bundle with this SHA256 instead of applying keep-last-N")
	workCmd.AddCommand(workPackCmd, workReceiveCmd, workStatusCmd, workPruneCmd, workRestoreCmd)
	rootCmd.AddCommand(workCmd)
}

// defaultWorkKeep is the keep-last-N retention when neither the flag nor the
// [work] table sets one.
const defaultWorkKeep = 5

// workContext is everything a work verb needs, resolved once.
type workContext struct {
	store   *work.Store
	lc      work.Locator
	id      work.Identity
	state   *work.State
	account string
	keep    int
}

// loadWorkContext resolves the project identity (with its shallow/worktree
// guards), the configured cargo store, this project's local work state, and
// this account's claim identity.
func loadWorkContext(c *cobra.Command, projectArg string) (*workContext, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	projectDir, err := filepath.Abs(projectArg)
	if err != nil {
		return nil, err
	}
	id, err := work.ProjectIdentity(projectDir)
	if err != nil {
		return nil, err
	}

	mc, err := config.LoadMachineConfig()
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("work: no machine config yet — run `ferry init` first, then add the [work] table to ~/.config/ferry/config.toml")
	}
	if err != nil {
		return nil, err
	}
	if mc.Work == nil || strings.TrimSpace(mc.Work.Store) == "" {
		return nil, errors.New(`work: no cargo store configured — add to ~/.config/ferry/config.toml:

    [work]
    store = "/Users/Shared/ferry-cargo"   # a shared or portable directory, created once

then create the directory (world-writable if two accounts share it) and retry`)
	}
	allowSync, _ := c.Flags().GetBool("allow-sync-root")
	st, err := work.OpenStore(mc.Work.Store, mc.Work.AllowSyncRoot || allowSync)
	if err != nil {
		return nil, err
	}
	state, err := work.LoadState(id.Key)
	if err != nil {
		return nil, err
	}
	keep := mc.Work.Keep
	if keep == 0 {
		keep = defaultWorkKeep
	}
	return &workContext{
		store:   st,
		lc:      work.Locator{Home: home, ProjectDir: projectDir, StoreKey: id.Key},
		id:      id,
		state:   state,
		account: workAccount(mc.Hostname),
		keep:    keep,
	}, nil
}

// workAccount derives this account's claim identity, user@host, sanitised to
// the claim-file character set.
func workAccount(hostname string) string {
	name := "user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	return sanitizeClaimPart(name) + "@" + sanitizeClaimPart(hostname)
}

// sanitizeClaimPart maps a raw user/host string onto [A-Za-z0-9._-] with an
// alphanumeric first character, so it always forms a valid claim filename.
func sanitizeClaimPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.TrimLeft(b.String(), "._-")
	if out == "" {
		return "x"
	}
	return out
}

func runWorkPack(c *cobra.Command, args []string) error {
	ctx, err := loadWorkContext(c, args[0])
	if err != nil {
		return err
	}
	out := c.OutOrStdout()
	excludes, _ := c.Flags().GetStringArray("exclude")
	allowEmpty, _ := c.Flags().GetBool("allow-empty")
	acks, _ := c.Flags().GetStringArray("acknowledge")

	opts := work.PackOptions{
		Excludes:     excludes,
		AllowEmpty:   allowEmpty,
		Account:      ctx.account,
		FerryVersion: version,
		Now:          time.Now().UTC().Format(time.RFC3339),
	}
	res, err := work.Pack(ctx.store, ctx.lc, ctx.id, ctx.state, opts)

	// A secret-gate stop with --acknowledge: pin each named finding to its
	// current content and retry ONCE. An unnamed finding still aborts.
	var sge *work.SecretGateError
	if errors.As(err, &sge) && len(acks) > 0 {
		named := map[string]bool{}
		for _, a := range acks {
			named[a] = true
		}
		var unmatched []string
		for _, f := range sge.Findings {
			if !named[f.Item+"/"+f.Path] {
				unmatched = append(unmatched, f.Item+"/"+f.Path)
				continue
			}
			ctx.state.Acks = append(ctx.state.Acks, work.Ack{
				Item: f.Item, Path: f.Path, SHA256: f.SHA256,
				Note: "acknowledged via work pack --acknowledge",
			})
		}
		if len(unmatched) > 0 {
			return fmt.Errorf("%w\n(--acknowledge covered some findings, but not: %s)", err, strings.Join(unmatched, ", "))
		}
		if err := ctx.state.Save(); err != nil {
			return err
		}
		fmt.Fprintf(out, "work: %d finding(s) acknowledged, pinned to current content\n", len(sge.Findings))
		res, err = work.Pack(ctx.store, ctx.lc, ctx.id, ctx.state, opts)
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "work: packed bundle %06d (%s) into %s\n", res.Ref.Seq, res.Ref.SHA256[:12], ctx.store.Root())
	for _, it := range res.Manifest.Items {
		if it.Included {
			fmt.Fprintf(out, "  %-13s %d file(s)\n", it.Name, len(it.Files))
		} else {
			fmt.Fprintf(out, "  %-13s — not packed (%s)\n", it.Name, it.Reason)
		}
	}
	if res.Manifest.ScanVerdict != work.ScanVerdictClean {
		fmt.Fprintf(out, "  secret scan: %s\n", res.Manifest.ScanVerdict)
	}
	fmt.Fprintf(out, "work: handover marker recorded; receive on the other account with `ferry work receive <project-dir>`\n")

	// Retention runs after a successful pack, as the plan pins.
	removed, err := ctx.store.Prune(ctx.id.Key, ctx.keep)
	if err != nil {
		return fmt.Errorf("work: pack succeeded but pruning failed: %w", err)
	}
	if len(removed) > 0 {
		fmt.Fprintf(out, "work: pruned %d old bundle(s) (keep-last-%d)\n", len(removed), ctx.keep)
	}
	return nil
}

func runWorkReceive(c *cobra.Command, args []string) error {
	ctx, err := loadWorkContext(c, args[0])
	if err != nil {
		return err
	}
	out := c.OutOrStdout()
	force, _ := c.Flags().GetBool("force")
	bundleSHA, _ := c.Flags().GetString("bundle")

	eng, err := backup.New()
	if err != nil {
		return err
	}
	// Receive mutates $HOME-managed paths through the engine; hold the same
	// lock apply holds so the two can never interleave.
	lock, err := eng.Lock()
	if err != nil {
		return err
	}
	defer lock.Unlock()

	res, err := work.Receive(ctx.store, eng, ctx.lc, ctx.id, ctx.state, work.ReceiveOptions{
		Force:        force,
		BundleSHA256: bundleSHA,
		Account:      ctx.account,
		Now:          time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	if res.TakeBack {
		fmt.Fprintf(out, "work: baton taken back — bundle %06d was packed by this account and nothing was restored\n", res.Ref.Seq)
		fmt.Fprintln(out, "work: the handover marker is cleared; keep working, then pack again when ready")
		return nil
	}
	fmt.Fprintf(out, "work: received bundle %06d (%s), packed by %s\n", res.Ref.Seq, res.Ref.SHA256[:12], res.Manifest.PackedBy)
	fmt.Fprintf(out, "  wrote %d path(s), skipped %d already-present\n", len(res.Written), len(res.Skipped))
	fmt.Fprintf(out, "work: revert this receive with `ferry work restore %s`\n", args[0])
	return nil
}

func runWorkStatus(c *cobra.Command, args []string) error {
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	ctx, err := loadWorkContext(c, dir)
	if err != nil {
		return err
	}
	s, err := work.Status(ctx.store, ctx.lc, ctx.id, ctx.state)
	if err != nil {
		return err
	}
	renderWorkStatus(c.OutOrStdout(), s)
	return nil
}

// renderWorkStatus prints the status picture in stable, plain lines.
func renderWorkStatus(out io.Writer, s *work.ProjectStatus) {
	fmt.Fprintf(out, "project %s\n", s.Key[:12])
	fmt.Fprintf(out, "store   %s (%.1f MiB total)\n", s.StoreDir, float64(s.StoreBytes)/(1<<20))
	if len(s.Bundles) == 0 {
		fmt.Fprintln(out, "cargo   none packed yet")
	}
	for _, b := range s.Bundles {
		fmt.Fprintf(out, "cargo   %06d %s\n", b.Seq, b.SHA256[:12])
	}
	if len(s.TopTie) > 1 {
		fmt.Fprintf(out, "WARNING %d bundles tie at the highest sequence — receive refuses until one is named (--bundle) or pruned\n", len(s.TopTie))
	}
	for _, cl := range s.Claims {
		if n := len(cl.Events); n > 0 {
			last := cl.Events[n-1]
			fmt.Fprintf(out, "claim   %s: last %s of %06d\n", cl.Account, last.Op, last.Seq)
		}
	}
	switch {
	case s.Marker == nil:
		fmt.Fprintln(out, "baton   no handover marker (not handed over from here)")
	case len(s.MarkerDirty) == 0:
		fmt.Fprintf(out, "baton   handed over as bundle %06d, not modified since\n", s.Marker.Seq)
	default:
		fmt.Fprintf(out, "baton   handed over as bundle %06d, MODIFIED after handover:\n", s.Marker.Seq)
		for _, d := range s.MarkerDirty {
			fmt.Fprintf(out, "        %s\n", d)
		}
	}
	if len(s.Diverged) > 0 {
		fmt.Fprintln(out, "drift   changed since this account last held the baton:")
		for _, d := range s.Diverged {
			fmt.Fprintf(out, "        %s\n", d)
		}
	}
	if len(s.OtherProjects) > 0 {
		fmt.Fprintf(out, "also in store: %s\n", strings.Join(s.OtherProjects, ", "))
	}
}

func runWorkPrune(c *cobra.Command, args []string) error {
	dir := "."
	if len(args) == 1 {
		dir = args[0]
	}
	ctx, err := loadWorkContext(c, dir)
	if err != nil {
		return err
	}
	out := c.OutOrStdout()
	if sha, _ := c.Flags().GetString("bundle"); sha != "" {
		if err := ctx.store.RemoveBundle(ctx.id.Key, sha); err != nil {
			return err
		}
		fmt.Fprintf(out, "work: removed bundle %s\n", sha[:12])
		return nil
	}
	keep := ctx.keep
	if n, _ := c.Flags().GetInt("keep"); n > 0 {
		keep = n
	}
	removed, err := ctx.store.Prune(ctx.id.Key, keep)
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		fmt.Fprintf(out, "work: nothing to prune (%d bundle(s) within keep-last-%d)\n", len(removed), keep)
		return nil
	}
	for _, r := range removed {
		fmt.Fprintf(out, "work: pruned bundle %06d (%s)\n", r.Seq, r.SHA256[:12])
	}
	return nil
}

func runWorkRestore(c *cobra.Command, args []string) error {
	ctx, err := loadWorkContext(c, args[0])
	if err != nil {
		return err
	}
	eng, err := backup.New()
	if err != nil {
		return err
	}
	lock, err := eng.Lock()
	if err != nil {
		return err
	}
	defer lock.Unlock()

	snapID, err := work.WorkRestore(eng, ctx.state)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.OutOrStdout(), "work: reverted the last receive (snapshot %s re-applied)\n", snapID)
	return nil
}
