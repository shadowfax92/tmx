package cmd

import (
	"fmt"
	"os"
	"strings"

	"tmx/internal/scratch"
	"tmx/internal/tmux"
)

// jumpPanes renders the pane picker (`tmx -p`): every pane across all sessions,
// searchable by window/pane label/command/path, with a live pane preview.
func jumpPanes(all bool) error {
	target, err := selectPane(all)
	if err != nil {
		return err
	}
	return switchOrAttach(target)
}

func selectPane(all bool) (string, error) {
	panes, err := tmux.ListPaneInfo()
	if err != nil {
		return "", err
	}
	if !all {
		var visible []tmux.PaneInfo
		for _, p := range panes {
			if !scratch.IsSession(p.Session) {
				visible = append(visible, p)
			}
		}
		panes = visible
	}
	if len(panes) == 0 {
		return "", fmt.Errorf("no tmux panes")
	}

	current, _ := tmux.CurrentTarget()
	home, _ := os.UserHomeDir()

	// Fields: 1=target(hidden) 2=pane 3=window 4=command 5=path. Session is not
	// shown — the preview makes the context obvious.
	var lines []string
	for _, p := range panes {
		path := p.Path
		if home != "" && strings.HasPrefix(path, home) {
			path = "~" + path[len(home):]
		}
		lines = append(lines, formatPanePickerLine(p, path, p.Target == current))
	}

	return runFzf("pane > ", lines, paneFzfArgs())
}

// formatPanePickerLine keeps the searched pane as the first visible fzf column
// while preserving field 1 as the hidden tmux target.
func formatPanePickerLine(p tmux.PaneInfo, path string, current bool) string {
	marker := "  "
	if current {
		marker = "● "
	}
	window := fmt.Sprintf("%d:%s", p.WindowIndex, p.WindowName)
	pane := fmt.Sprintf("%d", p.PaneIndex)
	if p.Label != "" {
		pane = fmt.Sprintf("%d:%s", p.PaneIndex, p.Label)
	}
	return fmt.Sprintf("%s\t%s%-16s\t%s\t%s\t%s",
		p.Target, marker, pane, window, p.Command, path)
}
