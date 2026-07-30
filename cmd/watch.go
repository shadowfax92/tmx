package cmd

import (
	"bufio"
	"context"
	"encoding/json"
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

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

var errWatcherStopped = errors.New("watcher stopped")

const emptyWatchListMessage = "No tracked agent panes."

var (
	startWatchDaemon  = attn.StartDaemon
	stopWatchDaemon   = attn.StopDaemon
	statusWatchDaemon = attn.StatusDaemon
	runWatchDaemon    = attn.RunDaemon
	listWatchPanes    = attn.Snapshot
	findWatchReap     = attn.FindReapCandidates
	reapWatchPanes    = attn.ReapPanes
	watchListNow      = time.Now
	watchReapNow      = time.Now
	watchStopPick     = runFzfMulti
	watchStopTerminal = func(w io.Writer) bool {
		file, ok := w.(*os.File)
		return ok && term.IsTerminal(file.Fd())
	}
	watchStopUnwatch = func(target string) error {
		return mutateAttentionTarget(target, mutationUnwatch)
	}
)

type watchListEntry struct {
	inboxEntry
	Process string
}

type watchListJSONEntry struct {
	Target     string `json:"target"`
	State      string `json:"state"`
	Age        string `json:"age"`
	AgeSeconds int64  `json:"age_seconds"`
	Since      int64  `json:"since"`
	Process    string `json:"process"`
	Label      string `json:"label"`
}

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
		newWatchStopCommand(),
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
					fmt.Fprintf(cmd.OutOrStdout(), "watcher stopped\nlog: %s\n", status.LogFile)
					return errWatcherStopped
				}
				fmt.Fprintf(cmd.OutOrStdout(), "watcher running (pid %d)\nlog: %s\n", status.PID, status.LogFile)
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
		newWatchListCommand(),
		newWatchReapCommand(),
	)
	return watch
}

func newWatchStopCommand() *cobra.Command {
	stop := &cobra.Command{
		Use:   "stop [target...]",
		Short: "Stop watching one or more agent panes",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			flagTargets, _ := cmd.Flags().GetStringArray("target")
			targets := append(flagTargets, args...)
			daemon, _ := cmd.Flags().GetBool("daemon")
			if daemon {
				if len(targets) > 0 {
					return fmt.Errorf("--daemon cannot be combined with pane targets")
				}
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
			}
			if len(targets) > 0 {
				return unwatchTargets(targets)
			}
			if !watchStopTerminal(cmd.OutOrStdout()) {
				return fmt.Errorf("watch stop needs a TTY to choose panes; pass -t <target> or positional targets")
			}

			states, err := listWatchPanes()
			if err != nil {
				return err
			}
			states = watchedWatchStopStates(states)
			sortInboxStates(states)
			if len(states) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), emptyInboxMessage)
				return err
			}

			lines := make([]string, 0, len(states))
			for _, state := range states {
				lines = append(lines, formatInboxPickerLine(makeInboxEntry(state, watchListNow())))
			}
			selected, err := watchStopPick(
				"watch stop > ",
				lines,
				append(paneFzfArgs(), "-m"),
			)
			if errors.Is(err, ErrCancelled) {
				return nil
			}
			if err != nil {
				return err
			}
			return unwatchTargets(selected)
		},
	}
	stop.Flags().StringArrayP("target", "t", nil, "Target pane, window, or session (repeatable)")
	stop.Flags().Bool("daemon", false, "Stop the detached watcher daemon")
	return stop
}

func watchedWatchStopStates(states []attn.PaneState) []attn.PaneState {
	watched := make([]attn.PaneState, 0, len(states))
	for _, state := range states {
		if state.Watch {
			watched = append(watched, state)
		}
	}
	return watched
}

func unwatchTargets(targets []string) error {
	var errs []error
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			errs = append(errs, fmt.Errorf("empty watch target"))
			continue
		}
		if err := watchStopUnwatch(target); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newWatchListCommand() *cobra.Command {
	list := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"l"},
		Short:   "List panes tracked by the attention watcher",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			states, err := listWatchPanes()
			if err != nil {
				return err
			}
			states = watchedInboxStates(states)
			sortInboxStates(states)

			entries := make([]watchListEntry, 0, len(states))
			for _, state := range states {
				entry := watchListEntry{inboxEntry: makeInboxEntry(state, watchListNow())}
				entry.Process = sanitizeInboxField(state.Proc)
				if entry.Process == "" {
					entry.Process = "-"
				}
				entries = append(entries, entry)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			quiet, _ := cmd.Flags().GetBool("quiet")
			switch {
			case jsonOut:
				return printWatchListJSON(cmd.OutOrStdout(), entries)
			case quiet:
				for _, entry := range entries {
					fmt.Fprintln(cmd.OutOrStdout(), entry.Target)
				}
			case len(entries) == 0:
				_, err = fmt.Fprintln(cmd.OutOrStdout(), emptyWatchListMessage)
				return err
			default:
				printWatchList(cmd.OutOrStdout(), entries)
			}
			return nil
		},
	}
	list.Flags().Bool("json", false, "Print tracked panes as JSON")
	list.Flags().BoolP("quiet", "q", false, "Print targets only")
	return list
}

func printWatchList(w io.Writer, entries []watchListEntry) {
	targetWidth, stateWidth, ageWidth, processWidth := 6, 5, 3, 7
	for _, entry := range entries {
		targetWidth = max(targetWidth, len(entry.Target))
		stateWidth = max(stateWidth, len(entry.State))
		ageWidth = max(ageWidth, len(entry.Age))
		processWidth = max(processWidth, len(entry.Process))
	}
	for _, entry := range entries {
		fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %s\n",
			targetWidth, entry.Target,
			stateWidth, entry.State,
			ageWidth, entry.Age,
			processWidth, entry.Process,
			entry.Label,
		)
	}
}

func printWatchListJSON(w io.Writer, entries []watchListEntry) error {
	output := make([]watchListJSONEntry, 0, len(entries))
	for _, entry := range entries {
		output = append(output, watchListJSONEntry{
			Target: entry.Target, State: entry.State, Age: entry.Age,
			AgeSeconds: entry.AgeSecs, Since: entry.PaneState.Since,
			Process: entry.Process, Label: entry.Label,
		})
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
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
