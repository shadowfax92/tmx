package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"tmx/internal/config"
	"tmx/internal/scratch"

	"github.com/spf13/cobra"
)

func init() {
	reapCmd.Flags().Bool("dry-run", false, "Show matching sessions without killing them")
	reapCmd.Flags().Bool("all", false, "Kill every scratch session")
	reapCmd.Flags().String("ttl", "", "Idle threshold override (e.g. 1h, 90m, 1d); defaults to config")
	rootCmd.AddCommand(reapCmd)
}

var reapCmd = &cobra.Command{
	Use:         "reap",
	Annotations: map[string]string{"group": "Scratch:"},
	Short:       "Kill stale scratch sessions (orphaned, idle, or dead-cwd)",
	Long: `Reap scratch (gs/) sessions that are no longer useful:

  orphan   — the parent pane is gone
  dead-cwd — the session's start directory no longer exists
  idle     — untouched longer than scratch.ttl (config, default 6h)

This also runs automatically on every scratch toggle, so the namespace
self-heals during normal use.

  tmx reap              — reap orphan + dead-cwd + idle(>ttl)
  tmx reap --dry-run    — show what would be reaped
  tmx reap --ttl 1h     — override the idle threshold
  tmx reap --all        — kill every scratch session`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts, err := reapOptionsFromFlags(cmd)
		if err != nil {
			return err
		}
		report, err := scratch.Reap(opts)
		printReapReport(cmd.OutOrStdout(), report, opts.DryRun)
		return err
	},
}

func reapOptionsFromFlags(cmd *cobra.Command) (scratch.ReapOptions, error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	all, _ := cmd.Flags().GetBool("all")
	ttlRaw, _ := cmd.Flags().GetString("ttl")

	if all && strings.TrimSpace(ttlRaw) != "" {
		return scratch.ReapOptions{}, fmt.Errorf("--all cannot be combined with --ttl")
	}

	var ttl time.Duration
	switch {
	case all:
		// TTL is irrelevant in --all mode.
	case strings.TrimSpace(ttlRaw) != "":
		d, err := config.ParseTTL(ttlRaw)
		if err != nil {
			return scratch.ReapOptions{}, err
		}
		ttl = d
	default:
		cfg, err := config.Load()
		if err != nil {
			return scratch.ReapOptions{}, fmt.Errorf("loading config: %w", err)
		}
		ttl = cfg.Scratch.TTL.Duration()
	}

	return scratch.ReapOptions{DryRun: dryRun, All: all, TTL: ttl}, nil
}

func printReapReport(w io.Writer, report scratch.ReapReport, dryRun bool) {
	if len(report.Matched) == 0 {
		fmt.Fprintln(w, "No scratch sessions matched.")
		return
	}

	if dryRun {
		fmt.Fprintf(w, "Would reap %d scratch sessions:\n", len(report.Matched))
		for _, c := range report.Matched {
			fmt.Fprintf(w, "  %-16s %-9s last active %s ago\n", c.SessionName, c.Reason, ago(c.LastActiveAt))
		}
		return
	}

	fmt.Fprintf(w, "Reaped %d scratch sessions:\n", len(report.Removed))
	for _, c := range report.Removed {
		fmt.Fprintf(w, "  %-16s %s\n", c.SessionName, c.Reason)
	}
	if len(report.Failed) > 0 {
		fmt.Fprintf(w, "Failed to reap %d:\n", len(report.Failed))
		for _, f := range report.Failed {
			fmt.Fprintf(w, "  %-16s %v\n", f.Candidate.SessionName, f.Err)
		}
	}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
