package attn

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestPaneStateSetGetClearAndMissingOptions(t *testing.T) {
	fake := newFakeStateBackend()
	stubStateBackend(t, fake)

	got, err := Get("%1")
	if err != nil {
		t.Fatalf("Get() absent state error = %v", err)
	}
	if got.Watch || got.WatchSet || got.State != "" || got.Since != 0 || got.Changed != 0 ||
		got.Hash != "" || got.Proc != "" || got.Fired {
		t.Fatalf("Get() absent state = %#v, want zero values", got)
	}

	want := PaneState{
		Watch: true, State: StateUnread, Since: 1234, Changed: 1200,
		Hash: "screen-a", Proc: "claude", Fired: true,
	}
	if err := Set("%1", want); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	got, err = Get("%1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != "%1" || !got.Watch || !got.WatchSet || got.State != StateUnread ||
		got.Since != 1234 || got.Changed != 1200 || got.Hash != "screen-a" ||
		got.Proc != "claude" || !got.Fired {
		t.Fatalf("Get() = %#v, want written contract", got)
	}
	if fake.windowCount != 1 {
		t.Fatalf("window unread count = %d, want 1", fake.windowCount)
	}

	want.Hash = "screen-b"
	if err := Set("%1", want); err != nil {
		t.Fatalf("second unread Set() error = %v", err)
	}
	if fake.windowCount != 1 {
		t.Fatalf("repeated unread count = %d, want 1", fake.windowCount)
	}

	want.State = StateQuiet
	if err := Set("%1", want); err != nil {
		t.Fatalf("quiet Set() error = %v", err)
	}
	if fake.windowCount != 0 {
		t.Fatalf("cleared unread count = %d, want 0", fake.windowCount)
	}

	want.State = StateUnread
	if err := Set("%1", want); err != nil {
		t.Fatalf("third unread Set() error = %v", err)
	}
	if err := Clear("%1"); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}
	got, err = Get("%1")
	if err != nil {
		t.Fatalf("Get() after Clear error = %v", err)
	}
	if got.WatchSet || got.State != "" || got.Since != 0 || got.Changed != 0 ||
		got.Hash != "" || got.Proc != "" || got.Fired {
		t.Fatalf("Get() after Clear = %#v, want zero values", got)
	}
	if fake.windowCount != 0 {
		t.Fatalf("window unread count after Clear = %d, want 0", fake.windowCount)
	}
}

func TestUpdateIfUnreadFastPathReadsOnlyState(t *testing.T) {
	for _, test := range []struct {
		name  string
		state AttentionState
	}{
		{name: "missing"},
		{name: "quiet", state: StateQuiet},
		{name: "active", state: StateActive},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeStateBackend()
			if test.state != "" {
				fake.options["%1"] = map[string]string{StateOption: string(test.state)}
			}
			stubStateBackend(t, fake)

			called := false
			err := UpdateIfUnread("%1", func(state PaneState) PaneState {
				called = true
				return state
			})
			if err != nil {
				t.Fatalf("UpdateIfUnread() error = %v", err)
			}
			if called {
				t.Fatal("UpdateIfUnread() called update for a non-unread pane")
			}
			if fake.showCalls != 1 || fake.displayCalls != 0 || fake.lockCalls != 0 ||
				fake.setCalls != 0 || len(fake.adjustments) != 0 {
				t.Fatalf(
					"fast-path calls = show:%d display:%d lock:%d set:%d adjust:%v, want one show only",
					fake.showCalls, fake.displayCalls, fake.lockCalls, fake.setCalls, fake.adjustments,
				)
			}
		})
	}
}

func TestUpdateIfUnreadUsesSetTransitionAndRechecksUnderLock(t *testing.T) {
	t.Run("updates unread state", func(t *testing.T) {
		fake := newFakeStateBackend()
		fake.options["%1"] = map[string]string{
			WatchOption:   "1",
			StateOption:   string(StateUnread),
			SinceOption:   "42",
			ChangedOption: "40",
			HashOption:    "screen",
			ProcOption:    "101:claude",
			FiredOption:   "1",
		}
		fake.windowCount = 1
		stubStateBackend(t, fake)

		err := UpdateIfUnread("%1", func(state PaneState) PaneState {
			if state.ID != "%1" || !state.Watch || state.Hash != "screen" {
				t.Fatalf("update state = %#v, want complete pane state", state)
			}
			state.State = StateQuiet
			state.Since = 500
			return state
		})
		if err != nil {
			t.Fatalf("UpdateIfUnread() error = %v", err)
		}
		if got := fake.options["%1"][StateOption]; got != string(StateQuiet) {
			t.Fatalf("state option = %q, want quiet", got)
		}
		if got := fake.options["%1"][SinceOption]; got != "500" {
			t.Fatalf("since option = %q, want 500", got)
		}
		if fake.windowCount != 0 || !slices.Equal(fake.adjustments, []int{-1}) {
			t.Fatalf("window count = %d, adjustments = %v; want 0 and [-1]", fake.windowCount, fake.adjustments)
		}
	})

	t.Run("loses concurrent transition", func(t *testing.T) {
		fake := newFakeStateBackend()
		fake.options["%1"] = map[string]string{StateOption: string(StateUnread)}
		fake.onLock = func() {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.options["%1"][StateOption] = string(StateActive)
		}
		stubStateBackend(t, fake)

		called := false
		err := UpdateIfUnread("%1", func(state PaneState) PaneState {
			called = true
			return state
		})
		if err != nil {
			t.Fatalf("UpdateIfUnread() error = %v", err)
		}
		if called || fake.setCalls != 0 || len(fake.adjustments) != 0 {
			t.Fatalf("race path called=%t set=%d adjustments=%v, want no mutation", called, fake.setCalls, fake.adjustments)
		}
		if fake.showCalls != 2 || fake.displayCalls != 1 || fake.lockCalls != 1 {
			t.Fatalf(
				"race-path calls = show:%d display:%d lock:%d, want 2, 1, 1",
				fake.showCalls, fake.displayCalls, fake.lockCalls,
			)
		}
	})
}

func TestSnapshotReadsAllAttentionOptionsInOnePass(t *testing.T) {
	fake := newFakeStateBackend()
	fake.listOutput = strings.Join([]string{
		strings.Join([]string{"%1", "work:1.0", "work", "1", "agent", "0", "101", "review", "claude", "", "/tmp/a", "1", "unread", "42", "40", "hash-a", "claude", "1", "_"}, "\t"),
		strings.Join([]string{"%2", "other:3.1", "other", "3", "shell", "1", "202", "", "zsh", "", "/tmp/b", "", "", "", "", "", "", "", "_"}, "\t"),
	}, "\n")
	stubStateBackend(t, fake)

	got, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if fake.listCalls != 1 {
		t.Fatalf("list-panes calls = %d, want 1", fake.listCalls)
	}
	for _, option := range []string{
		"#{@attn_watch}", "#{@attn_state}", "#{@attn_since}", "#{@attn_changed}",
		"#{@attn_hash}", "#{@attn_proc}", "#{@attn_fired}",
	} {
		if !strings.Contains(fake.listFormat, option) {
			t.Fatalf("snapshot format %q missing %s", fake.listFormat, option)
		}
	}
	if len(got) != 2 {
		t.Fatalf("Snapshot() returned %d panes, want 2", len(got))
	}
	if got[0].ID != "%1" || !got[0].Watch || got[0].State != StateUnread ||
		got[0].Since != 42 || got[0].Changed != 40 || got[0].Hash != "hash-a" ||
		got[0].Proc != "claude" || !got[0].Fired {
		t.Fatalf("watched snapshot = %#v", got[0])
	}
	if got[1].ID != "%2" || got[1].WatchSet || got[1].State != "" || got[1].Since != 0 ||
		got[1].Changed != 0 || got[1].Hash != "" || got[1].Proc != "" || got[1].Fired {
		t.Fatalf("absent snapshot = %#v, want zero state", got[1])
	}
}

func TestSnapshotTreatsMissingTmuxServerAsEmpty(t *testing.T) {
	fake := newFakeStateBackend()
	fake.listErr = errors.New("tmux list-panes: no server running")
	stubStateBackend(t, fake)

	got, err := Snapshot()
	if err != nil || len(got) != 0 {
		t.Fatalf("Snapshot() = %#v, %v; want empty inventory", got, err)
	}
}

func TestReconcileWindowUnreadCountsDropsOrphanedPaneCount(t *testing.T) {
	fake := newFakeStateBackend()
	fake.listOutput = strings.Join([]string{
		strings.Join([]string{"%1", "work:1.0", "work", "1", "agent", "0", "101", "", "claude", "", "/tmp/a", "1", "quiet", "42", "40", "hash-a", "claude", "1", "_"}, "\t"),
		strings.Join([]string{"%2", "work:2.0", "work", "2", "other", "0", "202", "", "codex", "", "/tmp/b", "1", "unread", "43", "41", "hash-b", "codex", "1", "_"}, "\t"),
	}, "\n")
	stubStateBackend(t, fake)

	if err := ReconcileWindowUnreadCounts(); err != nil {
		t.Fatalf("ReconcileWindowUnreadCounts() error = %v", err)
	}
	if got := fake.windowValues["work:1"]; got != "0" {
		t.Fatalf("work:1 unread count = %q, want 0 after unread pane disappeared", got)
	}
	if got := fake.windowValues["work:2"]; got != "1" {
		t.Fatalf("work:2 unread count = %q, want 1", got)
	}
}

func TestConcurrentStateTransitionsAdjustWindowCountOnce(t *testing.T) {
	fake := newFakeStateBackend()
	fake.options["%1"] = map[string]string{StateOption: string(StateUnread)}
	fake.windowCount = 2
	stubStateBackend(t, fake)

	runConcurrently(t, func() error { return Clear("%1") }, func() error { return Clear("%1") })
	if fake.windowCount != 1 {
		t.Fatalf("window unread count after duplicate clears = %d, want 1", fake.windowCount)
	}
	if !slices.Equal(fake.adjustments, []int{-1}) {
		t.Fatalf("clear adjustments = %#v, want [-1]", fake.adjustments)
	}

	fake.options["%2"] = map[string]string{StateOption: string(StateActive)}
	fake.windowCount = 0
	fake.adjustments = nil
	unread := PaneState{Watch: true, State: StateUnread, Since: 42, Hash: "same"}
	runConcurrently(t, func() error { return Set("%2", unread) }, func() error { return Set("%2", unread) })
	if fake.windowCount != 1 {
		t.Fatalf("window unread count after duplicate flags = %d, want 1", fake.windowCount)
	}
	if !slices.Equal(fake.adjustments, []int{1}) {
		t.Fatalf("flag adjustments = %#v, want [1]", fake.adjustments)
	}
}

func runConcurrently(t *testing.T, operations ...func() error) {
	t.Helper()
	errs := make(chan error, len(operations))
	for _, operation := range operations {
		go func() { errs <- operation() }()
	}
	for range operations {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent operation error = %v", err)
		}
	}
}

func TestResolveTargetAcceptsPaneWindowAndSession(t *testing.T) {
	fake := newFakeStateBackend()
	fake.resolutions = map[string]string{
		"%7":      "%7",
		"work:2":  "%8",
		"=tpp/5:": "%9",
	}
	stubStateBackend(t, fake)

	tests := []struct {
		name   string
		target string
		want   string
		passed string
	}{
		{name: "pane id", target: "%7", want: "%7", passed: "%7"},
		{name: "window", target: "work:2", want: "%8", passed: "work:2"},
		{name: "session", target: "tpp/5", want: "%9", passed: "=tpp/5:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveTarget(test.target)
			if err != nil {
				t.Fatalf("ResolveTarget() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveTarget() = %q, want %q", got, test.want)
			}
			if fake.lastDisplayTarget != test.passed {
				t.Fatalf("display target = %q, want %q", fake.lastDisplayTarget, test.passed)
			}
		})
	}
}

type fakeStateBackend struct {
	mu                sync.Mutex
	stateMu           sync.Mutex
	options           map[string]map[string]string
	resolutions       map[string]string
	windowCount       int
	adjustments       []int
	showCalls         int
	displayCalls      int
	lockCalls         int
	setCalls          int
	listOutput        string
	listCalls         int
	listFormat        string
	listErr           error
	lastDisplayTarget string
	windowValues      map[string]string
	onLock            func()
}

func newFakeStateBackend() *fakeStateBackend {
	return &fakeStateBackend{
		options:      make(map[string]map[string]string),
		resolutions:  make(map[string]string),
		windowValues: make(map[string]string),
	}
}

func stubStateBackend(t *testing.T, fake *fakeStateBackend) {
	t.Helper()
	originalList, originalDisplay := listPanesFormat, displayPaneFormat
	originalShow, originalSet := showPaneVar, setPaneVar
	originalUnset, originalAdjust := unsetPaneVar, adjustWindowVar
	originalSetWindow := setWindowVar
	originalLock, originalUnlock := lockState, unlockState

	listPanesFormat = func(format string) (string, error) {
		fake.listCalls++
		fake.listFormat = format
		return fake.listOutput, fake.listErr
	}
	displayPaneFormat = func(target, _ string) (string, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.displayCalls++
		fake.lastDisplayTarget = target
		if resolved, ok := fake.resolutions[target]; ok {
			return resolved, nil
		}
		return target, nil
	}
	showPaneVar = func(target, key string) (string, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.showCalls++
		return fake.options[target][key], nil
	}
	setPaneVar = func(target, key, value string) error {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.setCalls++
		if fake.options[target] == nil {
			fake.options[target] = make(map[string]string)
		}
		fake.options[target][key] = value
		return nil
	}
	unsetPaneVar = func(target, key string) error {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		delete(fake.options[target], key)
		return nil
	}
	adjustWindowVar = func(_ string, _ string, delta int) error {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.adjustments = append(fake.adjustments, delta)
		fake.windowCount += delta
		if fake.windowCount < 0 {
			fake.windowCount = 0
		}
		return nil
	}
	setWindowVar = func(target, _ string, value string) error {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.windowValues[target] = value
		return nil
	}
	lockState = func(_ string) error {
		fake.stateMu.Lock()
		fake.lockCalls++
		if fake.onLock != nil {
			fake.onLock()
		}
		return nil
	}
	unlockState = func(_ string) error {
		fake.stateMu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		listPanesFormat, displayPaneFormat = originalList, originalDisplay
		showPaneVar, setPaneVar = originalShow, originalSet
		unsetPaneVar, adjustWindowVar = originalUnset, originalAdjust
		setWindowVar = originalSetWindow
		lockState, unlockState = originalLock, originalUnlock
	})
}
