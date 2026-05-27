package cmd

import (
	"fmt"
	"strings"

	"tmx/internal/config"
	"tmx/internal/scratch"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(scratchCmd)
}

var scratchCmd = &cobra.Command{
	Use:         "scratch <type> [client] [session] [pane]",
	Aliases:     []string{"sc"},
	Annotations: map[string]string{"group": "Scratch:"},
	Short:       "Toggle a recreatable popup session for the current pane",
	Long: `Toggle a scratch popup bound to the current pane. <type> selects an entry
from config (scratch.popups) — the command to run and the popup size.

  tmx scratch vim    — toggle the "vim" popup (nvim by default)
  tmx scratch sh     — toggle a shell popup
  tmx scratch git    — toggle a custom popup, if configured

Toggling from outside the popup opens it; toggling from inside closes it. The
popup follows the pane's project: if the pane's cwd changed, it is recreated.

The optional client/session/pane args are supplied by the tmux keybind that
'tmx init' installs (via #{client_name}/#{session_name}/#{pane_id}); when
omitted they are resolved from the current tmux context.`,
	Args: cobra.RangeArgs(1, 4),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !tmux.IsInsideTmux() {
			return fmt.Errorf("tmx scratch must run inside tmux")
		}

		typ := args[0]
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		if !cfg.Scratch.HasType(typ) {
			return fmt.Errorf("unknown scratch type %q; configured types: %s",
				typ, strings.Join(cfg.Scratch.Types(), ", "))
		}

		clientName, currentSession, activePane, err := resolveScratchContext(args)
		if err != nil {
			return err
		}

		popupClient, err := scratch.PopupClient(currentSession, clientName)
		if err != nil {
			return err
		}
		parentPane, err := scratch.ParentPane(currentSession, activePane)
		if err != nil {
			return err
		}

		targetSession := scratch.Name(parentPane, typ)
		if scratch.IsSession(currentSession) {
			if currentSession == targetSession {
				if err := scratch.MarkToggled(currentSession); err != nil {
					return fmt.Errorf("storing scratch toggle timestamp: %w", err)
				}
			}
			if err := tmux.ClosePopup(popupClient); err != nil {
				return fmt.Errorf("closing popup: %w", err)
			}
			if currentSession == targetSession {
				return nil
			}
		}

		if !tmux.PaneExists(parentPane) {
			_, err := scratch.Reap(scratch.ReapOptions{})
			return err
		}

		paneCwd, err := tmux.PaneCwd(parentPane)
		if err != nil {
			return fmt.Errorf("getting pane cwd: %w", err)
		}

		// Self-heal the scratch namespace on every open (orphan/idle/dead-cwd),
		// so the gs/ space stays clean without relying on a tmux hook.
		scratch.ReapOnToggle(cfg.Scratch.TTL.Duration())

		popupCmd := cfg.Scratch.CmdFor(typ)
		if err := scratch.Ensure(targetSession, paneCwd, typ, parentPane, popupCmd); err != nil {
			return err
		}
		if err := scratch.MarkToggled(targetSession); err != nil {
			return fmt.Errorf("storing scratch toggle timestamp: %w", err)
		}
		if err := tmux.SetSessionVar(targetSession, "shadow_client_name", popupClient); err != nil {
			return fmt.Errorf("storing scratch client: %w", err)
		}

		attachCmd := fmt.Sprintf("exec tmux attach-session -t '=%s'", targetSession)
		size := cfg.Scratch.PopupFor(typ)
		return tmux.DisplayPopup(popupClient, size.Width, size.Height, attachCmd)
	},
}

// resolveScratchContext reads client/session/pane from positional args (passed
// by the tmux keybind, where they're pre-interpolated and reliable) or resolves
// them from the current tmux context when invoked by hand.
func resolveScratchContext(args []string) (client, session, pane string, err error) {
	if len(args) > 1 && args[1] != "" {
		client = args[1]
	} else if client, err = tmux.CurrentClient(); err != nil {
		return "", "", "", fmt.Errorf("resolving client: %w", err)
	}
	if len(args) > 2 && args[2] != "" {
		session = args[2]
	} else if session, err = tmux.CurrentSession(); err != nil {
		return "", "", "", fmt.Errorf("resolving session: %w", err)
	}
	if len(args) > 3 && args[3] != "" {
		pane = args[3]
	} else if pane, err = tmux.PaneID(); err != nil {
		return "", "", "", fmt.Errorf("resolving pane: %w", err)
	}
	return client, session, pane, nil
}
