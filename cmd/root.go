package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"tmx/internal/tmux"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var Version = "dev"

// ErrCancelled is returned when the user aborts an fzf picker (Esc/Ctrl-C).
// Execute treats it as a clean exit so cancelling isn't reported as an error.
var ErrCancelled = errors.New("")

var (
	clrCyan    = lipgloss.Color("6")
	clrHiGreen = lipgloss.Color("10")
	clrYellow  = lipgloss.Color("11")
	clrGreen   = lipgloss.Color("2")
)

func helpHeader(s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(clrCyan).Render(s)
}

func helpCmdCol(s string) string {
	return lipgloss.NewStyle().Foreground(clrHiGreen).Render(s)
}

func helpHint(s string) string {
	return lipgloss.NewStyle().Faint(true).Render(s)
}

func helpAliases(aliases []string) string {
	return lipgloss.NewStyle().Foreground(clrYellow).Render(fmt.Sprintf("(aliases: %s)", strings.Join(aliases, ", ")))
}

var groupOrder = []string{
	"Navigate:",
	"Scratch:",
	"Setup:",
	"Other:",
}

func groupedHelp(cmd *cobra.Command) string {
	groups := map[string][]*cobra.Command{}
	for _, c := range cmd.Commands() {
		if !c.IsAvailableCommand() && c.Name() != "help" {
			continue
		}
		g := c.Annotations["group"]
		if g == "" {
			g = "Other:"
		}
		groups[g] = append(groups[g], c)
	}

	var b strings.Builder
	for _, name := range groupOrder {
		cmds, ok := groups[name]
		if !ok {
			continue
		}
		b.WriteString("\n" + helpHeader(name) + "\n")
		for _, c := range cmds {
			line := "  " + helpCmdCol(fmt.Sprintf("%-12s", c.Name())) + " " + c.Short
			if len(c.Aliases) > 0 {
				line += " " + helpAliases(c.Aliases)
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

const usageTemplate = `{{helpHeader "Usage:"}}{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

{{helpHeader "Aliases:"}}
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

{{helpHeader "Examples:"}}
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}
{{groupedHelp .}}{{end}}{{if .HasAvailableLocalFlags}}

{{helpHeader "Flags:"}}
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

{{helpHeader "Global Flags:"}}
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableSubCommands}}

{{helpHint (printf "Use \"%s [command] --help\" for more information." .CommandPath)}}{{end}}
`

var rootCmd = &cobra.Command{
	Use:           "tmx",
	Short:         "Get around tmux — session tree, window/pane jump, scratch popups",
	Version:       Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	Long: `tmx is a tmux navigation and scratch-popup tool.

  tmx       — interactive session tree; switch to the selection
  tmx -w    — search windows (with a live pane preview)
  tmx -p    — search panes   (with a live pane preview)
  tmx -a    — include scratch (gs/) sessions in any of the above`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		panes, _ := cmd.Flags().GetBool("panes")
		windows, _ := cmd.Flags().GetBool("windows")
		all, _ := cmd.Flags().GetBool("all")
		if panes && windows {
			return fmt.Errorf("-p and -w cannot be combined; pick one")
		}
		if panes {
			return jumpPanes(all)
		}
		if windows {
			return jumpWindows(all)
		}
		return sessionTree(all)
	},
}

func init() {
	cobra.AddTemplateFunc("helpHeader", helpHeader)
	cobra.AddTemplateFunc("helpCmdCol", helpCmdCol)
	cobra.AddTemplateFunc("helpAliases", helpAliases)
	cobra.AddTemplateFunc("helpHint", helpHint)
	cobra.AddTemplateFunc("groupedHelp", groupedHelp)

	rootCmd.SetUsageTemplate(usageTemplate)

	rootCmd.Flags().BoolP("panes", "p", false, "Search panes")
	rootCmd.Flags().BoolP("windows", "w", false, "Search windows")
	rootCmd.Flags().BoolP("all", "a", false, "Include scratch (gs/) sessions")
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		if errors.Is(err, ErrCancelled) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runFzf pipes lines into fzf and returns the hidden first tab-delimited field
// of the selection. Lines are "<target>\t<visible columns…>"; callers pass
// extra fzf args (--with-nth/--nth/--preview). Esc/Ctrl-C yields ErrCancelled.
func runFzf(prompt string, lines []string, extra []string) (string, error) {
	args := []string{
		"--prompt", prompt,
		"--height", "100%",
		"--reverse",
		"--delimiter", "\t",
	}
	if len(extra) > 0 {
		args = append(args, extra...)
	} else {
		args = append(args, "--with-nth", "2")
	}

	fzfCmd := exec.Command("fzf", args...)
	fzfCmd.Stdin = strings.NewReader(strings.Join(lines, "\n"))
	fzfCmd.Stderr = os.Stderr

	out, err := fzfCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return "", ErrCancelled
		}
		if len(out) == 0 {
			return "", ErrCancelled
		}
		return "", fmt.Errorf("fzf: %w", err)
	}

	line := strings.TrimSpace(string(out))
	if idx := strings.Index(line, "\t"); idx >= 0 {
		return line[:idx], nil
	}
	return line, nil
}

// switchOrAttach moves the client to target: switch-client inside tmux,
// attach-session when run from a bare terminal.
func switchOrAttach(target string) error {
	if tmux.IsInsideTmux() {
		return tmux.SwitchClient(target)
	}
	return tmux.Attach(target)
}
