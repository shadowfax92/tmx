package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"tmx/internal/config"

	"github.com/spf13/cobra"
)

func init() {
	configCmd.Flags().Bool("path", false, "Print the config file path and exit")
	configCmd.Flags().Bool("edit", false, "Open the config in $EDITOR")
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:         "config",
	Annotations: map[string]string{"group": "Setup:"},
	Short:       "Show the config path and the resolved scratch popup profile",
	Long: `Print the config path and, for the current tmux client width, which size
profile is active and the popup size each scratch type resolves to. Useful for
debugging width-matched profiles (the tmx equivalent of inspecting which
profile a client falls into).

  tmx config          — show the active profile + per-type popup sizes
  tmx config --path   — print the config file path only
  tmx config --edit   — open the config in $EDITOR`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.DefaultConfigPath()
		if err != nil {
			return err
		}

		if p, _ := cmd.Flags().GetBool("path"); p {
			fmt.Println(path)
			return nil
		}
		if e, _ := cmd.Flags().GetBool("edit"); e {
			return editConfig(path)
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}

		fmt.Println(path)

		width := config.TmuxClientWidth()
		widthStr := "unknown (not inside tmux?)"
		if width > 0 {
			widthStr = fmt.Sprintf("%d cols", width)
		}
		active := "none — using top-level popup sizes"
		if profile := cfg.Scratch.SelectProfile(width); profile != nil {
			active = profile.Name
		}
		if env := os.Getenv("TMX_PROFILE"); env != "" {
			active += fmt.Sprintf("  (forced via TMX_PROFILE=%s)", env)
		}
		fmt.Printf("client width: %s\nactive profile: %s\n\n", widthStr, active)

		for _, typ := range cfg.Scratch.Types() {
			size, _ := cfg.Scratch.ResolvePopup(typ)
			run := cfg.Scratch.CmdFor(typ)
			if run == "" {
				run = "(login shell)"
			}
			fmt.Printf("  %-6s %4s x %-4s  %s\n", typ, size.Width, size.Height, run)
		}
		return nil
	},
}

func editConfig(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}
