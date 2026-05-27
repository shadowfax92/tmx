package cmd

import (
	"fmt"

	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

func init() {
	renameCmd.Flags().Bool("clear", false, "Unset the pane label")
	renameCmd.Flags().BoolP("window", "w", false, "Also rename the current window (disables automatic-rename)")
	rootCmd.AddCommand(renameCmd)
}

var renameCmd = &cobra.Command{
	Use:         "rename [label]",
	Aliases:     []string{"rn"},
	Annotations: map[string]string{"group": "Navigate:"},
	Short:       "Label the current tmux pane",
	Long: `Set @pane_label for the current tmux pane (rendered by pane-border-format).

  tmx rename           — auto-detect label from current pane cwd
  tmx rename <label>   — set an explicit label
  tmx rename --clear   — unset the pane label
  tmx rename -w        — also rename the window and disable automatic-rename

Auto-detect order:
  1. git repo on a feature branch → branch name
  2. git repo on main/master/trunk → repo folder name
  3. detached HEAD → <repo>@<short-sha>
  4. $HOME → "home"
  5. any other dir → folder name (basename of cwd)`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !tmux.IsInsideTmux() {
			return fmt.Errorf("tmx rename must run inside tmux")
		}

		clear, _ := cmd.Flags().GetBool("clear")
		alsoWindow, _ := cmd.Flags().GetBool("window")

		if clear {
			if len(args) > 0 {
				return fmt.Errorf("--clear takes no label argument")
			}
			if err := tmux.UnsetCurrentPaneLabel(); err != nil {
				return fmt.Errorf("clearing pane label: %w", err)
			}
			fmt.Println("pane label cleared")
			return nil
		}

		var label string
		if len(args) == 1 {
			label = args[0]
		} else {
			resolved, err := autoPaneLabel()
			if err != nil {
				return err
			}
			if resolved == "" {
				return fmt.Errorf("could not infer a pane label")
			}
			label = resolved
		}

		if err := tmux.SetCurrentPaneLabel(label); err != nil {
			return fmt.Errorf("setting pane label: %w", err)
		}

		if alsoWindow {
			if err := tmux.RenameCurrentWindow(label); err != nil {
				return fmt.Errorf("renaming window: %w", err)
			}
			if err := tmux.DisableCurrentWindowAutoRename(); err != nil {
				return fmt.Errorf("disabling automatic-rename: %w", err)
			}
		}

		fmt.Printf("pane label: %s\n", label)
		return nil
	},
}
