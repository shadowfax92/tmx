package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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
	)
	return watch
}
