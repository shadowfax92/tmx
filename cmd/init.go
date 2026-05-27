package cmd

import (
	"fmt"
	"os"
	"strconv"

	"tmx/internal/config"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

func init() {
	initCmd.Flags().Bool("no-jump", false, "Skip binding the M-s/M-w/M-p jump popups")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:         "init",
	Annotations: map[string]string{"group": "Setup:"},
	Short:       "Install tmx tmux keybindings (scratch toggles + jump popups)",
	Long: `Bind tmx keys in the running tmux server. Idempotent — safe to re-run.

Scratch toggles come from config (scratch.keys). Jump popups (unless --no-jump):
  M-s → tmx        session tree
  M-w → tmx -w     windows
  M-p → tmx -p     panes

Live binds don't survive a tmux server restart. To persist, add to ~/.tmux.conf:
  run-shell 'tmx init'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		selfPath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("finding executable path: %w", err)
		}
		self := strconv.Quote(selfPath)
		noJump, _ := cmd.Flags().GetBool("no-jump")

		var bound []string

		// Scratch toggles. Pass client/session/pane interpolated by tmux so the
		// toggle resolves against the pane that pressed the key, not whatever the
		// detached run-shell happens to see.
		for _, typ := range cfg.Scratch.Types() {
			key := cfg.Scratch.Keys[typ]
			if key == "" {
				continue
			}
			toggle := fmt.Sprintf(`%s scratch %s "#{client_name}" "#{session_name}" "#{pane_id}" >/dev/null 2>&1 || true`, self, typ)
			if err := tmux.BindKeyRaw("-n", key, "run-shell", "-b", toggle); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to bind %s (scratch %s): %v\n", key, typ, err)
				continue
			}
			bound = append(bound, fmt.Sprintf("%-4s → scratch %s", key, typ))
		}

		if !noJump {
			jumpBinds := []struct{ key, suffix, desc string }{
				{"M-s", "", "tmx (session tree)"},
				{"M-w", " -w", "tmx -w (windows)"},
				{"M-p", " -p", "tmx -p (panes)"},
			}
			for _, jb := range jumpBinds {
				// Bind the key directly to display-popup; the -E command string
				// is `"/path/to/tmx"[ -w|-p]`, which tmux runs via sh -c.
				if err := tmux.BindKeyRaw("-n", jb.key, "display-popup", "-E", self+jb.suffix); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to bind %s (%s): %v\n", jb.key, jb.desc, err)
					continue
				}
				bound = append(bound, fmt.Sprintf("%-4s → %s", jb.key, jb.desc))
			}
		}

		if len(bound) == 0 {
			fmt.Println("No keys bound. Configure scratch.keys in", configPathHint())
			return nil
		}
		fmt.Println("Bound tmx keys:")
		for _, b := range bound {
			fmt.Printf("  %s\n", b)
		}
		return nil
	},
}

func configPathHint() string {
	if path, err := config.DefaultConfigPath(); err == nil {
		return path
	}
	return "~/.config/tmx/config.yaml"
}
