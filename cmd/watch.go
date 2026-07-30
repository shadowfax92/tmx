package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tmx/internal/attn"
	"tmx/internal/config"

	"github.com/spf13/cobra"
)

var errWatcherStopped = errors.New("watcher stopped")

var (
	startWatchDaemon  = attn.StartDaemon
	stopWatchDaemon   = attn.StopDaemon
	statusWatchDaemon = attn.StatusDaemon
	runWatchDaemon    = attn.RunDaemon
	findWatchReap     = attn.FindReapCandidates
	reapWatchPanes    = attn.ReapPanes
	watchReapNow      = time.Now
)

func init() {
	rootCmd.AddCommand(newWatchCommand())
}

func newWatchCommand() *cobra.Command {
	watch := &cobra.Command{
		Use:         "watch",
		Annotations: map[string]string{"group": "Other:"},
		Short:       "Run and control the agent-pane attention watcher",
		Args:        cobra.NoArgs,
	}

	watch.AddCommand(
		&cobra.Command{
			Use:   "start",
			Short: "Start the detached watcher for this tmux socket",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				status, started, err := startWatchDaemon()
				if err != nil {
					return err
				}
				if started {
					fmt.Fprintf(cmd.OutOrStdout(), "watcher started (pid %d)\n", status.PID)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "watcher already running (pid %d)\n", status.PID)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the detached watcher for this tmux socket",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				_, stopped, err := stopWatchDaemon()
				if err != nil {
					return err
				}
				if stopped {
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "watcher stopped")
				} else {
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "watcher not running")
				}
				return err
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether the watcher is running",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				status, err := statusWatchDaemon()
				if err != nil {
					return err
				}
				if !status.Running {
					return errWatcherStopped
				}
				fmt.Fprintf(cmd.OutOrStdout(), "watcher running (pid %d)\n", status.PID)
				return nil
			},
		},
		&cobra.Command{
			Use:   "run",
			Short: "Run the watcher in the foreground",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := config.Load()
				if err != nil {
					return fmt.Errorf("loading config: %w", err)
				}
				ctx := cmd.Context()
				if ctx == nil {
					ctx = context.Background()
				}
				ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
				defer cancel()
				return runWatchDaemon(ctx, attn.WatchOptions{
					Poll:         cfg.Watch.Poll.Duration(),
					GracePeriods: cfg.Watch.GracePeriods,
					Period:       cfg.Watch.Period.Duration(),
					CaptureLines: cfg.Watch.CaptureLines,
					Agents:       cfg.Watch.Agents,
				})
			},
		},
		newWatchReapCommand(),
	)
	return watch
}

func newWatchReapCommand() *cobra.Command {
	reap := &cobra.Command{
		Use:   "reap",
		Short: "Kill agent panes whose screens have stopped changing",
		Long: `List agent panes whose screens have not changed past the watch reap TTL,
then ask once before killing the whole list. Explicitly unwatched panes remain
eligible, but panes focused by any attached client are always protected.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ttl, err := watchReapTTLFromFlags(cmd)
			if err != nil {
				return err
			}
			selection, err := findWatchReap(ttl, watchReapNow())
			if err != nil {
				return err
			}
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			printWatchReapSelection(cmd.OutOrStdout(), selection, dryRun)
			if dryRun || len(selection.Candidates) == 0 {
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Kill all %d panes? [y/N] ", len(selection.Candidates))
			confirmed, err := confirmWatchReap(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if !confirmed {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "No panes reaped.")
				return err
			}

			report, reapErr := reapWatchPanes(selection.Candidates)
			printWatchReapReport(cmd.OutOrStdout(), report)
			return reapErr
		},
	}
	reap.Flags().Bool("dry-run", false, "Show stale panes without killing them")
	reap.Flags().String("ttl", "", "Screen-change threshold override (e.g. 1h, 90m, 1d); defaults to config")
	return reap
}

func watchReapTTLFromFlags(cmd *cobra.Command) (time.Duration, error) {
	raw, _ := cmd.Flags().GetString("ttl")
	if strings.TrimSpace(raw) != "" {
		return config.ParseTTL(raw)
	}
	cfg, err := config.Load()
	if err != nil {
		return 0, fmt.Errorf("loading config: %w", err)
	}
	return cfg.Watch.ReapTTL.Duration(), nil
}

func printWatchReapSelection(w io.Writer, selection attn.ReapSelection, dryRun bool) {
	switch {
	case len(selection.Candidates) == 0:
		fmt.Fprintln(w, "No stale agent panes matched.")
	case dryRun:
		fmt.Fprintf(w, "Would reap %d stale agent panes:\n", len(selection.Candidates))
	default:
		fmt.Fprintf(w, "Stale agent panes (%d):\n", len(selection.Candidates))
	}
	for _, candidate := range selection.Candidates {
		fmt.Fprintf(w, "  %-32s unchanged %s\n", candidate.DisplayName(), formatWatchAge(candidate.Age))
	}
	if selection.MissingTimestamps > 0 {
		fmt.Fprintf(
			w,
			"Skipped %d attention %s without a last-change timestamp.\n",
			selection.MissingTimestamps,
			plural(selection.MissingTimestamps, "pane", "panes"),
		)
	}
}

func confirmWatchReap(r io.Reader) (bool, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return false, scanner.Err()
	}
	answer := strings.TrimSpace(scanner.Text())
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func printWatchReapReport(w io.Writer, report attn.ReapReport) {
	fmt.Fprintf(w, "Reaped %d stale agent panes.\n", len(report.Removed))
	if len(report.Protected) > 0 {
		fmt.Fprintf(
			w,
			"Protected %d %s focused after selection.\n",
			len(report.Protected),
			plural(len(report.Protected), "pane", "panes"),
		)
	}
	if len(report.Failed) > 0 {
		fmt.Fprintf(w, "Failed to reap %d:\n", len(report.Failed))
		for _, failure := range report.Failed {
			fmt.Fprintf(w, "  %-32s %v\n", failure.Candidate.DisplayName(), failure.Err)
		}
	}
}

func formatWatchAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd", int(age.Hours()/24))
	}
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}
