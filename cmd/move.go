package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"tmx/internal/scratch"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(moveCmd)
}

var moveCmd = &cobra.Command{
	Use:         "move [target-session]",
	Aliases:     []string{"mv"},
	Annotations: map[string]string{"group": "Navigate:"},
	Short:       "Move a selected window to another session",
	Long: `Pick a tmux window, then move it to a different session.
Creates the target session if it doesn't exist.

  tmx move         — pick source window, then target session via fzf
  tmx move admin   — pick source window, then move it to "admin" (created if missing)
  tmx mv ops       — same, with alias`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		source, err := pickMoveWindow()
		if err != nil {
			return err
		}

		var target string
		if len(args) == 1 {
			target = args[0]
		} else {
			picked, err := pickMoveTarget(source.Session)
			if err != nil {
				return err
			}
			target = picked
		}

		return moveSelectedWindow(source, target)
	},
}

// moveSelectedWindow moves an already-selected source window into target. It
// preserves the legacy conveniences of g/-prefix resolution and creating a
// missing destination session.
func moveSelectedWindow(source tmux.WindowInfo, target string) error {
	if !tmux.SessionExists(target) && tmux.SessionExists("g/"+target) {
		target = "g/" + target
	}

	if source.Session == target {
		return fmt.Errorf("window %s is already in %q", source.Target, target)
	}

	created := false
	if !tmux.SessionExists(target) {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/"
		}
		if err := tmux.NewSession(target, home); err != nil {
			return fmt.Errorf("creating session %q: %w", target, err)
		}
		created = true
	}

	if err := tmux.MoveWindow(source.Target, target); err != nil {
		return fmt.Errorf("moving window: %w", err)
	}

	// A freshly created session has a placeholder window the moved window
	// landed beside — kill it so the target isn't left with an empty shell.
	if created {
		_ = tmux.KillWindow("=" + target + ":1")
	}

	fmt.Printf("moved %s to %s\n", source.Target, target)
	return nil
}

// pickMoveWindow lets tmx move choose its source explicitly instead of relying
// on the pane that invoked the command. Scratch windows stay hidden because
// they are recreatable popups, not normal move sources.
func pickMoveWindow() (tmux.WindowInfo, error) {
	windows, err := tmux.ListWindowInfo()
	if err != nil {
		return tmux.WindowInfo{}, err
	}
	windows = moveSourceWindows(windows)
	if len(windows) == 0 {
		return tmux.WindowInfo{}, fmt.Errorf("no tmux windows to move")
	}

	lines := make([]string, 0, len(windows))
	for _, w := range windows {
		lines = append(lines, formatMoveWindowPickerLine(w))
	}

	target, err := runFzf("move window > ", lines, windowFzfArgs())
	if err != nil {
		return tmux.WindowInfo{}, err
	}
	for _, w := range windows {
		if w.Target == target {
			return w, nil
		}
	}
	return tmux.WindowInfo{}, fmt.Errorf("selected window %q no longer exists", target)
}

func moveSourceWindows(windows []tmux.WindowInfo) []tmux.WindowInfo {
	visible := make([]tmux.WindowInfo, 0, len(windows))
	for _, w := range windows {
		if scratch.IsSession(w.Session) {
			continue
		}
		visible = append(visible, w)
	}
	return visible
}

func formatMoveWindowPickerLine(w tmux.WindowInfo) string {
	return formatWindowPickerLine(w, false)
}

// pickMoveTarget chooses the destination session for a selected source window.
// The printed query remains a create-new-session path for typed destinations.
func pickMoveTarget(sourceSession string) (string, error) {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return "", err
	}

	lines := moveTargetSessions(sessions, sourceSession)

	if len(lines) == 0 {
		return "", fmt.Errorf("no other sessions to move to — pass a name to create one")
	}

	fzfCmd := exec.Command("fzf", "--prompt", "move to > ",
		"--height", "100%", "--reverse",
		"--print-query")
	fzfCmd.Stdin = strings.NewReader(strings.Join(lines, "\n"))
	fzfCmd.Stderr = os.Stderr

	out, err := fzfCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 130 {
			return "", ErrCancelled
		}
		// --print-query exits 1 when the query matches nothing but the user
		// pressed enter; in that case line 1 holds the typed query.
		if len(out) > 0 {
			query := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			if query != "" {
				return query, nil
			}
		}
		return "", ErrCancelled
	}

	// --print-query: line 1 = query, line 2 = selected match.
	outputLines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(outputLines) >= 2 && outputLines[1] != "" {
		return outputLines[1], nil
	}
	if outputLines[0] != "" {
		return outputLines[0], nil
	}
	return "", ErrCancelled
}

func moveTargetSessions(sessions []string, sourceSession string) []string {
	targets := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if scratch.IsSession(s) || s == sourceSession {
			continue
		}
		targets = append(targets, s)
	}
	return targets
}
