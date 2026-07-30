package cmd

import (
	"fmt"
	"os"
	"strconv"

	"tmx/internal/config"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

const attnConfigSnippet = `# Attention window label: use in window-status-format and
# window-status-current-format where you render the window index/name.
#{?#{e|>:#{@attn_unread_count},0},#[fg=blue]#{window_index}:#{window_name}#[default],#{window_index}:#{window_name}}

# Attention pane badge: append inside each pane-border-format branch.
#{?#{==:#{@attn_state},unread},#[fg=blue] [ATTN]#[default],}
`

type initDeps struct {
	loadConfig func() (*config.Config, error)
	executable func() (string, error)
	bindKey    func(...string) error
}

func defaultInitDeps() initDeps {
	return initDeps{
		loadConfig: config.Load,
		executable: os.Executable,
		bindKey:    tmux.BindKeyRaw,
	}
}

func init() {
	rootCmd.AddCommand(initCmd)
}

var initCmd = newInitCommand(defaultInitDeps())

func newInitCommand(deps initDeps) *cobra.Command {
	command := &cobra.Command{
		Use:         "init",
		Annotations: map[string]string{"group": "Setup:"},
		Short:       "Install tmx tmux keybindings",
		Long: `Bind tmx keys in the running tmux server. Idempotent — safe to re-run.

Scratch toggles come from config (scratch.keys). Jump popups (unless --no-jump):
  M-s → tmx        session tree
  M-w → tmx -w     windows
  M-p → tmx -p     panes

Attention keys:
  M-u u → tmx jump       M-u s → tmx snooze
  M-u i → tmx inbox      M-u d → tmx unwatch

Live binds don't survive a tmux server restart. To persist, add to ~/.tmux.conf:
  run-shell 'tmx init'

Use --attn-conf to print status-format fragments without changing tmux.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if printConfig, _ := cmd.Flags().GetBool("attn-conf"); printConfig {
				_, err := fmt.Fprint(cmd.OutOrStdout(), attnConfigSnippet)
				return err
			}

			cfg, err := deps.loadConfig()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}
			selfPath, err := deps.executable()
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
				if err := deps.bindKey("-n", key, "run-shell", "-b", toggle); err != nil {
					cmd.PrintErrf("warning: failed to bind %s (scratch %s): %v\n", key, typ, err)
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
					if err := deps.bindKey("-n", jb.key, "display-popup", "-E", self+jb.suffix); err != nil {
						cmd.PrintErrf("warning: failed to bind %s (%s): %v\n", jb.key, jb.desc, err)
						continue
					}
					bound = append(bound, fmt.Sprintf("%-4s → %s", jb.key, jb.desc))
				}
			}

			attentionBinds := []struct {
				key, subcommand, desc string
				repeatable            bool
			}{
				{key: "u", subcommand: "jump", desc: "next unread", repeatable: true},
				{key: "s", subcommand: "snooze", desc: "snooze current pane"},
				{key: "i", subcommand: "inbox", desc: "attention inbox"},
				{key: "d", subcommand: "unwatch", desc: "unwatch current pane"},
			}
			if err := deps.bindKey("-n", "M-u", "switch-client", "-T", "attn"); err != nil {
				cmd.PrintErrf("warning: failed to bind M-u (attention table): %v\n", err)
			} else {
				bound = append(bound, "M-u  → attention table")
			}
			for _, attention := range attentionBinds {
				bindArgs := []string{"-T", "attn"}
				if attention.repeatable {
					bindArgs = append(bindArgs, "-r")
				}
				bindArgs = append(bindArgs, attention.key, "run-shell", self+" "+attention.subcommand)
				if err := deps.bindKey(bindArgs...); err != nil {
					cmd.PrintErrf("warning: failed to bind M-u %s (%s): %v\n", attention.key, attention.desc, err)
					continue
				}
				bound = append(bound, fmt.Sprintf("M-u %s → %s", attention.key, attention.desc))
			}

			if len(bound) == 0 {
				cmd.Println("No keys bound. Configure scratch.keys in", configPathHint())
				return nil
			}
			cmd.Println("Bound tmx keys:")
			for _, b := range bound {
				cmd.Printf("  %s\n", b)
			}
			return nil
		},
	}
	command.Flags().Bool("no-jump", false, "Skip binding the M-s/M-w/M-p jump popups")
	command.Flags().Bool("attn-conf", false, "Print attention status-format fragments and exit")
	return command
}

func configPathHint() string {
	if path, err := config.DefaultConfigPath(); err == nil {
		return path
	}
	return "~/.config/tmx/config.yaml"
}
