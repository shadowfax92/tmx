package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"tmx/internal/attn"
	"tmx/internal/config"
	"tmx/internal/tmux"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

const emptyInboxMessage = "No watched agent panes."

type inboxOptions struct {
	json        bool
	quiet       bool
	interactive bool
}

type inboxDeps struct {
	snapshot func() ([]attn.PaneState, error)
	capture  func(string, int) (string, error)
	pick     func(string, []string, []string) (string, error)
	terminal func(io.Writer) bool
	loadJump func() (config.JumpAction, string, error)
	jump     jumpDeps
	now      func() time.Time
}

type inboxEntry struct {
	PaneState attn.PaneState
	State     string
	Age       string
	AgeSecs   int64
	Target    string
	Label     string
	LastLine  string
}

type inboxJSONEntry struct {
	State      string `json:"state"`
	Age        string `json:"age"`
	AgeSeconds int64  `json:"age_seconds"`
	Since      int64  `json:"since"`
	Target     string `json:"target"`
	PaneID     string `json:"pane_id"`
	Label      string `json:"label"`
	LastLine   string `json:"last_line"`
}

func init() {
	rootCmd.AddCommand(newInboxCommand())
}

func newInboxCommand() *cobra.Command {
	return newInboxCommandWithDeps(defaultInboxDeps())
}

func defaultInboxDeps() inboxDeps {
	return inboxDeps{
		snapshot: attn.Snapshot,
		capture:  tmux.CapturePane,
		pick:     runFzf,
		terminal: func(w io.Writer) bool {
			file, ok := w.(*os.File)
			return ok && term.IsTerminal(file.Fd())
		},
		loadJump: func() (config.JumpAction, string, error) {
			cfg, err := config.Load()
			if err != nil {
				return "", "", fmt.Errorf("loading config: %w", err)
			}
			action, _ := cfg.Scratch.ResolveJumpAction()
			return action, cfg.Watch.FocusCommand, nil
		},
		jump: jumpDeps{
			backend:  tmuxJumpBackend{},
			snapshot: attn.Snapshot,
			markRead: markPaneRead,
		},
		now: time.Now,
	}
}

func newInboxCommandWithDeps(deps inboxDeps) *cobra.Command {
	command := &cobra.Command{
		Use:         "inbox",
		Annotations: map[string]string{"group": "Navigate:"},
		Short:       "List watched agent panes or jump to one",
		Args:        cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			quiet, _ := cmd.Flags().GetBool("quiet")
			plain, _ := cmd.Flags().GetBool("plain")
			options := inboxOptions{
				json:  jsonOut,
				quiet: quiet,
			}
			options.interactive = !jsonOut && !quiet && !plain && deps.terminal(cmd.OutOrStdout())
			return runInbox(cmd.OutOrStdout(), options, deps)
		},
	}
	command.Flags().Bool("json", false, "Print watched panes as JSON")
	command.Flags().BoolP("quiet", "q", false, "Print targets only")
	command.Flags().Bool("plain", false, "Print the listing instead of opening fzf")
	return command
}

func runInbox(w io.Writer, options inboxOptions, deps inboxDeps) error {
	states, err := deps.snapshot()
	if err != nil {
		return err
	}
	states = watchedInboxStates(states)
	sortInboxStates(states)

	entries := make([]inboxEntry, 0, len(states))
	for _, state := range states {
		entry := makeInboxEntry(state, deps.now())
		if !options.quiet {
			captured, err := deps.capture(state.ID, 0)
			if err != nil {
				// Snapshot is a live inventory; capture failures normally mean
				// this pane disappeared between the two tmux calls.
				continue
			}
			entry.LastLine = lastNonEmptyScreenLine(captured)
		}
		entries = append(entries, entry)
	}

	switch {
	case options.json:
		return printInboxJSON(w, entries)
	case options.quiet:
		for _, entry := range entries {
			fmt.Fprintln(w, entry.Target)
		}
		return nil
	case len(entries) == 0:
		_, err := fmt.Fprintln(w, emptyInboxMessage)
		return err
	case !options.interactive:
		printInboxPlain(w, entries)
		return nil
	}

	lines := make([]string, 0, len(entries))
	byID := make(map[string]inboxEntry, len(entries))
	for _, entry := range entries {
		lines = append(lines, formatInboxPickerLine(entry))
		byID[entry.PaneState.ID] = entry
	}
	selected, err := deps.pick("inbox > ", lines, paneFzfArgs())
	if err != nil {
		return err
	}
	entry, ok := byID[selected]
	if !ok {
		return fmt.Errorf("fzf selected unknown pane %q", selected)
	}
	action, focus, err := deps.loadJump()
	if err != nil {
		return err
	}
	client, err := deps.jump.backend.CurrentClient()
	if err != nil {
		return fmt.Errorf("resolving invoking client: %w", err)
	}
	jumped, err := jumpToTarget(action, focus, client, entry.PaneState, deps.jump)
	if err != nil {
		return err
	}
	if !jumped {
		return fmt.Errorf("pane %s is no longer available", selected)
	}
	return nil
}

func watchedInboxStates(states []attn.PaneState) []attn.PaneState {
	watched := make([]attn.PaneState, 0, len(states))
	for _, state := range states {
		if state.WatchSet {
			watched = append(watched, state)
		}
	}
	return watched
}

func sortInboxStates(states []attn.PaneState) {
	sort.SliceStable(states, func(i, j int) bool {
		left, right := states[i], states[j]
		leftRank, rightRank := inboxStateRank(left), inboxStateRank(right)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if left.Since != right.Since {
			if left.Since == 0 {
				return false
			}
			if right.Since == 0 {
				return true
			}
			return left.Since < right.Since
		}
		return inboxTarget(left) < inboxTarget(right)
	})
}

func inboxStateRank(state attn.PaneState) int {
	switch inboxStateName(state) {
	case string(attn.StateUnread):
		return 0
	case string(attn.StateQuiet):
		return 1
	case string(attn.StateActive):
		return 2
	default:
		return 3
	}
}

func inboxStateName(state attn.PaneState) string {
	if !state.Watch {
		return "unwatched"
	}
	switch state.State {
	case attn.StateUnread, attn.StateQuiet, attn.StateActive:
		return string(state.State)
	default:
		return string(attn.StateActive)
	}
}

func makeInboxEntry(state attn.PaneState, now time.Time) inboxEntry {
	age, ageSecs := inboxAge(state.Since, now)
	label := strings.TrimSpace(state.Label)
	if label == "" {
		label = strings.TrimSpace(state.WindowName)
	}
	if label == "" {
		label = "-"
	}
	return inboxEntry{
		PaneState: state,
		State:     inboxStateName(state),
		Age:       age,
		AgeSecs:   ageSecs,
		Target:    inboxTarget(state),
		Label:     sanitizeInboxField(label),
	}
}

func inboxTarget(state attn.PaneState) string {
	if state.Target != "" {
		return state.Target
	}
	return state.ID
}

func inboxAge(since int64, now time.Time) (string, int64) {
	if since <= 0 {
		return "-", 0
	}
	seconds := now.Unix() - since
	if seconds < 0 {
		seconds = 0
	}
	return formatWatchAge(time.Duration(seconds) * time.Second), seconds
}

func lastNonEmptyScreenLine(captured string) string {
	captured = ansi.Strip(strings.ReplaceAll(captured, "\r", ""))
	lines := strings.Split(captured, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return sanitizeInboxField(line)
		}
	}
	return "-"
}

func sanitizeInboxField(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	return strings.Join(strings.Fields(value), " ")
}

func printInboxPlain(w io.Writer, entries []inboxEntry) {
	stateWidth, ageWidth, targetWidth, labelWidth := 5, 3, 6, 5
	for _, entry := range entries {
		stateWidth = max(stateWidth, len(entry.State))
		ageWidth = max(ageWidth, len(entry.Age))
		targetWidth = max(targetWidth, len(entry.Target))
		labelWidth = max(labelWidth, len(entry.Label))
	}
	for _, entry := range entries {
		fmt.Fprintf(
			w,
			"%-*s  %-*s  %-*s  %-*s  %s\n",
			stateWidth, entry.State,
			ageWidth, entry.Age,
			targetWidth, entry.Target,
			labelWidth, entry.Label,
			entry.LastLine,
		)
	}
}

func printInboxJSON(w io.Writer, entries []inboxEntry) error {
	output := make([]inboxJSONEntry, 0, len(entries))
	for _, entry := range entries {
		output = append(output, inboxJSONEntry{
			State:      entry.State,
			Age:        entry.Age,
			AgeSeconds: entry.AgeSecs,
			Since:      entry.PaneState.Since,
			Target:     entry.Target,
			PaneID:     entry.PaneState.ID,
			Label:      entry.Label,
			LastLine:   entry.LastLine,
		})
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}

func formatInboxPickerLine(entry inboxEntry) string {
	return fmt.Sprintf(
		"%s\t%-9s\t%-7s\t%s\t%s\t%s",
		entry.PaneState.ID,
		entry.State,
		entry.Age,
		entry.Target,
		entry.Label,
		entry.LastLine,
	)
}
