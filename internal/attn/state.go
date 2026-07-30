package attn

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"tmx/internal/tmux"
)

const (
	WatchOption             = "attn_watch"
	StateOption             = "attn_state"
	SinceOption             = "attn_since"
	ChangedOption           = "attn_changed"
	HashOption              = "attn_hash"
	ProcOption              = "attn_proc"
	FiredOption             = "attn_fired"
	ReadHashOption          = "attn_read_hash"
	ReadLinesOption         = "attn_read_lines"
	WindowUnreadCountOption = "attn_unread_count"
)

type AttentionState string

const (
	StateActive AttentionState = "active"
	StateQuiet  AttentionState = "quiet"
	StateUnread AttentionState = "unread"
)

// PaneState is the attention contract attached to a discovered pane.
// WatchSet distinguishes a missing @attn_watch from an explicit opt-out.
type PaneState struct {
	tmux.PaneInfo
	WindowID          string
	WindowActivity    int64
	WindowUnreadCount int
	Watch             bool
	WatchSet          bool
	State             AttentionState
	Since             int64
	Changed           int64
	Hash              string
	Proc              string
	Fired             bool
	// ReadHash is the public screen baseline last acknowledged by the user.
	// Hash and Fired remain watcher-private rolling episode state.
	ReadHash  string
	ReadLines int
}

const (
	snapshotFormat  = "#{pane_id}\t#{session_name}:#{window_index}.#{pane_index}\t#{session_name}\t#{window_index}\t#{window_name}\t#{pane_index}\t#{pane_pid}\t#{@pane_label}\t#{pane_current_command}\t#{@fip_buffer}\t#{pane_current_path}\t#{window_id}\t#{window_activity}\t#{@attn_unread_count}\t#{@attn_watch}\t#{@attn_state}\t#{@attn_since}\t#{@attn_changed}\t#{@attn_proc}\t#{@attn_read_hash}\t#{@attn_read_lines}\t_"
	paneStateFormat = "_\t#{@attn_watch}\t#{@attn_state}\t#{@attn_since}\t#{@attn_changed}\t#{@attn_proc}\t#{@attn_read_hash}\t#{@attn_read_lines}\t_"
)

var (
	listPanesFormat   = tmux.ListPanesFormat
	displayPaneFormat = tmux.DisplayPaneFormat
	showPaneVar       = tmux.ShowPaneVar
	runCommands       = tmux.RunCommands
	lockState         = tmux.WaitForLock
	unlockState       = tmux.WaitForUnlock
)

var errPaneStateDrift = errors.New("pane attention state changed since snapshot")

// Snapshot returns attention state alongside the existing pane inventory.
// All attention user options are expanded in one list-panes call.
func Snapshot() ([]PaneState, error) {
	out, err := listPanesFormat(snapshotFormat)
	if err != nil {
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var states []PaneState
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 22)
		if len(parts) != 22 {
			continue
		}
		windowIndex, _ := strconv.Atoi(parts[3])
		paneIndex, _ := strconv.Atoi(parts[5])
		pid, _ := strconv.Atoi(parts[6])
		windowActivity, _ := strconv.ParseInt(parts[12], 10, 64)
		windowUnreadCount, _ := strconv.Atoi(parts[13])
		since, _ := strconv.ParseInt(parts[16], 10, 64)
		changed, _ := strconv.ParseInt(parts[17], 10, 64)
		readLines, _ := strconv.Atoi(parts[20])
		states = append(states, PaneState{
			PaneInfo: tmux.PaneInfo{
				ID:          parts[0],
				Target:      parts[1],
				Session:     parts[2],
				WindowIndex: windowIndex,
				WindowName:  parts[4],
				PaneIndex:   paneIndex,
				PID:         pid,
				Label:       parts[7],
				Command:     parts[8],
				FIPBuffer:   parts[9] != "",
				Path:        parts[10],
			},
			WindowID:          parts[11],
			WindowActivity:    windowActivity,
			WindowUnreadCount: windowUnreadCount,
			Watch:             parts[14] == "1",
			WatchSet:          parts[14] != "",
			State:             AttentionState(parts[15]),
			Since:             since,
			Changed:           changed,
			Proc:              parts[18],
			ReadHash:          parts[19],
			ReadLines:         readLines,
		})
	}
	return states, nil
}

// ResolveTarget resolves a pane id, window target, or session name to a pane.
// A bare session name is made exact and tmux chooses its active pane.
func ResolveTarget(target string) (string, error) {
	resolved, err := displayPaneFormat(normalizeTarget(target), "#{pane_id}")
	if err != nil {
		return "", err
	}
	if resolved == "" {
		return "", fmt.Errorf("target %q did not resolve to a pane", target)
	}
	return resolved, nil
}

func normalizeTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" || strings.Contains(target, ":") {
		return target
	}
	switch target[0] {
	case '%', '@', '$', '=':
		return target
	default:
		return "=" + target + ":"
	}
}

// Get reads the state contract for target. Missing options produce zero
// values; WatchSet remains false when @attn_watch has never been written.
func Get(target string) (PaneState, error) {
	paneID, err := ResolveTarget(target)
	if err != nil {
		return PaneState{}, err
	}
	state, err := readPaneState(paneID)
	state.ID = paneID
	return state, err
}

func readPaneState(paneID string) (PaneState, error) {
	out, err := displayPaneFormat(paneID, paneStateFormat)
	if err != nil {
		return PaneState{}, err
	}
	return parsePaneState(out, paneID)
}

func parsePaneState(out, paneID string) (PaneState, error) {
	values := strings.SplitN(out, "\t", 9)
	if len(values) != 9 || values[0] != "_" || values[8] != "_" {
		return PaneState{}, fmt.Errorf("reading attention state for %s: malformed tmux response", paneID)
	}
	since, _ := strconv.ParseInt(values[3], 10, 64)
	changed, _ := strconv.ParseInt(values[4], 10, 64)
	readLines, _ := strconv.Atoi(values[7])
	return PaneState{
		Watch:     values[1] == "1",
		WatchSet:  values[1] != "",
		State:     AttentionState(values[2]),
		Since:     since,
		Changed:   changed,
		Proc:      values[5],
		ReadHash:  values[6],
		ReadLines: readLines,
	}, nil
}

// Set writes the complete pane contract and updates the window unread count
// when the pane crosses the unread boundary.
func Set(target string, state PaneState) error {
	if err := validatePaneState(state); err != nil {
		return err
	}
	paneID, err := ResolveTarget(target)
	if err != nil {
		return err
	}
	return withPaneLock(paneID, func() error {
		previous, err := readPaneState(paneID)
		if err != nil {
			return err
		}
		return writePaneState(paneID, previous, state)
	})
}

// UpdateIfUnread applies update only while target is unread. The common no-op
// path reads only @attn_state; resolution, locking, and the full state read are
// deferred until that option says unread. The state is checked again under the
// pane lock so a concurrent transition wins instead of being overwritten.
func UpdateIfUnread(target string, update func(PaneState) PaneState) error {
	current, err := showPaneVar(normalizeTarget(target), StateOption)
	if err != nil {
		return err
	}
	if AttentionState(current) != StateUnread {
		return nil
	}

	paneID, err := ResolveTarget(target)
	if err != nil {
		return err
	}
	return withPaneLock(paneID, func() error {
		current, err := showPaneVar(paneID, StateOption)
		if err != nil {
			return err
		}
		if AttentionState(current) != StateUnread {
			return nil
		}

		state, err := readPaneState(paneID)
		if err != nil {
			return err
		}
		state.ID = paneID
		next := update(state)
		if err := validatePaneState(next); err != nil {
			return err
		}
		return writePaneState(paneID, state, next)
	})
}

// setPaneStateIfCurrent is the daemon's locked compare-and-set. If a CLI
// command changed the public contract after the tick snapshot, the daemon
// leaves it alone and reconciles that drift on the next roll call.
func setPaneStateIfCurrent(paneID string, expected, state PaneState) error {
	return withPaneLock(paneID, func() error {
		current, err := readPaneState(paneID)
		if err != nil {
			return err
		}
		if !samePublicState(current, expected) {
			return errPaneStateDrift
		}
		return writePaneState(paneID, current, state)
	})
}

func validatePaneState(state PaneState) error {
	if !validState(state.State) {
		return fmt.Errorf("invalid attention state %q", state.State)
	}
	return nil
}

func writePaneState(paneID string, previous, state PaneState) error {
	if err := validatePaneState(state); err != nil {
		return err
	}
	state.WatchSet = true

	watch := "0"
	if state.Watch {
		watch = "1"
	}
	var commands [][]string
	appendSet := func(changed bool, key, value string) {
		if changed {
			commands = append(commands, []string{"set-option", "-p", "-t", paneID, "@" + key, value})
		}
	}
	appendSet(!previous.WatchSet || previous.Watch != state.Watch, WatchOption, watch)
	appendSet(previous.Since != state.Since, SinceOption, strconv.FormatInt(state.Since, 10))
	appendSet(previous.Changed != state.Changed, ChangedOption, strconv.FormatInt(state.Changed, 10))
	appendSet(previous.Proc != state.Proc, ProcOption, state.Proc)
	appendSet(previous.ReadHash != state.ReadHash, ReadHashOption, state.ReadHash)
	appendSet(
		previous.ReadLines != state.ReadLines,
		ReadLinesOption,
		strconv.Itoa(state.ReadLines),
	)
	// State is last so readers never see a new state with stale companion
	// fields while tmux processes the command batch.
	appendSet(previous.State != state.State, StateOption, string(state.State))

	switch {
	case previous.State != StateUnread && state.State == StateUnread:
		commands = append(commands, tmux.WindowAdjustmentArgs(paneID, WindowUnreadCountOption, 1))
	case previous.State == StateUnread && state.State != StateUnread:
		commands = append(commands, tmux.WindowAdjustmentArgs(paneID, WindowUnreadCountOption, -1))
	}
	return runCommands(commands...)
}

// Clear unsets the complete pane contract. Clearing an unread pane also
// decrements its window aggregate.
func Clear(target string) error {
	paneID, err := ResolveTarget(target)
	if err != nil {
		return err
	}
	return withPaneLock(paneID, func() error {
		previous, err := readPaneState(paneID)
		if err != nil {
			return err
		}

		var commands [][]string
		for _, key := range []string{
			WatchOption, StateOption, SinceOption, ChangedOption,
			HashOption, ProcOption, FiredOption, ReadHashOption, ReadLinesOption,
		} {
			commands = append(commands, []string{"set-option", "-p", "-u", "-t", paneID, "@" + key})
		}
		if previous.State == StateUnread {
			commands = append(commands, tmux.WindowAdjustmentArgs(paneID, WindowUnreadCountOption, -1))
		}
		return runCommands(commands...)
	})
}

// ReconcileWindowUnreadCounts repairs window aggregates from the live pane
// inventory. This is what removes the count left behind when an unread pane
// dies before its pane options can be cleared.
func ReconcileWindowUnreadCounts() error {
	states, err := Snapshot()
	if err != nil {
		return err
	}
	return ReconcileWindowUnreadCountsFromSnapshot(states)
}

// ReconcileWindowUnreadCountsFromSnapshot repairs aggregates using an
// existing roll call. Only drifted windows are written, all in one tmux
// invocation.
func ReconcileWindowUnreadCountsFromSnapshot(states []PaneState) error {
	type aggregate struct {
		current int
		want    int
	}
	counts := make(map[string]aggregate)
	for _, state := range states {
		target := windowTarget(state)
		if target == "" {
			continue
		}
		count, exists := counts[target]
		if !exists {
			count.current = state.WindowUnreadCount
		}
		if state.State == StateUnread {
			count.want++
		}
		counts[target] = count
	}

	targets := make([]string, 0, len(counts))
	for target := range counts {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	var commands [][]string
	for _, target := range targets {
		count := counts[target]
		if count.current != count.want {
			commands = append(commands, []string{
				"set-option", "-w", "-t", target,
				"@" + WindowUnreadCountOption, strconv.Itoa(count.want),
			})
		}
	}
	return runCommands(commands...)
}

func windowTarget(state PaneState) string {
	if dot := strings.LastIndex(state.Target, "."); dot >= 0 {
		return state.Target[:dot]
	}
	if state.Session == "" {
		return ""
	}
	return fmt.Sprintf("=%s:%d", state.Session, state.WindowIndex)
}

func samePublicState(left, right PaneState) bool {
	return left.Watch == right.Watch &&
		left.WatchSet == right.WatchSet &&
		left.State == right.State &&
		left.Since == right.Since &&
		left.Changed == right.Changed &&
		left.Proc == right.Proc &&
		left.ReadHash == right.ReadHash &&
		left.ReadLines == right.ReadLines
}

func withPaneLock(paneID string, operation func() error) error {
	channel := "tmx-attn-state-" + paneID
	if err := lockState(channel); err != nil {
		return err
	}
	operationErr := operation()
	unlockErr := unlockState(channel)
	return errors.Join(operationErr, unlockErr)
}
