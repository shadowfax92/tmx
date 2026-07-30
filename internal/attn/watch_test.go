package attn

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tmx/internal/tmux"
)

func TestScreenHashStripsANSIAndUsesOnlyTrailingLines(t *testing.T) {
	colored := "ignored\nalso ignored\n\u001b[31mkeep\u001b[0m\nlast"
	plain := "different\nprefix\nkeep\nlast"

	if got, want := screenHash(colored, 2), screenHash(plain, 2); got != want {
		t.Fatalf("trailing ANSI-stripped hashes differ: %q != %q", got, want)
	}
	if got, want := screenHash("one\nkeep\nchanged", 2), screenHash(plain, 2); got == want {
		t.Fatalf("material trailing-line change did not change hash: %q", got)
	}
}

func TestObservePaneFlagsExactlyOncePerQuietEpisodeAndRearmsOnChange(t *testing.T) {
	quietFor := 90 * time.Second
	state, changed := observePane(PaneState{}, "screen-a", "claude", false, unixTime(100), quietFor)
	assertWatchState(t, state, changed, StateActive, true, false)

	state, changed = observePane(state, "screen-a", "claude", false, unixTime(101), quietFor)
	assertWatchState(t, state, changed, StateQuiet, true, false)

	state, changed = observePane(state, "screen-a", "claude", false, unixTime(189), quietFor)
	assertWatchState(t, state, changed, StateQuiet, false, false)

	state, changed = observePane(state, "screen-a", "claude", false, unixTime(190), quietFor)
	assertWatchState(t, state, changed, StateUnread, true, true)
	flaggedAt := state.Since

	state, changed = observePane(state, "screen-a", "claude", false, unixTime(300), quietFor)
	assertWatchState(t, state, changed, StateUnread, false, true)
	if state.Since != flaggedAt {
		t.Fatalf("unchanged unread episode timestamp = %d, want %d", state.Since, flaggedAt)
	}

	state, changed = observePane(state, "screen-a", "claude", true, unixTime(301), quietFor)
	assertWatchState(t, state, changed, StateQuiet, true, true)
	if state.ReadHash != "screen-a" {
		t.Fatalf("visited read baseline = %q, want current screen hash", state.ReadHash)
	}

	state, changed = observePane(state, "screen-a", "claude", false, unixTime(500), quietFor)
	assertWatchState(t, state, changed, StateQuiet, false, true)

	state, changed = observePane(state, "screen-b", "claude", false, unixTime(501), quietFor)
	assertWatchState(t, state, changed, StateActive, true, false)
	if state.ReadHash != "" {
		t.Fatalf("changed screen retained read baseline %q", state.ReadHash)
	}

	state, changed = observePane(state, "screen-b", "claude", false, unixTime(502), quietFor)
	assertWatchState(t, state, changed, StateQuiet, true, false)

	state, changed = observePane(state, "screen-b", "claude", false, unixTime(591), quietFor)
	assertWatchState(t, state, changed, StateUnread, true, true)
}

func TestObservePaneConsumesQuietEpisodeWhenAlreadyFocused(t *testing.T) {
	state := PaneState{
		Watch:    true,
		WatchSet: true,
		State:    StateQuiet,
		Since:    100,
		Changed:  100,
		Hash:     "screen",
		Proc:     "claude",
	}

	state, changed := observePane(state, "screen", "claude", true, unixTime(190), 90*time.Second)
	assertWatchState(t, state, changed, StateQuiet, true, true)
	if state.ReadHash != "screen" {
		t.Fatalf("focused quiet baseline = %q, want current screen hash", state.ReadHash)
	}

	state, changed = observePane(state, "screen", "claude", false, unixTime(400), 90*time.Second)
	assertWatchState(t, state, changed, StateQuiet, false, true)
}

func TestObservePaneReadBaselineDoesNotConsumeLaterProcessChange(t *testing.T) {
	state := PaneState{
		Watch: true, WatchSet: true, State: StateQuiet,
		Since: 100, Changed: 90, Hash: "screen", Proc: "101:claude", Fired: true,
		ReadHash: "screen", ReadLines: 30,
	}

	next, changed := observePane(
		state, "screen", "202:claude", false, unixTime(200), 90*time.Second,
	)
	assertWatchState(t, next, changed, StateActive, true, false)
	if next.Proc != "202:claude" || next.ReadHash != "" || next.ReadLines != 0 {
		t.Fatalf("later process state = %#v, want re-armed with cleared read baseline", next)
	}
}

func TestObservePaneUnwatchedNeverFlagsAndProcessChangeRearms(t *testing.T) {
	state := PaneState{
		Watch:    false,
		WatchSet: true,
		State:    StateActive,
		Since:    100,
		Changed:  100,
		Hash:     "screen-a",
		Proc:     "claude",
	}

	state, changed := observePane(state, "screen-a", "claude", false, unixTime(500), 90*time.Second)
	assertWatchState(t, state, changed, StateActive, false, false)
	if state.Watch {
		t.Fatal("unchanged opted-out pane re-armed")
	}

	state, changed = observePane(state, "screen-b", "claude", false, unixTime(501), 90*time.Second)
	assertWatchState(t, state, changed, StateActive, true, false)
	if state.Watch {
		t.Fatal("screen activity alone re-armed opted-out pane")
	}

	state, changed = observePane(state, "screen-b", "codex", false, unixTime(502), 90*time.Second)
	assertWatchState(t, state, changed, StateActive, true, false)
	if !state.Watch || state.Proc != "codex" {
		t.Fatalf("process change state = %#v, want watched codex", state)
	}
}

func TestObservePaneUnwatchedConsumesExistingUnreadFlag(t *testing.T) {
	state := PaneState{
		Watch:    false,
		WatchSet: true,
		State:    StateUnread,
		Since:    100,
		Changed:  100,
		Hash:     "screen",
		Proc:     "101:claude",
		Fired:    true,
	}

	state, changed := observePane(state, "screen", "101:claude", false, unixTime(200), 90*time.Second)
	assertWatchState(t, state, changed, StateQuiet, true, true)
	if state.Watch {
		t.Fatal("unwatched unread pane was re-armed")
	}
}

func TestObservePaneTracksLastScreenChangeSeparatelyFromStateAge(t *testing.T) {
	quietFor := 90 * time.Second
	state, _ := observePane(PaneState{}, "screen-a", "claude", false, unixTime(100), quietFor)
	if state.Changed != 100 {
		t.Fatalf("initial changed timestamp = %d, want 100", state.Changed)
	}

	state, _ = observePane(state, "screen-a", "claude", false, unixTime(101), quietFor)
	state, _ = observePane(state, "screen-a", "claude", false, unixTime(190), quietFor)
	if state.State != StateUnread || state.Since != 190 || state.Changed != 100 {
		t.Fatalf("unread state = %#v, want state age 190 and screen change 100", state)
	}

	state, _ = observePane(state, "screen-a", "claude", true, unixTime(200), quietFor)
	if state.State != StateQuiet || state.Since != 200 || state.Changed != 100 {
		t.Fatalf("visited state = %#v, want state age 200 and screen change 100", state)
	}

	state, _ = observePane(state, "screen-b", "claude", false, unixTime(201), quietFor)
	if state.State != StateActive || state.Since != 201 || state.Changed != 201 {
		t.Fatalf("changed screen state = %#v, want both timestamps 201", state)
	}

	legacy := PaneState{
		Watch: true, WatchSet: true, State: StateQuiet, Since: 50,
		Hash: "screen", Proc: "claude",
	}
	legacy, changed := observePane(legacy, "screen", "claude", false, unixTime(300), quietFor)
	if !changed || legacy.Changed != 300 {
		t.Fatalf("legacy state = %#v changed=%v, want conservative timestamp 300", legacy, changed)
	}
}

func TestSelectReapCandidatesUsesLastScreenChangeIncludesUnwatchedAndProtectsFocused(t *testing.T) {
	now := unixTime(10 * 24 * 60 * 60)
	ttl := 24 * time.Hour
	state := func(id, target string, watch bool, changed time.Time) PaneState {
		return PaneState{
			PaneInfo: tmux.PaneInfo{ID: id, Target: target},
			Watch:    watch, WatchSet: true, State: StateQuiet,
			Since:   now.Unix(),
			Changed: changed.Unix(),
			Hash:    "screen",
		}
	}
	states := []PaneState{
		state("%watched", "work:1.0", true, now.Add(-48*time.Hour)),
		state("%unwatched", "work:2.0", false, now.Add(-25*time.Hour)),
		state("%fresh", "work:3.0", true, now.Add(-time.Hour)),
		state("%focused", "work:4.0", true, now.Add(-72*time.Hour)),
		{
			PaneInfo: tmux.PaneInfo{ID: "%active", Target: "work:5.0"},
			Watch:    true, WatchSet: true, State: StateActive,
			Changed: now.Add(-96 * time.Hour).Unix(),
		},
		{PaneInfo: tmux.PaneInfo{ID: "%missing"}, Watch: true, WatchSet: true, State: StateQuiet},
		{PaneInfo: tmux.PaneInfo{ID: "%ordinary"}},
	}

	got := selectReapCandidates(states, map[string]bool{"%focused": true}, ttl, now)
	if len(got.Candidates) != 2 ||
		got.Candidates[0].ID != "%watched" ||
		got.Candidates[1].ID != "%unwatched" {
		t.Fatalf("candidates = %#v, want watched then explicitly unwatched", got.Candidates)
	}
	if got.Candidates[0].Age != 48*time.Hour || got.Candidates[1].Age != 25*time.Hour {
		t.Fatalf("candidate ages = %v, %v", got.Candidates[0].Age, got.Candidates[1].Age)
	}
	if got.MissingTimestamps != 1 {
		t.Fatalf("missing timestamps = %d, want 1", got.MissingTimestamps)
	}
}

func TestWatcherKeepsStreamingStatePrivateAndSkipsIdleWindowCaptures(t *testing.T) {
	options := WatchOptions{
		Poll: time.Second, GracePeriods: 2, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	}
	watcher, err := NewWatcher(options)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	watcher.logger = logForTest(&logs)

	rollCall := PaneState{
		PaneInfo:       tmux.PaneInfo{ID: "%1", Target: "work:1.0", Session: "work", WindowIndex: 1},
		WindowID:       "@1",
		WindowActivity: 100,
	}
	process := "101:claude"
	captures, snapshots, reconciles := 0, 0, 0
	var writes []PaneState
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) {
			snapshots++
			return []PaneState{rollCall}, nil
		},
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: rollCall.PaneInfo, ProcessFingerprint: process,
			}}, nil
		},
		focused: func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture: func(string, int) (string, error) {
			captures++
			return "screen-" + strconv.Itoa(captures), nil
		},
		set: func(_ string, expected, next PaneState) error {
			if !samePublicState(expected, rollCall) {
				t.Fatalf("transition expected %#v, roll call %#v", expected, publicState(rollCall))
			}
			writes = append(writes, next)
			applyPublicForTest(&rollCall, next)
			return nil
		},
		clear: func(string) error { return nil },
		reconcile: func([]PaneState) error {
			reconciles++
			return nil
		},
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	rollCall.WindowActivity = 101
	if err := watcher.Tick(unixTime(101)); err != nil {
		t.Fatal(err)
	}
	rollCall.WindowActivity = 102
	if err := watcher.Tick(unixTime(102)); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 || writes[0].State != StateActive {
		t.Fatalf("streaming writes = %#v, want only initial active transition", writes)
	}

	// Activity is unchanged, so these ticks reuse the private hash. The first
	// settles active -> quiet and publishes the frozen last-change timestamp;
	// the second reaches the quiet threshold and flags unread.
	if err := watcher.Tick(unixTime(103)); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Tick(unixTime(104)); err != nil {
		t.Fatal(err)
	}

	if captures != 3 {
		t.Fatalf("capture calls = %d, want 3 (idle ticks skipped)", captures)
	}
	if len(writes) != 3 || writes[1].State != StateQuiet || writes[1].Changed != 102 ||
		writes[2].State != StateUnread {
		t.Fatalf("transition writes = %#v, want active, quiet(changed=102), unread", writes)
	}
	if rollCall.Hash != "" || rollCall.Fired {
		t.Fatalf("roll call contains daemon-private fields: %#v", rollCall)
	}
	if memory := watcher.panes["%1"].current; memory.Hash == "" || !memory.Fired {
		t.Fatalf("watcher private state = %#v, want learned hash and fired episode", memory)
	}
	if snapshots != 5 || reconciles != 1 {
		t.Fatalf("snapshots=%d reconciles=%d, want one roll call/tick and no second reconcile snapshot", snapshots, reconciles)
	}
	if !strings.Contains(logs.String(), "state=active->quiet") {
		t.Fatalf("transition log = %q, want state transition", logs.String())
	}
}

func TestWatcherReconcilesCLIChangesAndRearmsUnwatchOnProcessChange(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Second, GracePeriods: 30, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude", "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}

	rollCall := PaneState{
		PaneInfo:       tmux.PaneInfo{ID: "%1", Target: "work:1.0", Session: "work", WindowIndex: 1},
		WindowID:       "@1",
		WindowActivity: 100,
	}
	process := "101:claude"
	captures := 0
	var writes []PaneState
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return []PaneState{rollCall}, nil },
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: rollCall.PaneInfo, ProcessFingerprint: process,
			}}, nil
		},
		focused: func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture: func(string, int) (string, error) {
			captures++
			return "same screen", nil
		},
		set: func(_ string, expected, next PaneState) error {
			if !samePublicState(expected, rollCall) {
				t.Fatalf("transition expected %#v, roll call %#v", expected, publicState(rollCall))
			}
			writes = append(writes, next)
			applyPublicForTest(&rollCall, next)
			return nil
		},
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Tick(unixTime(101)); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 2 || rollCall.State != StateQuiet {
		t.Fatalf("initial transitions = %#v roll call=%#v", writes, rollCall)
	}

	// These are the public mutations performed by unwatch, mark unread,
	// snooze, and mark read in separate CLI processes. Each must be accepted
	// by the daemon without a compensating write.
	rollCall.Watch = false
	rollCall.State = StateQuiet
	rollCall.Since = 110
	if err := watcher.Tick(unixTime(110)); err != nil {
		t.Fatal(err)
	}
	rollCall.Watch = true
	rollCall.State = StateUnread
	rollCall.Since = 111
	if err := watcher.Tick(unixTime(111)); err != nil {
		t.Fatal(err)
	}
	rollCall.Since = 112 // snooze
	if err := watcher.Tick(unixTime(112)); err != nil {
		t.Fatal(err)
	}
	rollCall.State = StateQuiet // mark read
	rollCall.Since = 113
	if err := watcher.Tick(unixTime(113)); err != nil {
		t.Fatal(err)
	}
	rollCall.Watch = false // unwatch again
	rollCall.Since = 114
	if err := watcher.Tick(unixTime(114)); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 2 {
		t.Fatalf("daemon fought CLI mutations with writes: %#v", writes[2:])
	}

	process = "202:codex"
	if err := watcher.Tick(unixTime(115)); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 3 || !rollCall.Watch || rollCall.State != StateActive ||
		rollCall.Proc != process {
		t.Fatalf("process re-arm writes=%#v roll call=%#v", writes, rollCall)
	}
	if captures != 1 {
		t.Fatalf("capture calls = %d, want first sight only for idle window", captures)
	}

	if err := watcher.Tick(unixTime(116)); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 4 || rollCall.State != StateQuiet || rollCall.Changed != 115 {
		t.Fatalf("settled process episode writes=%#v roll call=%#v", writes, rollCall)
	}
	selection := selectReapCandidates(
		[]PaneState{rollCall}, map[string]bool{}, time.Second, unixTime(118),
	)
	if len(selection.Candidates) != 1 || selection.Candidates[0].ID != "%1" {
		t.Fatalf("reap selection after quiet transition = %#v", selection)
	}
}

func TestWatcherAdoptsExternalReadBaselineBeforeComparingScreen(t *testing.T) {
	options := WatchOptions{
		Poll: time.Second, GracePeriods: 30, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	}
	watcher, err := NewWatcher(options)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	watcher.logger = logForTest(&logs)

	beforeRead := PaneState{
		PaneInfo:       tmux.PaneInfo{ID: "%1", Target: "work:1.0", Session: "work", WindowIndex: 1},
		WindowID:       "@1",
		WindowActivity: 100,
		Watch:          true,
		WatchSet:       true,
		State:          StateUnread,
		Since:          90,
		Changed:        80,
		Proc:           "101:claude",
		Fired:          true,
	}
	private := beforeRead
	private.Hash = screenHash("screen before focus", options.CaptureLines)
	watcher.panes["%1"] = &watchedPane{
		current:   private,
		published: publicState(beforeRead),
	}

	screen := "screen after focus"
	baseline := screenHash(screen, options.CaptureLines)
	process := "202:claude"
	rollCall := beforeRead
	rollCall.State = StateQuiet
	rollCall.Since = 100
	rollCall.Proc = process
	rollCall.ReadHash = baseline
	rollCall.ReadLines = options.CaptureLines
	rollCall.WindowActivity = 101
	var writes []PaneState
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return []PaneState{rollCall}, nil },
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: rollCall.PaneInfo, ProcessFingerprint: process,
			}}, nil
		},
		focused: func() (map[string]bool, error) { return map[string]bool{"%1": true}, nil },
		capture: func(string, int) (string, error) { return screen, nil },
		set: func(_ string, expected, next PaneState) error {
			if !samePublicState(expected, rollCall) {
				t.Fatalf("transition expected %#v, roll call %#v", expected, publicState(rollCall))
			}
			writes = append(writes, next)
			applyPublicForTest(&rollCall, next)
			return nil
		},
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	memory := watcher.panes["%1"].current
	if len(writes) != 0 || memory.State != StateQuiet || memory.Hash != baseline ||
		memory.ReadHash != baseline || memory.Proc != process || !memory.Fired {
		t.Fatalf("post-read writes=%#v memory=%#v, want quiet adopted baseline", writes, memory)
	}
	if !strings.Contains(logs.String(), "read baseline adopted pane=%1") {
		t.Fatalf("watcher logs = %q, want adopted read baseline", logs.String())
	}

	screen = "genuine later output"
	rollCall.WindowActivity = 102
	if err := watcher.Tick(unixTime(101)); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 || writes[0].State != StateActive || writes[0].ReadHash != "" {
		t.Fatalf("later-output writes = %#v, want active with cleared read baseline", writes)
	}
	if !strings.Contains(logs.String(), "read baseline changed pane=%1") ||
		!strings.Contains(logs.String(), "rearm=true") {
		t.Fatalf("watcher logs = %q, want explicit baseline-change rearm", logs.String())
	}
}

func TestWatcherRestoresReadBaselineAfterRestart(t *testing.T) {
	options := WatchOptions{
		Poll: time.Second, GracePeriods: 30, Period: time.Second,
		CaptureLines: 2, Agents: []string{"claude"},
	}
	watcher, err := NewWatcher(options)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	watcher.logger = logForTest(&logs)

	screen := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight"
	baselineLines := 7
	baseline := screenHash(screen, baselineLines)
	rollingHash := screenHash(screen, options.CaptureLines)
	rollCall := PaneState{
		PaneInfo:       tmux.PaneInfo{ID: "%1", Target: "work:1.0"},
		WindowID:       "@1",
		WindowActivity: 100,
		Watch:          true,
		WatchSet:       true,
		State:          StateQuiet,
		Since:          90,
		Changed:        80,
		Proc:           "101:claude",
		ReadHash:       baseline,
		ReadLines:      baselineLines,
	}
	writes := 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return []PaneState{rollCall}, nil },
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: rollCall.PaneInfo, ProcessFingerprint: "101:claude",
			}}, nil
		},
		focused: func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture: func(_ string, lines int) (string, error) {
			if lines != baselineLines {
				t.Fatalf("capture lines = %d, want baseline-compatible %d", lines, baselineLines)
			}
			return screen, nil
		},
		set: func(string, PaneState, PaneState) error {
			writes++
			return nil
		},
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	memory := watcher.panes["%1"].current
	if writes != 0 || memory.State != StateQuiet || memory.Hash != rollingHash || !memory.Fired {
		t.Fatalf("restart writes=%d memory=%#v, want restored quiet baseline", writes, memory)
	}
	if !strings.Contains(logs.String(), "read baseline adopted pane=%1 source=restart") {
		t.Fatalf("watcher logs = %q, want restart baseline adoption", logs.String())
	}
}

func TestWatcherRetriesFailedTransitionWithoutAnotherPrivateChange(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Second, GracePeriods: 30, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rollCall := PaneState{
		PaneInfo:       tmux.PaneInfo{ID: "%1", Target: "work:1.0"},
		WindowID:       "@1",
		WindowActivity: 100,
	}
	attempts := 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return []PaneState{rollCall}, nil },
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: rollCall.PaneInfo, ProcessFingerprint: "101:claude",
			}}, nil
		},
		focused: func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture: func(string, int) (string, error) { return "same", nil },
		set: func(_ string, _ PaneState, next PaneState) error {
			attempts++
			if attempts == 2 {
				return errors.New("temporary set failure")
			}
			applyPublicForTest(&rollCall, next)
			return nil
		},
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Tick(unixTime(101)); err != nil {
		t.Fatal(err)
	}
	if rollCall.State != StateActive {
		t.Fatalf("failed transition changed roll call to %s", rollCall.State)
	}
	if err := watcher.Tick(unixTime(102)); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || rollCall.State != StateQuiet {
		t.Fatalf("transition attempts=%d roll call=%#v, want retry to quiet", attempts, rollCall)
	}
}

func TestWatcherCommitsFailedInitialActiveBeforeSettlingQuiet(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Second, GracePeriods: 30, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rollCall := PaneState{
		PaneInfo:       tmux.PaneInfo{ID: "%1", Target: "work:1.0"},
		WindowID:       "@1",
		WindowActivity: 100,
	}
	attempts, captures := 0, 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return []PaneState{rollCall}, nil },
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: rollCall.PaneInfo, ProcessFingerprint: "101:claude",
			}}, nil
		},
		focused: func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture: func(string, int) (string, error) {
			captures++
			return "same", nil
		},
		set: func(_ string, _ PaneState, next PaneState) error {
			attempts++
			if attempts == 1 {
				return errors.New("temporary initial write failure")
			}
			applyPublicForTest(&rollCall, next)
			return nil
		},
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Tick(unixTime(101)); err != nil {
		t.Fatal(err)
	}
	if rollCall.State != StateActive || rollCall.Changed != 0 {
		t.Fatalf("retried initial transition = %#v, want active with no reap timestamp", rollCall)
	}
	if err := watcher.Tick(unixTime(102)); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || captures != 1 ||
		rollCall.State != StateQuiet || rollCall.Changed != 100 {
		t.Fatalf("attempts=%d captures=%d roll call=%#v, want quiet with frozen change timestamp",
			attempts, captures, rollCall)
	}
}

func TestWatcherRetriesCaptureWhenWindowActivityCouldNotBeObserved(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Second, GracePeriods: 30, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rollCall := PaneState{
		PaneInfo:       tmux.PaneInfo{ID: "%1", Target: "work:1.0"},
		WindowID:       "@1",
		WindowActivity: 100,
	}
	captures := 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return []PaneState{rollCall}, nil },
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: rollCall.PaneInfo, ProcessFingerprint: "101:claude",
			}}, nil
		},
		focused: func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture: func(string, int) (string, error) {
			captures++
			if captures == 2 {
				return "", errors.New("temporary capture failure")
			}
			return "screen-" + strconv.Itoa(captures), nil
		},
		set: func(_ string, _ PaneState, next PaneState) error {
			applyPublicForTest(&rollCall, next)
			return nil
		},
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	rollCall.WindowActivity = 101
	if err := watcher.Tick(unixTime(101)); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Tick(unixTime(102)); err != nil {
		t.Fatal(err)
	}
	if captures != 3 {
		t.Fatalf("capture calls = %d, want failed activity capture retried next tick", captures)
	}
}

func TestWatcherRunLogsAndRetriesTransientTickErrors(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Millisecond, GracePeriods: 1, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	watcher.logger = logForTest(&logs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	snapshotCalls := 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) {
			snapshotCalls++
			if snapshotCalls < 3 {
				return nil, errors.New("temporary tmux failure")
			}
			cancel()
			return nil, nil
		},
		discover:  func([]string) ([]DiscoveredPane, error) { return nil, nil },
		focused:   func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture:   func(string, int) (string, error) { return "", nil },
		set:       func(string, PaneState, PaneState) error { return nil },
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if snapshotCalls != 3 {
		t.Fatalf("snapshot calls = %d, want retry through success", snapshotCalls)
	}
	if got := logs.String(); strings.Count(got, "tick error: snapshot: temporary tmux failure") != 2 {
		t.Fatalf("retry log = %q", got)
	}
}

func TestWatcherRetriesFailedFullReconcileOnNextTick(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Second, GracePeriods: 1, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciles := 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return nil, nil },
		discover: func([]string) ([]DiscoveredPane, error) { return nil, nil },
		focused:  func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture:  func(string, int) (string, error) { return "", nil },
		set:      func(string, PaneState, PaneState) error { return nil },
		clear:    func(string) error { return nil },
		reconcile: func([]PaneState) error {
			reconciles++
			if reconciles == 1 {
				return errors.New("temporary reconcile failure")
			}
			return nil
		},
	})

	if err := watcher.Tick(unixTime(100)); err == nil {
		t.Fatal("first Tick() succeeded, want reconcile error")
	}
	if err := watcher.Tick(unixTime(101)); err != nil {
		t.Fatalf("second Tick() error = %v", err)
	}
	if reconciles != 2 {
		t.Fatalf("reconcile calls = %d, want immediate retry", reconciles)
	}
}

func TestWatcherFullReconcileDropsVanishedPrivateState(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Second, GracePeriods: 1, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	watcher.ticks = 9
	watcher.panes["%gone"] = &watchedPane{
		current: PaneState{PaneInfo: tmux.PaneInfo{ID: "%gone"}, Hash: "private"},
	}
	watcher.windowActivity["@gone"] = 42
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return nil, nil },
		discover: func([]string) ([]DiscoveredPane, error) { return nil, nil },
		focused:  func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture:  func(string, int) (string, error) { return "", nil },
		set:      func(string, PaneState, PaneState) error { return nil },
		clear:    func(string) error { return nil },
		reconcile: func(states []PaneState) error {
			if len(states) != 0 {
				t.Fatalf("reconcile states = %#v, want empty roll call", states)
			}
			return nil
		},
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatal(err)
	}
	if len(watcher.panes) != 0 || len(watcher.windowActivity) != 0 {
		t.Fatalf("private drift after reconcile: panes=%#v windows=%#v",
			watcher.panes, watcher.windowActivity)
	}
}

func TestDaemonForegroundLogsLifecycleWithPID(t *testing.T) {
	dir := t.TempDir()
	manager := &DaemonManager{
		Paths: DaemonPaths{
			Dir: dir, PIDFile: filepath.Join(dir, "watch.pid"), LogFile: filepath.Join(dir, "watch.log"),
		},
		backend:      &fakeDaemonBackend{currentPID: 77, alive: make(map[int]bool)},
		startTimeout: time.Second,
		pollInterval: time.Millisecond,
	}
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot:  func() ([]PaneState, error) { return nil, nil },
		discover:  func([]string) ([]DiscoveredPane, error) { return nil, nil },
		focused:   func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture:   func(string, int) (string, error) { return "", nil },
		set:       func(string, PaneState, PaneState) error { return nil },
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := manager.RunForeground(ctx, WatchOptions{
		Poll: time.Second, GracePeriods: 1, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatalf("RunForeground() error = %v", err)
	}
	logData, err := os.ReadFile(manager.Paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(logData)
	if !strings.Contains(logged, "watcher starting pid=77") ||
		!strings.Contains(logged, "watcher stopped pid=77") {
		t.Fatalf("lifecycle log = %q", logged)
	}
}

func TestReapPanesRechecksFocusKillsUnfocusedPanesAndReconcilesAggregates(t *testing.T) {
	originalFocused, originalKill := watchFocused, watchKillPane
	originalReconcile := watchReapReconcile
	t.Cleanup(func() {
		watchFocused, watchKillPane = originalFocused, originalKill
		watchReapReconcile = originalReconcile
	})

	var killed []string
	reconciled := 0
	watchFocused = func() (map[string]bool, error) { return map[string]bool{"%1": true}, nil }
	watchKillPane = func(target string) error {
		killed = append(killed, target)
		return nil
	}
	watchReapReconcile = func() error {
		reconciled++
		return nil
	}
	candidates := []ReapCandidate{
		{PaneState: PaneState{PaneInfo: tmux.PaneInfo{ID: "%1"}}},
		{PaneState: PaneState{PaneInfo: tmux.PaneInfo{ID: "%2"}}},
	}

	report, err := ReapPanes(candidates)
	if err != nil {
		t.Fatalf("ReapPanes() error = %v", err)
	}
	if !slices.Equal(killed, []string{"%2"}) {
		t.Fatalf("killed = %#v, want only unfocused pane", killed)
	}
	if len(report.Removed) != 1 || report.Removed[0].ID != "%2" ||
		len(report.Protected) != 1 || report.Protected[0].ID != "%1" ||
		len(report.Failed) != 0 || reconciled != 1 {
		t.Fatalf("report = %#v reconciled=%d, want focused protection and one removal", report, reconciled)
	}
}

func TestReapPanesKillFailureDoesNotStopRemainingKills(t *testing.T) {
	originalFocused, originalKill := watchFocused, watchKillPane
	originalReconcile := watchReapReconcile
	t.Cleanup(func() {
		watchFocused, watchKillPane = originalFocused, originalKill
		watchReapReconcile = originalReconcile
	})

	var killed []string
	reconciled := 0
	watchFocused = func() (map[string]bool, error) { return map[string]bool{}, nil }
	watchKillPane = func(target string) error {
		killed = append(killed, target)
		if target == "%1" {
			return errors.New("pane survived")
		}
		return nil
	}
	watchReapReconcile = func() error {
		reconciled++
		return nil
	}
	candidates := []ReapCandidate{
		{PaneState: PaneState{PaneInfo: tmux.PaneInfo{ID: "%1"}}},
		{PaneState: PaneState{PaneInfo: tmux.PaneInfo{ID: "%2"}}},
	}

	report, err := ReapPanes(candidates)
	if err == nil || !strings.Contains(err.Error(), "pane survived") {
		t.Fatalf("ReapPanes() error = %v, want kill failure", err)
	}
	if !slices.Equal(killed, []string{"%1", "%2"}) {
		t.Fatalf("kill attempts = %#v, want both confirmed panes", killed)
	}
	if len(report.Removed) != 1 || report.Removed[0].ID != "%2" ||
		len(report.Failed) != 1 || report.Failed[0].Candidate.ID != "%1" ||
		len(report.Protected) != 0 || reconciled != 1 {
		t.Fatalf("report = %#v reconciled=%d, want one failure and one removal", report, reconciled)
	}
}

func TestWatcherTickClearsExitedAgentsReadsFocusedPaneAndInitializesNewPane(t *testing.T) {
	options := WatchOptions{
		Poll:         time.Second,
		GracePeriods: 3,
		Period:       30 * time.Second,
		CaptureLines: 30,
		Agents:       []string{"claude"},
	}
	watcher, err := NewWatcher(options)
	if err != nil {
		t.Fatal(err)
	}

	states := []PaneState{
		{
			PaneInfo: tmux.PaneInfo{ID: "%old", Command: "zsh"},
			Watch:    true, WatchSet: true, State: StateUnread, Since: 10,
			Changed: 5, Hash: "old", Proc: "claude", Fired: true,
		},
		{
			PaneInfo: tmux.PaneInfo{ID: "%focus", Command: "claude"},
			Watch:    true, WatchSet: true, State: StateUnread, Since: 20,
			Changed: 15, Hash: screenHash("same", options.CaptureLines), Proc: "101:claude", Fired: true,
		},
	}
	discovered := []DiscoveredPane{
		{PaneInfo: tmux.PaneInfo{ID: "%focus", Command: "claude"}, ProcessFingerprint: "101:claude"},
		{PaneInfo: tmux.PaneInfo{ID: "%new", Command: "claude"}, ProcessFingerprint: "102:claude"},
	}

	var cleared []string
	written := make(map[string]PaneState)
	reconciled := 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return states, nil },
		discover: func([]string) ([]DiscoveredPane, error) { return discovered, nil },
		focused:  func() (map[string]bool, error) { return map[string]bool{"%focus": true}, nil },
		capture: func(target string, _ int) (string, error) {
			if target == "%focus" {
				return "same", nil
			}
			return "new screen", nil
		},
		set: func(target string, _ PaneState, state PaneState) error {
			written[target] = state
			return nil
		},
		clear: func(target string) error {
			cleared = append(cleared, target)
			return nil
		},
		reconcile: func([]PaneState) error {
			reconciled++
			return nil
		},
	})

	if err := watcher.Tick(unixTime(100)); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if !slices.Equal(cleared, []string{"%old"}) {
		t.Fatalf("cleared panes = %#v, want %%old", cleared)
	}
	if got := written["%focus"]; got.State != StateQuiet {
		t.Fatalf("focused pane state = %#v, want quiet", got)
	}
	if got := written["%new"]; got.State != StateActive || !got.Watch ||
		got.Hash != "" || got.Fired {
		t.Fatalf("new pane state = %#v, want watched active", got)
	}
	if reconciled != 1 {
		t.Fatalf("reconcile calls = %d, want 1", reconciled)
	}
}

func TestWatcherTickSkipsPaneThatVanishesDuringCapture(t *testing.T) {
	watcher, err := NewWatcher(WatchOptions{
		Poll: time.Second, GracePeriods: 1, Period: time.Second,
		CaptureLines: 30, Agents: []string{"claude"},
	})
	if err != nil {
		t.Fatal(err)
	}

	setCalls := 0
	stubWatchBoundary(t, watchBoundaryStub{
		snapshot: func() ([]PaneState, error) { return nil, nil },
		discover: func([]string) ([]DiscoveredPane, error) {
			return []DiscoveredPane{{
				PaneInfo: tmux.PaneInfo{ID: "%gone", Command: "claude"}, ProcessFingerprint: "101:claude",
			}}, nil
		},
		focused:   func() (map[string]bool, error) { return map[string]bool{}, nil },
		capture:   func(string, int) (string, error) { return "", errors.New("pane vanished") },
		set:       func(string, PaneState, PaneState) error { setCalls++; return nil },
		clear:     func(string) error { return nil },
		reconcile: func([]PaneState) error { return nil },
	})

	if err := watcher.Tick(time.Now()); err != nil {
		t.Fatalf("Tick() pane churn error = %v, want nil", err)
	}
	if setCalls != 0 {
		t.Fatalf("Set calls = %d, want 0", setCalls)
	}
}

func TestDaemonStartNoopStaleReplacementStopAndStatus(t *testing.T) {
	dir := t.TempDir()
	backend := &fakeDaemonBackend{
		executable: "/opt/bin/tmx",
		pids:       []int{41, 42},
		alive:      make(map[int]bool),
	}
	manager := &DaemonManager{
		Paths: DaemonPaths{
			Dir: dir, PIDFile: filepath.Join(dir, "watch.pid"), LogFile: filepath.Join(dir, "watch.log"),
		},
		backend: backend, stopTimeout: 10 * time.Millisecond, pollInterval: time.Millisecond,
	}
	backend.pidFile = manager.Paths.PIDFile

	status, started, err := manager.Start()
	if err != nil || !started || status.PID != 41 {
		t.Fatalf("first Start() = (%+v, %v, %v), want started pid 41", status, started, err)
	}
	if backend.executableSeen != "/opt/bin/tmx" || !slices.Equal(backend.argsSeen, []string{"watch", "run"}) {
		t.Fatalf("spawn = %q %#v, want current executable watch run", backend.executableSeen, backend.argsSeen)
	}
	if backend.logSeen != manager.Paths.LogFile {
		t.Fatalf("spawn log = %q, want %q", backend.logSeen, manager.Paths.LogFile)
	}

	status, started, err = manager.Start()
	if err != nil || started || status.PID != 41 || backend.spawnCalls != 1 {
		t.Fatalf("second Start() = (%+v, %v, %v), spawn calls %d; want no-op", status, started, err, backend.spawnCalls)
	}

	backend.release(41)
	status, started, err = manager.Start()
	if err != nil || !started || status.PID != 42 || backend.spawnCalls != 2 {
		t.Fatalf("stale Start() = (%+v, %v, %v), spawn calls %d; want replacement", status, started, err, backend.spawnCalls)
	}

	status, err = manager.Status()
	if err != nil || !status.Running || status.PID != 42 {
		t.Fatalf("Status() = (%+v, %v), want running pid 42", status, err)
	}

	status, stopped, err := manager.Stop()
	if err != nil || !stopped || status.Running {
		t.Fatalf("Stop() = (%+v, %v, %v), want stopped", status, stopped, err)
	}
	if !slices.Equal(backend.signals, []int{42}) {
		t.Fatalf("signals = %#v, want [42]", backend.signals)
	}
	if _, err := os.Stat(manager.Paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile after stop error = %v, want not exist", err)
	}

	status, stopped, err = manager.Stop()
	if err != nil || stopped || status.Running {
		t.Fatalf("second Stop() = (%+v, %v, %v), want no-op", status, stopped, err)
	}
}

func TestDaemonStatusRemovesMalformedAndUnownedPidfiles(t *testing.T) {
	dir := t.TempDir()
	backend := &fakeDaemonBackend{alive: map[int]bool{99: true}}
	manager := &DaemonManager{
		Paths: DaemonPaths{
			Dir: dir, PIDFile: filepath.Join(dir, "watch.pid"), LogFile: filepath.Join(dir, "watch.log"),
		},
		backend: backend,
	}

	if err := os.WriteFile(manager.Paths.PIDFile, []byte("not-a-pid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status()
	if err != nil || status.Running {
		t.Fatalf("malformed Status() = (%+v, %v), want stopped", status, err)
	}
	if _, err := os.Stat(manager.Paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed pidfile was not removed: %v", err)
	}

	if err := writePID(manager.Paths.PIDFile, 99); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil || status.Running {
		t.Fatalf("unowned Status() = (%+v, %v), want stopped", status, err)
	}
	if _, err := os.Stat(manager.Paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unowned pidfile was not removed: %v", err)
	}
	if _, stopped, err := manager.Stop(); err != nil || stopped || len(backend.signals) != 0 {
		t.Fatalf("Stop() on unowned pid = stopped %v, err %v, signals %#v", stopped, err, backend.signals)
	}
}

func TestDaemonStartFailsWhenChildExitsBeforeReady(t *testing.T) {
	dir := t.TempDir()
	backend := &fakeDaemonBackend{
		executable:  "/opt/bin/tmx",
		pids:        []int{55},
		alive:       make(map[int]bool),
		exitOnSpawn: true,
	}
	manager := &DaemonManager{
		Paths: DaemonPaths{
			Dir: dir, PIDFile: filepath.Join(dir, "watch.pid"), LogFile: filepath.Join(dir, "watch.log"),
		},
		backend: backend, startTimeout: 10 * time.Millisecond, pollInterval: time.Millisecond,
	}

	status, started, err := manager.Start()
	if err == nil || started || status.Running {
		t.Fatalf("Start() = (%+v, %v, %v), want readiness failure", status, started, err)
	}
	if !strings.Contains(err.Error(), "exited before becoming ready") {
		t.Fatalf("Start() error = %v, want readiness detail", err)
	}
}

func TestPIDGuardDistinguishesTransientInspectionFromDaemonOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch.pid")
	if err := writePID(path, 99); err != nil {
		t.Fatal(err)
	}
	inspection, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(inspection.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(2 * time.Millisecond)
		_ = syscall.Flock(int(inspection.Fd()), syscall.LOCK_UN)
		_ = inspection.Close()
	}()

	guard, err := acquirePIDGuard(path, 77, 100*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("acquirePIDGuard() after transient inspection error = %v", err)
	}
	if err := guard.MarkReady(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}

	owner, err := acquirePIDGuard(path, 88, 100*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.MarkReady(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()

	if _, err := acquirePIDGuard(path, 89, 20*time.Millisecond, time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "already running (pid 88)") {
		t.Fatalf("second acquirePIDGuard() error = %v, want existing daemon", err)
	}
}

func TestCurrentDaemonPathsArePerSocketUnderStateDir(t *testing.T) {
	t.Setenv("TMX_STATE_DIR", "/state/tmx")
	t.Setenv("TMUX", "/tmp/tmux-501/dev,123,0")

	paths, err := CurrentDaemonPaths()
	if err != nil {
		t.Fatal(err)
	}
	wantDir := "/state/tmx/%2Ftmp%2Ftmux-501%2Fdev"
	if paths.Dir != wantDir {
		t.Fatalf("daemon dir = %q, want %q", paths.Dir, wantDir)
	}
	if paths.PIDFile != filepath.Join(wantDir, "watch.pid") || paths.LogFile != filepath.Join(wantDir, "watch.log") {
		t.Fatalf("daemon paths = %+v", paths)
	}
}

func TestAppendWatcherLogWritesCLIEventToSocketLog(t *testing.T) {
	t.Setenv("TMX_STATE_DIR", t.TempDir())
	t.Setenv("TMUX", "/tmp/tmux-501/dev,123,0")

	if err := AppendWatcherLog("read acknowledged pane=%s baseline=%s", "%1", "abc123"); err != nil {
		t.Fatalf("AppendWatcherLog() error = %v", err)
	}
	paths, err := CurrentDaemonPaths()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(paths.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); !strings.Contains(got, "read acknowledged pane=%1 baseline=abc123") {
		t.Fatalf("watcher log = %q, want CLI event", got)
	}
}

func unixTime(seconds int64) time.Time {
	return time.Unix(seconds, 0)
}

func logForTest(output *bytes.Buffer) *log.Logger {
	return log.New(output, "", 0)
}

func applyPublicForTest(state *PaneState, public PaneState) {
	state.Watch = public.Watch
	state.WatchSet = public.WatchSet
	state.State = public.State
	state.Since = public.Since
	state.Changed = public.Changed
	state.Proc = public.Proc
	state.ReadHash = public.ReadHash
}

func assertWatchState(
	t *testing.T,
	state PaneState,
	changed bool,
	wantState AttentionState,
	wantChanged bool,
	wantFired bool,
) {
	t.Helper()
	if state.State != wantState || changed != wantChanged || state.Fired != wantFired {
		t.Fatalf("state = %s changed=%v fired=%v, want %s changed=%v fired=%v",
			state.State, changed, state.Fired, wantState, wantChanged, wantFired)
	}
}

type watchBoundaryStub struct {
	snapshot  func() ([]PaneState, error)
	discover  func([]string) ([]DiscoveredPane, error)
	capture   func(string, int) (string, error)
	focused   func() (map[string]bool, error)
	set       func(string, PaneState, PaneState) error
	clear     func(string) error
	reconcile func([]PaneState) error
}

func stubWatchBoundary(t *testing.T, stub watchBoundaryStub) {
	t.Helper()
	originalSnapshot, originalDiscover := watchSnapshot, watchDiscover
	originalCapture, originalFocused := watchCapture, watchFocused
	originalSet, originalClear := watchSet, watchClear
	originalReconcile := watchReconcile
	watchSnapshot, watchDiscover = stub.snapshot, stub.discover
	watchCapture, watchFocused = stub.capture, stub.focused
	watchSet, watchClear = stub.set, stub.clear
	watchReconcile = stub.reconcile
	t.Cleanup(func() {
		watchSnapshot, watchDiscover = originalSnapshot, originalDiscover
		watchCapture, watchFocused = originalCapture, originalFocused
		watchSet, watchClear = originalSet, originalClear
		watchReconcile = originalReconcile
	})
}

type fakeDaemonBackend struct {
	executable     string
	executableSeen string
	argsSeen       []string
	logSeen        string
	pids           []int
	alive          map[int]bool
	signals        []int
	spawnCalls     int
	currentPID     int
	pidFile        string
	guards         map[int]*pidGuard
	exitOnSpawn    bool
}

func (f *fakeDaemonBackend) CurrentExecutable() (string, error) {
	return f.executable, nil
}

func (f *fakeDaemonBackend) CurrentPID() int {
	return f.currentPID
}

func (f *fakeDaemonBackend) Spawn(executable string, args []string, logPath string) (int, error) {
	f.executableSeen = executable
	f.argsSeen = append([]string(nil), args...)
	f.logSeen = logPath
	pid := f.pids[f.spawnCalls]
	f.spawnCalls++
	f.alive[pid] = true
	if f.exitOnSpawn {
		f.alive[pid] = false
		return pid, nil
	}
	if f.pidFile != "" {
		guard, err := acquirePIDGuard(f.pidFile, pid, time.Second, time.Millisecond)
		if err != nil {
			return 0, err
		}
		if err := guard.MarkReady(); err != nil {
			_ = guard.Close()
			return 0, err
		}
		if f.guards == nil {
			f.guards = make(map[int]*pidGuard)
		}
		f.guards[pid] = guard
	}
	return pid, nil
}

func (f *fakeDaemonBackend) Alive(pid int) bool {
	return f.alive[pid]
}

func (f *fakeDaemonBackend) Signal(pid int, _ os.Signal) error {
	f.signals = append(f.signals, pid)
	f.release(pid)
	return nil
}

func (f *fakeDaemonBackend) release(pid int) {
	f.alive[pid] = false
	if guard := f.guards[pid]; guard != nil {
		_ = guard.Close()
		delete(f.guards, pid)
	}
}

var _ daemonBackend = (*fakeDaemonBackend)(nil)
