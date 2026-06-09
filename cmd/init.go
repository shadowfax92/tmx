package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"

	"tmx/internal/config"
	"tmx/internal/mux"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

func init() {
	initCmd.Flags().Bool("no-jump", false, "Skip binding the M-s/M-w/M-p jump popups")
	initCmd.Flags().Bool("rmux", false, "Install scratch keybindings into rmux instead of tmux")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:         "init",
	Annotations: map[string]string{"group": "Setup:"},
	Short:       "Install tmx keybindings (tmux by default, rmux with --rmux)",
	Long: `Bind tmx keys in the running tmux server. Idempotent — safe to re-run.

Scratch toggles come from config (scratch.keys). Jump popups (unless --no-jump):
  M-s → tmx        session tree
  M-w → tmx -w     windows
  M-p → tmx -p     panes

Use --rmux to install scratch toggles in the running rmux server instead.

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
		rmuxMode, _ := cmd.Flags().GetBool("rmux")

		target := config.KeyTargetTmux
		bind := tmux.BindKeyRaw
		if rmuxMode {
			target = config.KeyTargetRmux
			bind = mux.BindRmuxKeyRaw
		}
		bindings := initBindings(cfg, self, initBindOptions{target: target, noJump: noJump})
		bound, err := runInitBindings(bindings, bind, os.Stderr)
		if err != nil {
			return err
		}

		if len(bound) == 0 {
			fmt.Println("No keys bound. Configure", keyConfigHint(target), "in", configPathHint())
			return nil
		}
		fmt.Println("Bound tmx keys:")
		for _, b := range bound {
			fmt.Printf("  %s\n", b)
		}
		return nil
	},
}

type initBindOptions struct {
	target config.KeyTarget
	noJump bool
}

type initBinding struct {
	key  string
	typ  string
	desc string
	args []string
}

type bindKeyFunc func(args ...string) error

// initBindings builds the live key bindings for the selected multiplexer.
func initBindings(cfg *config.Config, self string, opts initBindOptions) []initBinding {
	bindings := scratchInitBindings(cfg, self, opts.target)
	if opts.target == config.KeyTargetTmux && !opts.noJump {
		bindings = append(bindings, jumpInitBindings(self)...)
	}
	return bindings
}

// scratchInitBindings builds per-type scratch toggle bindings.
func scratchInitBindings(cfg *config.Config, self string, target config.KeyTarget) []initBinding {
	var bindings []initBinding
	for _, typ := range cfg.Scratch.Types() {
		key := cfg.Scratch.KeyFor(typ, target)
		if key == "" {
			continue
		}
		toggle := scratchToggleCommand(self, typ, target)
		bindings = append(bindings, initBinding{
			key:  key,
			typ:  typ,
			desc: fmt.Sprintf("%-4s → scratch %s", key, typ),
			args: []string{"-n", key, "run-shell", "-b", toggle},
		})
	}
	return bindings
}

func scratchToggleCommand(self, typ string, target config.KeyTarget) string {
	prefix := ""
	if target == config.KeyTargetRmux {
		prefix = "TMX_SCRATCH_CONTEXT=rmux "
	}
	return fmt.Sprintf(`%s%s scratch %s "#{client_name}" "#{session_name}" "#{pane_id}" >/dev/null 2>&1 || true`, prefix, self, typ)
}

func jumpInitBindings(self string) []initBinding {
	jumps := []struct{ key, suffix, desc string }{
		{"M-s", "", "tmx (session tree)"},
		{"M-w", " -w", "tmx -w (windows)"},
		{"M-p", " -p", "tmx -p (panes)"},
	}
	bindings := make([]initBinding, 0, len(jumps))
	for _, jump := range jumps {
		bindings = append(bindings, initBinding{
			key:  jump.key,
			desc: fmt.Sprintf("%-4s → %s", jump.key, jump.desc),
			args: []string{"-n", jump.key, "display-popup", "-E", self + jump.suffix},
		})
	}
	return bindings
}

func runInitBindings(bindings []initBinding, bind bindKeyFunc, errOut io.Writer) ([]string, error) {
	bound := make([]string, 0, len(bindings))
	failures := 0
	for _, binding := range bindings {
		if err := bind(binding.args...); err != nil {
			failures++
			fmt.Fprintf(errOut, "warning: failed to bind %s (%s): %v\n", binding.key, binding.desc, err)
			continue
		}
		bound = append(bound, binding.desc)
	}
	if len(bindings) > 0 && len(bound) == 0 && failures > 0 {
		return bound, fmt.Errorf("failed to bind %d configured key(s); no keys installed", failures)
	}
	return bound, nil
}

func keyConfigHint(target config.KeyTarget) string {
	if target == config.KeyTargetRmux {
		return "scratch.rmux_keys or scratch.keys"
	}
	return "scratch.keys"
}

func configPathHint() string {
	if path, err := config.DefaultConfigPath(); err == nil {
		return path
	}
	return "~/.config/tmx/config.yaml"
}
