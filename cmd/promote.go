package cmd

import (
	"fmt"

	"tmx/internal/scratch"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(promoteCmd)
}

var promoteCmd = &cobra.Command{
	Use:         "promote [name]",
	Annotations: map[string]string{"group": "Scratch:"},
	Short:       "Promote the current scratch (gs/) session to a regular session",
	Long: `Rename the current scratch session into a regular tmux session and strip its
scratch metadata so it's no longer reaped.

  tmx promote         — auto-name from cwd (branch, folder, or "home")
  tmx promote admin   — explicit name

Only works when the current session is a gs/ scratch session.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !tmux.IsInsideTmux() {
			return fmt.Errorf("tmx promote must run inside tmux")
		}

		session, err := tmux.CurrentSession()
		if err != nil {
			return fmt.Errorf("reading current session: %w", err)
		}
		if !scratch.IsSession(session) {
			return fmt.Errorf("current session %q is not a scratch (gs/) session", session)
		}

		var name string
		if len(args) == 1 {
			name = args[0]
		} else {
			label, err := autoPaneLabel()
			if err != nil {
				return err
			}
			if label == "" {
				return fmt.Errorf("could not infer a name — pass one explicitly")
			}
			name = label
		}

		if tmux.SessionExists(name) {
			return fmt.Errorf("session %q already exists", name)
		}

		if err := tmux.RenameSession(session, name); err != nil {
			return fmt.Errorf("renaming session: %w", err)
		}

		// Strip scratch metadata so reap no longer considers it a candidate.
		for _, key := range []string{
			"shadow_cwd", "shadow_parent_pane", "shadow_env_version",
			"shadow_opened_at", "shadow_last_toggled_at", "shadow_client_name",
		} {
			_ = tmux.UnsetSessionVar(name, key)
		}

		fmt.Printf("promoted %s → %s\n", session, name)
		return nil
	},
}
