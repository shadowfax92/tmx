package cmd

import (
	"fmt"
	"strings"

	"tmx/internal/config"
	"tmx/internal/mux"
	"tmx/internal/scratch"

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

The optional client/session/pane args may be supplied by keybinds; when omitted
they are resolved from the current multiplexer context.`,
	Args: cobra.RangeArgs(1, 4),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScratch(args)
	},
}

// runScratch toggles a configured scratch popup in the active multiplexer.
func runScratch(args []string) error {
	backend, err := mux.SelectScratchBackend()
	if err != nil {
		return err
	}
	return runScratchWithBackend(args, backend)
}

func runScratchWithBackend(args []string, backend mux.ScratchBackend) error {
	return scratch.WithBackend(backend, func() error {
		typ := args[0]
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		if !cfg.Scratch.HasType(typ) {
			return fmt.Errorf("unknown scratch type %q; configured types: %s",
				typ, strings.Join(cfg.Scratch.Types(), ", "))
		}

		clientName, currentSession, activePane, err := resolveScratchContext(args, backend)
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
			if err := backend.ClosePopup(popupClient); err != nil {
				return fmt.Errorf("closing popup: %w", err)
			}
			if currentSession == targetSession {
				return nil
			}
		}

		if !backend.PaneExists(parentPane) {
			_, err := scratch.Reap(scratch.ReapOptions{})
			return err
		}

		paneCwd, err := backend.PaneCwd(parentPane)
		if err != nil {
			return fmt.Errorf("getting pane cwd: %w", err)
		}

		popupCmd := cfg.Scratch.CmdFor(typ)
		if err := scratch.Ensure(targetSession, paneCwd, typ, parentPane, popupCmd); err != nil {
			return err
		}
		if err := scratch.MarkToggled(targetSession); err != nil {
			return fmt.Errorf("storing scratch toggle timestamp: %w", err)
		}
		if err := backend.SetSessionVar(targetSession, "shadow_client_name", popupClient); err != nil {
			return fmt.Errorf("storing scratch client: %w", err)
		}

		attachCmd := backend.PopupAttachCommand(targetSession)
		size := cfg.Scratch.PopupFor(typ)
		return backend.DisplayPopup(popupClient, size.Width, size.Height, attachCmd)
	})
}

// resolveScratchContext reads explicit keybind context or asks the active backend.
func resolveScratchContext(args []string, backend mux.ScratchBackend) (client, session, pane string, err error) {
	if len(args) > 1 && args[1] != "" {
		client = args[1]
	} else if client, err = backend.CurrentClient(); err != nil {
		return "", "", "", fmt.Errorf("resolving client: %w", err)
	}
	if len(args) > 2 && args[2] != "" {
		session = args[2]
	} else if session, err = backend.CurrentSession(); err != nil {
		return "", "", "", fmt.Errorf("resolving session: %w", err)
	}
	if len(args) > 3 && args[3] != "" {
		pane = args[3]
	} else if pane, err = backend.PaneID(); err != nil {
		return "", "", "", fmt.Errorf("resolving pane: %w", err)
	}
	return client, session, pane, nil
}
