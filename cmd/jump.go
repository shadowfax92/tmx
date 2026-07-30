package cmd

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"tmx/internal/attn"
	"tmx/internal/config"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

type jumpBackend interface {
	CurrentClient() (string, error)
	PaneExists(string) bool
	SwitchClient(client, session string) error
	SelectWindow(string) error
	SelectPane(string) error
	PaneWindowZoomed(string) (bool, error)
	TogglePaneZoom(string) error
	DisplayMessage(client, message string) error
	RunShell(string) error
}

type tmuxJumpBackend struct{}

func (tmuxJumpBackend) CurrentClient() (string, error) { return tmux.CurrentClient() }
func (tmuxJumpBackend) PaneExists(target string) bool  { return tmux.PaneExists(target) }
func (tmuxJumpBackend) SwitchClient(client, session string) error {
	return tmux.SwitchClientFor(client, session)
}
func (tmuxJumpBackend) SelectWindow(target string) error { return tmux.SelectWindow(target) }
func (tmuxJumpBackend) SelectPane(target string) error   { return tmux.SelectPane(target) }
func (tmuxJumpBackend) PaneWindowZoomed(target string) (bool, error) {
	return tmux.PaneWindowZoomed(target)
}
func (tmuxJumpBackend) TogglePaneZoom(target string) error { return tmux.TogglePaneZoom(target) }
func (tmuxJumpBackend) DisplayMessage(client, message string) error {
	return tmux.DisplayMessage(client, message)
}
func (tmuxJumpBackend) RunShell(command string) error {
	output, err := exec.Command("/bin/sh", "-c", command).CombinedOutput()
	if err != nil {
		return fmt.Errorf("focus command: %s (%w)", strings.TrimSpace(string(output)), err)
	}
	return nil
}

type jumpDeps struct {
	backend  jumpBackend
	snapshot func() ([]attn.PaneState, error)
	markRead func(attn.PaneState) error
}

func init() {
	rootCmd.AddCommand(newJumpCommand())
}

func newJumpCommand() *cobra.Command {
	return &cobra.Command{
		Use:         "jump",
		Annotations: map[string]string{"group": "Navigate:"},
		Short:       "Jump to the oldest unread agent pane",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			action, _ := cfg.Scratch.ResolveJumpAction()
			return runJump(action, cfg.Watch.FocusCommand, jumpDeps{
				backend:  tmuxJumpBackend{},
				snapshot: attn.Snapshot,
				markRead: markPaneRead,
			})
		},
	}
}

func runJump(action config.JumpAction, focusCommand string, deps jumpDeps) error {
	states, err := deps.snapshot()
	if err != nil {
		return err
	}
	unread := make([]attn.PaneState, 0, len(states))
	for _, state := range states {
		if state.State == attn.StateUnread {
			unread = append(unread, state)
		}
	}
	sort.SliceStable(unread, func(i, j int) bool { return unread[i].Since < unread[j].Since })

	client, err := deps.backend.CurrentClient()
	if err != nil {
		return fmt.Errorf("resolving invoking client: %w", err)
	}
	for _, target := range unread {
		jumped, err := jumpToTarget(action, focusCommand, client, target, deps)
		if err != nil {
			return err
		}
		if jumped {
			return nil
		}
	}
	return deps.backend.DisplayMessage(client, "inbox zero")
}

// jumpToTarget is the shared selected-pane path for `jump` and `inbox`.
// It returns false when the pane disappears during navigation so queue-based
// callers can continue to the next live candidate.
func jumpToTarget(
	action config.JumpAction,
	focusCommand string,
	client string,
	target attn.PaneState,
	deps jumpDeps,
) (bool, error) {
	if !deps.backend.PaneExists(target.ID) {
		return false, nil
	}
	if err := focusJumpTarget(deps.backend, client, target); err != nil {
		if !deps.backend.PaneExists(target.ID) {
			return false, nil
		}
		return false, err
	}
	if target.State == attn.StateUnread {
		if err := deps.markRead(target); err != nil {
			if !deps.backend.PaneExists(target.ID) {
				return false, nil
			}
			return false, fmt.Errorf("clearing unread on %s: %w", target.ID, err)
		}
	}
	if err := applyJumpAction(deps.backend, action, focusCommand, target.ID); err != nil {
		if !deps.backend.PaneExists(target.ID) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func focusJumpTarget(backend jumpBackend, client string, target attn.PaneState) error {
	if err := backend.SwitchClient(client, target.Session); err != nil {
		return fmt.Errorf("switching client to %s: %w", target.Session, err)
	}
	window := fmt.Sprintf("=%s:%d", target.Session, target.WindowIndex)
	if err := backend.SelectWindow(window); err != nil {
		return fmt.Errorf("selecting window %s: %w", window, err)
	}
	if err := backend.SelectPane(target.ID); err != nil {
		return fmt.Errorf("selecting pane %s: %w", target.ID, err)
	}
	return nil
}

func applyJumpAction(backend jumpBackend, action config.JumpAction, focusCommand, paneID string) error {
	switch action {
	case config.JumpActionZoom:
		zoomed, err := backend.PaneWindowZoomed(paneID)
		if err != nil {
			return err
		}
		if !zoomed {
			return backend.TogglePaneZoom(paneID)
		}
	case config.JumpActionFocus:
		if strings.TrimSpace(focusCommand) != "" {
			return backend.RunShell(strings.ReplaceAll(focusCommand, "{pane}", paneID))
		}
	}
	return nil
}

func markPaneRead(state attn.PaneState) error {
	state.State = attn.StateQuiet
	state.Since = time.Now().Unix()
	state.Fired = true
	return attn.Set(state.ID, state)
}
