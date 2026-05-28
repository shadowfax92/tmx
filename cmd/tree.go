package cmd

import (
	"fmt"
	"sort"

	"tmx/internal/scratch"
	"tmx/internal/tmux"

	"github.com/charmbracelet/lipgloss"
)

// sessionTree renders the colored session tree picker (the default `tmx`
// action): sessions grouped by "/" segments, each leaf annotated with the
// command running in its active pane. Selecting a row switches the client to
// that session. Scratch (gs/) sessions are hidden unless all is set.
func sessionTree(all bool) error {
	names, err := tmux.ListSessions()
	if err != nil {
		return err
	}
	if !all {
		names = visibleSessions(names)
	}
	if len(names) == 0 {
		return fmt.Errorf("no tmux sessions")
	}
	sort.Strings(names)

	commands, _ := tmux.ActivePaneCommands()
	current, _ := tmux.CurrentSession()

	faint := lipgloss.NewStyle().Faint(true)
	branchStyle := lipgloss.NewStyle().Foreground(clrCyan)
	currentStyle := lipgloss.NewStyle().Foreground(clrGreen).Bold(true)
	dim := lipgloss.NewStyle().Faint(true)

	var lines []string
	for _, row := range buildSessionTreeRows(names) {
		marker := "  "
		segment := row.segment
		switch {
		case row.sessionName != "" && row.sessionName == current:
			marker = currentStyle.Render("● ")
			segment = currentStyle.Render(segment)
		case row.hasChild && row.sessionName == "":
			segment = branchStyle.Render(segment)
		}

		visible := faint.Render(row.branch) + marker + segment
		if row.sessionName != "" {
			if cmd := commands[row.sessionName]; cmd != "" {
				visible += "  " + dim.Render(cmd)
			}
		}

		lines = append(lines, fmt.Sprintf("%s\t%s", row.defaultTarget, visible))
	}

	target, err := runFzf("session > ", lines, sessionTreeFzfArgs())
	if err != nil {
		return err
	}
	return switchOrAttach(target)
}

func visibleSessions(names []string) []string {
	visible := make([]string, 0, len(names))
	for _, name := range names {
		if scratch.IsSession(name) {
			continue
		}
		visible = append(visible, name)
	}
	return visible
}
