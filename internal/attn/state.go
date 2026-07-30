package attn

import (
	"errors"
	"fmt"
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
	Watch    bool
	WatchSet bool
	State    AttentionState
	Since    int64
	Changed  int64
	Hash     string
	Proc     string
	Fired    bool
}

const snapshotFormat = "#{pane_id}\t#{session_name}:#{window_index}.#{pane_index}\t#{session_name}\t#{window_index}\t#{window_name}\t#{pane_index}\t#{pane_pid}\t#{@pane_label}\t#{pane_current_command}\t#{@fip_buffer}\t#{pane_current_path}\t#{@attn_watch}\t#{@attn_state}\t#{@attn_since}\t#{@attn_changed}\t#{@attn_hash}\t#{@attn_proc}\t#{@attn_fired}\t_"

var (
	listPanesFormat   = tmux.ListPanesFormat
	displayPaneFormat = tmux.DisplayPaneFormat
	showPaneVar       = tmux.ShowPaneVar
	setPaneVar        = tmux.SetPaneVar
	unsetPaneVar      = tmux.UnsetPaneVar
	adjustWindowVar   = tmux.AdjustWindowVar
	setWindowVar      = tmux.SetWindowVar
	lockState         = tmux.WaitForLock
	unlockState       = tmux.WaitForUnlock
)

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
		parts := strings.SplitN(line, "\t", 19)
		if len(parts) != 19 {
			continue
		}
		windowIndex, _ := strconv.Atoi(parts[3])
		paneIndex, _ := strconv.Atoi(parts[5])
		pid, _ := strconv.Atoi(parts[6])
		since, _ := strconv.ParseInt(parts[13], 10, 64)
		changed, _ := strconv.ParseInt(parts[14], 10, 64)
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
			Watch:    parts[11] == "1",
			WatchSet: parts[11] != "",
			State:    AttentionState(parts[12]),
			Since:    since,
			Changed:  changed,
			Hash:     parts[15],
			Proc:     parts[16],
			Fired:    parts[17] == "1",
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
	values := make([]string, 7)
	for i, key := range []string{WatchOption, StateOption, SinceOption, ChangedOption, HashOption, ProcOption, FiredOption} {
		value, err := showPaneVar(paneID, key)
		if err != nil {
			return PaneState{}, err
		}
		values[i] = value
	}
	since, _ := strconv.ParseInt(values[2], 10, 64)
	changed, _ := strconv.ParseInt(values[3], 10, 64)
	return PaneState{
		Watch:    values[0] == "1",
		WatchSet: values[0] != "",
		State:    AttentionState(values[1]),
		Since:    since,
		Changed:  changed,
		Hash:     values[4],
		Proc:     values[5],
		Fired:    values[6] == "1",
	}, nil
}

// Set writes the complete pane contract and updates the window unread count
// when the pane crosses the unread boundary.
func Set(target string, state PaneState) error {
	if state.State != StateActive && state.State != StateQuiet && state.State != StateUnread {
		return fmt.Errorf("invalid attention state %q", state.State)
	}
	paneID, err := ResolveTarget(target)
	if err != nil {
		return err
	}
	return withPaneLock(paneID, func() error {
		previous, err := showPaneVar(paneID, StateOption)
		if err != nil {
			return err
		}

		watch := "0"
		if state.Watch {
			watch = "1"
		}
		fired := "0"
		if state.Fired {
			fired = "1"
		}
		values := []struct {
			key   string
			value string
		}{
			{WatchOption, watch},
			{SinceOption, strconv.FormatInt(state.Since, 10)},
			{ChangedOption, strconv.FormatInt(state.Changed, 10)},
			{HashOption, state.Hash},
			{ProcOption, state.Proc},
			{FiredOption, fired},
			{StateOption, string(state.State)},
		}
		for _, value := range values {
			if err := setPaneVar(paneID, value.key, value.value); err != nil {
				return err
			}
		}

		switch {
		case AttentionState(previous) != StateUnread && state.State == StateUnread:
			return adjustWindowVar(paneID, WindowUnreadCountOption, 1)
		case AttentionState(previous) == StateUnread && state.State != StateUnread:
			return adjustWindowVar(paneID, WindowUnreadCountOption, -1)
		default:
			return nil
		}
	})
}

// Clear unsets the complete pane contract. Clearing an unread pane also
// decrements its window aggregate.
func Clear(target string) error {
	paneID, err := ResolveTarget(target)
	if err != nil {
		return err
	}
	return withPaneLock(paneID, func() error {
		previous, err := showPaneVar(paneID, StateOption)
		if err != nil {
			return err
		}

		var errs []error
		for _, key := range []string{WatchOption, StateOption, SinceOption, ChangedOption, HashOption, ProcOption, FiredOption} {
			if err := unsetPaneVar(paneID, key); err != nil {
				errs = append(errs, err)
			}
		}
		if AttentionState(previous) == StateUnread {
			if err := adjustWindowVar(paneID, WindowUnreadCountOption, -1); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
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

	counts := make(map[string]int)
	for _, state := range states {
		target := windowTarget(state)
		if target == "" {
			continue
		}
		if _, exists := counts[target]; !exists {
			counts[target] = 0
		}
		if state.State == StateUnread {
			counts[target]++
		}
	}

	var errs []error
	for target, count := range counts {
		if err := setWindowVar(target, WindowUnreadCountOption, strconv.Itoa(count)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

func withPaneLock(paneID string, operation func() error) error {
	channel := "tmx-attn-state-" + paneID
	if err := lockState(channel); err != nil {
		return err
	}
	operationErr := operation()
	unlockErr := unlockState(channel)
	return errors.Join(operationErr, unlockErr)
}
