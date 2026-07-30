package cmd

import (
	"fmt"
	"strings"
	"time"

	"tmx/internal/attn"
	"tmx/internal/config"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

type attentionMutation string

type attentionReadBaseline struct {
	Hash    string
	Lines   int
	Process string
}

const (
	mutationRead    attentionMutation = "read"
	mutationUnread  attentionMutation = "unread"
	mutationSnooze  attentionMutation = "snooze"
	mutationUnwatch attentionMutation = "unwatch"
)

var (
	getAttentionState        = attn.Get
	setAttentionState        = attn.Set
	updateAttentionIfUnread  = attn.UpdateIfUnread
	discoverAttentionPanes   = attn.DiscoverWithFingerprints
	currentAttentionPane     = tmux.PaneID
	insideTmux               = tmux.IsInsideTmux
	attentionNow             = time.Now
	captureAttentionScreen   = attn.CaptureScreenHash
	captureAttentionBaseline = func(target string) (attentionReadBaseline, error) {
		cfg, err := config.Load()
		if err != nil {
			return attentionReadBaseline{}, fmt.Errorf("loading attention config: %w", err)
		}
		return captureStableAttentionBaseline(
			target, cfg.Watch.CaptureLines, cfg.Watch.Agents,
		)
	}
	logAttentionEvent   = attn.AppendWatcherLog
	loadAttentionAgents = func() ([]string, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		return cfg.Watch.Agents, nil
	}
)

func captureStableAttentionBaseline(
	target string,
	lines int,
	agents []string,
) (attentionReadBaseline, error) {
	before, err := attentionProcessFingerprint(target, agents)
	if err != nil {
		return attentionReadBaseline{}, fmt.Errorf("sampling agent process before capture: %w", err)
	}
	hash, err := captureAttentionScreen(target, lines)
	if err != nil {
		return attentionReadBaseline{}, err
	}
	after, err := attentionProcessFingerprint(target, agents)
	if err != nil {
		return attentionReadBaseline{}, fmt.Errorf("sampling agent process after capture: %w", err)
	}
	if before != after {
		return attentionReadBaseline{}, fmt.Errorf(
			"agent process changed while capturing pane %s read baseline (%q -> %q)",
			target, before, after,
		)
	}
	return attentionReadBaseline{Hash: hash, Lines: lines, Process: after}, nil
}

func attentionProcessFingerprint(target string, agents []string) (string, error) {
	panes, err := discoverAttentionPanes(agents)
	if err != nil {
		return "", err
	}
	for _, pane := range panes {
		if pane.ID == target {
			if pane.ProcessFingerprint == "" {
				return "", fmt.Errorf("pane %s has an empty agent process fingerprint", target)
			}
			return pane.ProcessFingerprint, nil
		}
	}
	return "", fmt.Errorf("pane %s is not a discovered agent pane", target)
}

func init() {
	rootCmd.AddCommand(newMarkCommand(), newSnoozeCommand(), newUnwatchCommand())
}

func newMarkCommand() *cobra.Command {
	command := &cobra.Command{
		Use:         "mark",
		Annotations: map[string]string{"group": "Other:"},
		Short:       "Mark an agent pane read or unread",
		Args:        cobra.NoArgs,
	}
	command.PersistentFlags().StringP("target", "t", "", "Target pane, window, or session (defaults to current pane)")
	readCommand := &cobra.Command{
		Use:   "read",
		Short: "Clear an agent pane's unread flag",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ifUnread, _ := cmd.Flags().GetBool("if-unread")
			if ifUnread {
				return markReadIfUnread(cmd)
			}
			return mutateAttention(cmd, mutationRead)
		},
	}
	readCommand.Flags().Bool("if-unread", false, "Clear only if the pane is unread; otherwise exit silently")
	command.AddCommand(
		readCommand,
		&cobra.Command{
			Use:   "unread",
			Short: "Flag and re-arm an agent pane",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return mutateAttention(cmd, mutationUnread)
			},
		},
	)
	return command
}

func newSnoozeCommand() *cobra.Command {
	command := &cobra.Command{
		Use:         "snooze",
		Annotations: map[string]string{"group": "Other:"},
		Short:       "Move an agent pane to the back of the unread queue",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateAttention(cmd, mutationSnooze)
		},
	}
	command.Flags().StringP("target", "t", "", "Target pane, window, or session (defaults to current pane)")
	return command
}

func newUnwatchCommand() *cobra.Command {
	command := &cobra.Command{
		Use:         "unwatch",
		Annotations: map[string]string{"group": "Other:"},
		Short:       "Stop monitoring an agent pane",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateAttention(cmd, mutationUnwatch)
		},
	}
	command.Flags().StringP("target", "t", "", "Target pane, window, or session (defaults to current pane)")
	return command
}

func mutateAttention(cmd *cobra.Command, mutation attentionMutation) error {
	target, err := attentionTarget(cmd)
	if err != nil {
		return err
	}
	return mutateAttentionTarget(target, mutation)
}

func markReadIfUnread(cmd *cobra.Command) error {
	target, err := attentionTarget(cmd)
	if err != nil {
		_ = logAttentionEvent("read failed target=current error=%v", err)
		return nil
	}
	// This command is intended for a high-frequency tmux hook. Its failure
	// modes (including a pane disappearing mid-focus-hop) stay silent in tmux,
	// but are recorded in the watcher log for diagnosis.
	var baseline attentionReadBaseline
	var prepareErr error
	err = updateAttentionIfUnread(target, func(state attn.PaneState) attn.PaneState {
		var next attn.PaneState
		next, baseline, prepareErr = prepareAttentionRead(state)
		if prepareErr != nil {
			return state
		}
		return next
	})
	if err != nil {
		_ = logAttentionEvent("read failed target=%s error=%v", target, err)
		return nil
	}
	if prepareErr != nil {
		_ = logAttentionEvent("read failed target=%s error=%v", target, prepareErr)
		return nil
	}
	if baseline.Hash != "" {
		_ = logAttentionEvent(
			"read acknowledged pane=%s baseline=%s lines=%d proc=%q",
			target, shortAttentionHash(baseline.Hash), baseline.Lines, baseline.Process,
		)
	}
	return nil
}

func mutateAttentionTarget(target string, mutation attentionMutation) error {
	state, err := getAttentionState(target)
	if err != nil {
		return fmt.Errorf("resolving attention target %q: %w", target, err)
	}

	if mutation == mutationRead && !hasAttentionState(state) {
		return nil
	}
	if mutation == mutationRead {
		if err := markPaneRead(state); err != nil {
			return fmt.Errorf("%s pane %s: %w", mutation, state.ID, err)
		}
		return nil
	}

	now := attentionNow().Unix()
	next := state
	switch mutation {
	case mutationUnread, mutationSnooze:
		process, err := requireAgentPane(state.ID)
		if err != nil {
			return err
		}
		next.Watch = true
		next.State = attn.StateUnread
		next.Since = now
		next.Proc = process
		next.Fired = true
		next.ReadHash = ""
		next.ReadLines = 0
	case mutationUnwatch:
		next.Watch = false
		next.State = attn.StateQuiet
		next.Since = now
		next.Fired = true
		next.ReadHash = ""
		next.ReadLines = 0
	default:
		return fmt.Errorf("unknown attention mutation %q", mutation)
	}

	if err := setAttentionState(state.ID, next); err != nil {
		return fmt.Errorf("%s pane %s: %w", mutation, state.ID, err)
	}
	return nil
}

func prepareAttentionRead(state attn.PaneState) (attn.PaneState, attentionReadBaseline, error) {
	baseline, err := captureAttentionBaseline(state.ID)
	if err != nil {
		return state, attentionReadBaseline{}, fmt.Errorf("capturing pane %s read baseline: %w", state.ID, err)
	}
	if baseline.Process == "" {
		return state, attentionReadBaseline{}, fmt.Errorf(
			"capturing pane %s read baseline: missing agent process fingerprint",
			state.ID,
		)
	}
	next := markAttentionReadAt(state, attentionNow().Unix())
	next.ReadHash = baseline.Hash
	next.ReadLines = baseline.Lines
	next.Proc = baseline.Process
	return next, baseline, nil
}

func markAttentionReadAt(state attn.PaneState, now int64) attn.PaneState {
	state.State = attn.StateQuiet
	state.Since = now
	state.Fired = true
	return state
}

func shortAttentionHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func attentionTarget(cmd *cobra.Command) (string, error) {
	target, _ := cmd.Flags().GetString("target")
	if target = strings.TrimSpace(target); target != "" {
		return target, nil
	}
	if !insideTmux() {
		return "", fmt.Errorf("no current pane outside tmux; pass -t <pane|window|session>")
	}
	target, err := currentAttentionPane()
	if err != nil {
		return "", fmt.Errorf("resolving current pane: %w", err)
	}
	if target == "" {
		return "", fmt.Errorf("resolving current pane: tmux returned an empty pane id")
	}
	return target, nil
}

func requireAgentPane(paneID string) (string, error) {
	agents, err := loadAttentionAgents()
	if err != nil {
		return "", fmt.Errorf("loading attention config: %w", err)
	}
	panes, err := discoverAttentionPanes(agents)
	if err != nil {
		return "", fmt.Errorf("discovering agent panes: %w", err)
	}
	for _, pane := range panes {
		if pane.ID == paneID {
			return pane.ProcessFingerprint, nil
		}
	}
	return "", fmt.Errorf("pane %s is not an agent pane; manual watch-add is not supported", paneID)
}

func hasAttentionState(state attn.PaneState) bool {
	return state.WatchSet || state.State != "" || state.Since != 0 ||
		state.Hash != "" || state.Proc != "" || state.Fired ||
		state.ReadHash != "" || state.ReadLines != 0
}
