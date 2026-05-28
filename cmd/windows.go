package cmd

import (
	"fmt"
	"sort"
	"strings"

	"tmx/internal/scratch"
	"tmx/internal/tmux"
)

// jumpWindows renders the window picker (`tmx -w`): every window across all
// sessions, with a live pane preview. Sorted by the session's tmux activity
// (most-recently-active first), then window index.
func jumpWindows(all bool) error {
	sessions, err := tmux.ListSessionInfo()
	if err != nil {
		return err
	}

	windows, err := tmux.ListWindowInfo()
	if err != nil {
		return err
	}
	if !all {
		var visible []tmux.WindowInfo
		for _, w := range windows {
			if !scratch.IsSession(w.Session) {
				visible = append(visible, w)
			}
		}
		windows = visible
	}
	if len(windows) == 0 {
		return fmt.Errorf("no tmux windows")
	}

	activity := make(map[string]int64, len(sessions))
	for _, s := range sessions {
		activity[s.Name] = s.Activity
	}

	sort.Slice(windows, func(i, j int) bool {
		ai, aj := activity[windows[i].Session], activity[windows[j].Session]
		if ai != aj {
			return ai > aj
		}
		return windows[i].Index < windows[j].Index
	})

	currentTarget, _ := tmux.CurrentTarget()
	currentWindow := currentTarget
	if idx := strings.LastIndex(currentTarget, "."); idx >= 0 {
		currentWindow = currentTarget[:idx]
	}

	// Fields: 1=target(hidden) 2=window 3=session. Everything visible is searchable.
	var lines []string
	for _, w := range windows {
		lines = append(lines, formatWindowPickerLine(w, w.Target == currentWindow))
	}

	target, err := runFzf("window > ", lines, windowFzfArgs())
	if err != nil {
		return err
	}
	return switchOrAttach(target)
}

// formatWindowPickerLine keeps the searched window as the first visible fzf
// column while preserving field 1 as the hidden tmux target.
func formatWindowPickerLine(w tmux.WindowInfo, current bool) string {
	marker := "  "
	if current {
		marker = "● "
	}
	name := w.Name
	if name == "" {
		name = w.Label
	}
	window := fmt.Sprintf("%d:%s", w.Index, name)
	return fmt.Sprintf("%s\t%s%-24s\t%s", w.Target, marker, window, w.Session)
}
