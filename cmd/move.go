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
	Short:       "Move selected windows to another session",
	Long: `Pick one or more tmux windows, then move them to a different session.
Creates the target session if it doesn't exist.

  tmx move         — pick source windows, then target session via fzf
  tmx move admin   — pick source windows, then move them to "admin" (created if missing)
  tmx mv ops       — same, with alias`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sources, err := pickMoveWindows()
		if err != nil {
			return err
		}

		var target string
		if len(args) == 1 {
			target = args[0]
		} else {
			picked, err := pickMoveTarget(sources)
			if err != nil {
				return err
			}
			target = picked
		}

		return moveSelectedWindows(sources, target)
	},
}

// moveSelectedWindows moves already-selected source windows into target. It
// preserves the legacy conveniences of g/-prefix resolution and creating a
// missing destination session.
func moveSelectedWindows(sources []tmux.WindowInfo, target string) error {
	if !tmux.SessionExists(target) && tmux.SessionExists("g/"+target) {
		target = "g/" + target
	}

	toMove := moveWindowsForTarget(sources, target)
	if len(toMove) == 0 {
		return fmt.Errorf("selected windows are already in %q", target)
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

	for _, source := range toMove {
		if err := tmux.MoveWindow(moveWindowSourceTarget(source), target); err != nil {
			return fmt.Errorf("moving window %s: %w", source.Target, err)
		}
	}

	// A freshly created session has a placeholder window the moved window
	// landed beside — kill it so the target isn't left with an empty shell.
	if created {
		_ = tmux.KillWindow("=" + target + ":1")
	}

	fmt.Printf("moved %d window(s) to %s\n", len(toMove), target)
	return nil
}

// pickMoveWindows lets tmx move choose sources explicitly instead of relying
// on the pane that invoked the command. Scratch windows stay hidden because
// they are recreatable popups, not normal move sources.
func pickMoveWindows() ([]tmux.WindowInfo, error) {
	windows, err := tmux.ListWindowInfo()
	if err != nil {
		return nil, err
	}
	windows = moveSourceWindows(windows)
	if len(windows) == 0 {
		return nil, fmt.Errorf("no tmux windows to move")
	}

	lines := make([]string, 0, len(windows))
	for _, w := range windows {
		lines = append(lines, formatMoveWindowPickerLine(w))
	}

	targets, err := runFzfMulti("move window > ", lines, moveWindowFzfArgs())
	if err != nil {
		return nil, err
	}
	selected := make([]tmux.WindowInfo, 0, len(targets))
	byTarget := make(map[string]tmux.WindowInfo, len(windows))
	for _, w := range windows {
		byTarget[moveWindowSourceTarget(w)] = w
	}
	for _, target := range targets {
		w, ok := byTarget[target]
		if !ok {
			return nil, fmt.Errorf("selected window %q no longer exists", target)
		}
		selected = append(selected, w)
	}
	if len(selected) == 0 {
		return nil, ErrCancelled
	}
	return selected, nil
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
	w.Target = moveWindowSourceTarget(w)
	return formatWindowPickerLine(w, false)
}

func moveWindowSourceTarget(w tmux.WindowInfo) string {
	if w.ID != "" {
		return w.ID
	}
	return w.Target
}

func moveWindowFzfArgs() []string {
	return append(windowFzfArgs(), "--multi")
}

func moveWindowsForTarget(sources []tmux.WindowInfo, target string) []tmux.WindowInfo {
	toMove := make([]tmux.WindowInfo, 0, len(sources))
	for _, source := range sources {
		if source.Session == target {
			continue
		}
		toMove = append(toMove, source)
	}
	return toMove
}

// pickMoveTarget chooses the destination session for a selected source window.
// The printed query remains a create-new-session path for typed destinations.
func pickMoveTarget(sources []tmux.WindowInfo) (string, error) {
	sessions, err := tmux.ListSessions()
	if err != nil {
		return "", err
	}

	lines := moveTargetSessions(sessions, sources)

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

func moveTargetSessions(sessions []string, sources []tmux.WindowInfo) []string {
	sourceSession := onlySourceSession(sources)
	targets := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if scratch.IsSession(s) || s == sourceSession {
			continue
		}
		targets = append(targets, s)
	}
	return targets
}

func onlySourceSession(sources []tmux.WindowInfo) string {
	var only string
	for _, source := range sources {
		if only == "" {
			only = source.Session
			continue
		}
		if source.Session != only {
			return ""
		}
	}
	return only
}
