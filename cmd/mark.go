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

const (
	mutationRead    attentionMutation = "read"
	mutationUnread  attentionMutation = "unread"
	mutationSnooze  attentionMutation = "snooze"
	mutationUnwatch attentionMutation = "unwatch"
)

var (
	getAttentionState       = attn.Get
	setAttentionState       = attn.Set
	updateAttentionIfUnread = attn.UpdateIfUnread
	discoverAttentionPanes  = attn.DiscoverWithFingerprints
	currentAttentionPane    = tmux.PaneID
	insideTmux              = tmux.IsInsideTmux
	attentionNow            = time.Now
	loadAttentionAgents     = func() ([]string, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		return cfg.Watch.Agents, nil
	}
)

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
		return nil
	}
	// This command is intended for a high-frequency tmux hook. Its failure
	// modes (including a pane disappearing mid-focus-hop) are all silent.
	_ = updateAttentionIfUnread(target, markAttentionRead)
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

	now := attentionNow().Unix()
	next := state
	switch mutation {
	case mutationRead:
		next = markAttentionReadAt(next, now)
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
	case mutationUnwatch:
		next.Watch = false
		next.State = attn.StateQuiet
		next.Since = now
		next.Fired = true
	default:
		return fmt.Errorf("unknown attention mutation %q", mutation)
	}

	if err := setAttentionState(state.ID, next); err != nil {
		return fmt.Errorf("%s pane %s: %w", mutation, state.ID, err)
	}
	return nil
}

func markAttentionRead(state attn.PaneState) attn.PaneState {
	return markAttentionReadAt(state, attentionNow().Unix())
}

func markAttentionReadAt(state attn.PaneState, now int64) attn.PaneState {
	state.State = attn.StateQuiet
	state.Since = now
	state.Fired = true
	return state
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
		state.Hash != "" || state.Proc != "" || state.Fired
}
