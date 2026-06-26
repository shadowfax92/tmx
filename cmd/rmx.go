package cmd

import (
	"fmt"
	"strings"

	"tmx/internal/config"
	"tmx/internal/sessionmux"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(rmxCmd)
}

var rmxCmd = &cobra.Command{
	Use:         "rmx [--dry-run] <command> [args...]",
	Annotations: map[string]string{"group": "Sessions:"},
	Short:       "Run rmx-style workflow sessions through the configured backend",
	Long: `Run the small rmux-compatible command surface used by detached workflow
sessions. With the default config, tmx shells out to the real tmux binary and
stores physical sessions under rmx/.

  tmx rmx has-session -t codex/feat-example
  tmx rmx new-session -d -s codex/feat-example -c /tmp/work launcher
  tmx rmx set-buffer -- "$PROMPT"
  tmx rmx paste-buffer -t codex/feat-example -p
  tmx rmx send-keys -t codex/feat-example Enter
  tmx rmx attach-session -t codex/feat-example
  tmx rmx exit`,
	DisableFlagParsing: true,
	Args:               cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
			return cmd.Help()
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runRmxArgs(cmd, sessionmux.New(cfg.Sessions, nil), args)
	},
}

func runRmxArgs(cmd *cobra.Command, client sessionmux.Client, args []string) error {
	dryRun := false
	if len(args) > 0 && args[0] == "--dry-run" {
		dryRun = true
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("missing rmx command")
	}
	if args[0] == "-h" || args[0] == "--help" {
		return cmd.Help()
	}

	if dryRun {
		line, err := client.DryRunLine(args)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), line)
		return nil
	}

	if args[0] == "exit" {
		return client.ExitCurrent()
	}

	out, err := client.Run(args)
	if err != nil {
		return err
	}
	if strings.TrimRight(out, "\n") != "" {
		fmt.Fprintln(cmd.OutOrStdout(), out)
	}
	return nil
}
