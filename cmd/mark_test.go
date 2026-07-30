package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"tmx/internal/attn"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

func TestCaptureStableAttentionBaselineBracketsScreenWithProcessSamples(t *testing.T) {
	for _, test := range []struct {
		name      string
		processes []string
		wantErr   bool
	}{
		{name: "stable", processes: []string{"101:claude", "101:claude"}},
		{name: "rollover", processes: []string{"101:claude", "202:claude"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			originalDiscover, originalCapture := discoverAttentionPanes, captureAttentionScreen
			t.Cleanup(func() {
				discoverAttentionPanes, captureAttentionScreen = originalDiscover, originalCapture
			})

			var events []string
			sample := 0
			discoverAttentionPanes = func([]string) ([]attn.DiscoveredPane, error) {
				process := test.processes[sample]
				sample++
				events = append(events, "process:"+process)
				return []attn.DiscoveredPane{{
					PaneInfo: tmux.PaneInfo{ID: "%1"}, ProcessFingerprint: process,
				}}, nil
			}
			captureAttentionScreen = func(target string, lines int) (string, error) {
				events = append(events, fmt.Sprintf("capture:%s:%d", target, lines))
				return "screen-hash", nil
			}

			baseline, err := captureStableAttentionBaseline("%1", 30, []string{"claude"})
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "process changed") {
					t.Fatalf("captureStableAttentionBaseline() error = %v, want rollover", err)
				}
			} else if err != nil || baseline.Hash != "screen-hash" ||
				baseline.Lines != 30 || baseline.Process != "101:claude" {
				t.Fatalf("captureStableAttentionBaseline() = %#v, %v", baseline, err)
			}
			if got := strings.Join(events, ","); got !=
				"process:"+test.processes[0]+",capture:%1:30,process:"+test.processes[1] {
				t.Fatalf("baseline sampling order = %q", got)
			}
		})
	}
}

func TestCaptureStableAttentionBaselineRejectsMissingAgent(t *testing.T) {
	originalDiscover, originalCapture := discoverAttentionPanes, captureAttentionScreen
	t.Cleanup(func() {
		discoverAttentionPanes, captureAttentionScreen = originalDiscover, originalCapture
	})
	discoverAttentionPanes = func([]string) ([]attn.DiscoveredPane, error) {
		return nil, nil
	}
	captured := false
	captureAttentionScreen = func(string, int) (string, error) {
		captured = true
		return "screen-hash", nil
	}

	_, err := captureStableAttentionBaseline("%1", 30, []string{"claude"})
	if err == nil || !strings.Contains(err.Error(), "not a discovered agent pane") {
		t.Fatalf("captureStableAttentionBaseline() error = %v, want missing agent", err)
	}
	if captured {
		t.Fatal("screen captured without a contemporaneous agent process")
	}
}

func TestMarkDefaultsToCurrentPaneAndReadClearsUnread(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.states["%current"] = attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: "%current"},
		Watch:    true, WatchSet: true, State: attn.StateUnread, Since: 10, Fired: true,
	}

	if _, err := executeAttentionCommand(newMarkCommand(), "read"); err != nil {
		t.Fatalf("mark read error = %v", err)
	}
	if fake.gotTargets[0] != "%current" {
		t.Fatalf("mark read target = %q, want current pane", fake.gotTargets[0])
	}
	got := fake.lastWrite()
	if got.target != "%current" || got.state.State != attn.StateQuiet || !got.state.Watch || !got.state.Fired {
		t.Fatalf("mark read write = %#v, want watched quiet pane", got)
	}
	if got.state.Since != 500 || got.state.ReadHash != "baseline:%current" ||
		got.state.ReadLines != 30 || got.state.Proc != "101:claude" {
		t.Fatalf("mark read state = %#v, want timestamp and captured baseline", got.state)
	}
}

func TestMarkReadIfUnreadClearsOnlyUnreadAndSilencesFailures(t *testing.T) {
	t.Run("unread", func(t *testing.T) {
		fake := newFakeAttentionCommands(t)
		fake.states["%1"] = attn.PaneState{
			PaneInfo: tmux.PaneInfo{ID: "%1"},
			Watch:    true, WatchSet: true, State: attn.StateUnread, Since: 10, Fired: true,
		}

		output, err := executeAttentionCommand(newMarkCommand(), "read", "--if-unread", "-t", "%1")
		if err != nil || output != "" {
			t.Fatalf("mark read --if-unread = output %q, error %v; want silent success", output, err)
		}
		if len(fake.gotTargets) != 0 {
			t.Fatalf("full Get targets = %v, want conditional state helper only", fake.gotTargets)
		}
		if got := fake.conditionalTargets; len(got) != 1 || got[0] != "%1" {
			t.Fatalf("conditional targets = %v, want [%%1]", got)
		}
		got := fake.lastWrite()
		if got.target != "%1" || got.state.State != attn.StateQuiet ||
			got.state.Since != 500 || got.state.ReadHash != "baseline:%1" ||
			got.state.ReadLines != 30 || got.state.Proc != "101:claude" ||
			!got.state.Watch || !got.state.Fired {
			t.Fatalf("conditional read write = %#v, want watched quiet pane", got)
		}
		if len(fake.captureTargets) != 1 || fake.captureTargets[0] != "%1" ||
			!strings.Contains(strings.Join(fake.logs, "\n"), "read acknowledged pane=%1") {
			t.Fatalf("captures=%v logs=%v, want acknowledged baseline", fake.captureTargets, fake.logs)
		}
	})

	for _, test := range []struct {
		name  string
		state attn.AttentionState
		err   error
	}{
		{name: "quiet", state: attn.StateQuiet},
		{name: "untracked"},
		{name: "resolution failure", state: attn.StateUnread, err: errors.New("pane vanished")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeAttentionCommands(t)
			fake.states["%1"] = attn.PaneState{
				PaneInfo: tmux.PaneInfo{ID: "%1"},
				Watch:    test.state != "", WatchSet: test.state != "", State: test.state,
			}
			fake.conditionalErr = test.err

			output, err := executeAttentionCommand(newMarkCommand(), "read", "--if-unread", "-t", "%1")
			if err != nil || output != "" {
				t.Fatalf("mark read --if-unread = output %q, error %v; want silent success", output, err)
			}
			if len(fake.writes) != 0 {
				t.Fatalf("writes = %#v, want none", fake.writes)
			}
			if test.err != nil && !strings.Contains(strings.Join(fake.logs, "\n"), "read failed target=%1") {
				t.Fatalf("logs = %v, want silent hook failure recorded", fake.logs)
			}
		})
	}
}

func TestMarkReadAcknowledgesCurrentAgentProcess(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.states["%1"] = attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: "%1"},
		Watch:    true, WatchSet: true, State: attn.StateUnread,
		Since: 10, Proc: "101:claude", Fired: true,
	}
	fake.baselineProcess = "202:claude"

	if _, err := executeAttentionCommand(newMarkCommand(), "read", "-t", "%1"); err != nil {
		t.Fatalf("mark read error = %v", err)
	}
	got := fake.lastWrite().state
	if got.State != attn.StateQuiet || got.Proc != "202:claude" {
		t.Fatalf("mark read state = %#v, want current process acknowledged", got)
	}
}

func TestMarkReadIfUnreadLogsBaselineCaptureFailureWithoutClearing(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.states["%1"] = attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: "%1"},
		Watch:    true, WatchSet: true, State: attn.StateUnread, Since: 10, Fired: true,
	}
	fake.captureErr = errors.New("capture failed")

	output, err := executeAttentionCommand(newMarkCommand(), "read", "--if-unread", "-t", "%1")
	if err != nil || output != "" {
		t.Fatalf("mark read --if-unread = output %q, error %v; want silent success", output, err)
	}
	if len(fake.writes) != 0 || fake.states["%1"].State != attn.StateUnread {
		t.Fatalf("writes=%#v state=%#v, want unread preserved", fake.writes, fake.states["%1"])
	}
	logs := strings.Join(fake.logs, "\n")
	if !strings.Contains(logs, "read failed target=%1") || !strings.Contains(logs, "capture failed") {
		t.Fatalf("logs = %q, want baseline capture failure", logs)
	}
}

func TestMarkReadLogsAndReturnsSharedReadFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		captureErr error
		setErr     error
	}{
		{name: "capture", captureErr: errors.New("capture failed")},
		{name: "state write", setErr: errors.New("state write failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeAttentionCommands(t)
			fake.states["%1"] = attn.PaneState{
				PaneInfo: tmux.PaneInfo{ID: "%1"},
				Watch:    true, WatchSet: true, State: attn.StateUnread,
			}
			fake.captureErr = test.captureErr
			fake.setErr = test.setErr

			_, err := executeAttentionCommand(newMarkCommand(), "read", "-t", "%1")
			if err == nil {
				t.Fatal("mark read error = nil, want shared read failure")
			}
			if logs := strings.Join(fake.logs, "\n"); !strings.Contains(logs, "read failed pane=%1") {
				t.Fatalf("logs = %q, want shared read failure", logs)
			}
		})
	}
}

func TestMarkReadRejectsEmptyProcessBaseline(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.states["%1"] = attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: "%1"},
		Watch:    true, WatchSet: true, State: attn.StateUnread, Proc: "stale:claude",
	}
	fake.baselineProcess = ""

	_, err := executeAttentionCommand(newMarkCommand(), "read", "-t", "%1")
	if err == nil || !strings.Contains(err.Error(), "missing agent process fingerprint") {
		t.Fatalf("mark read error = %v, want missing process rejection", err)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("writes = %#v, want unread state preserved", fake.writes)
	}
}

func TestMarkTargetAcceptsPaneWindowAndSession(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.resolved = map[string]string{
		"%7":      "%7",
		"work:2":  "%8",
		"tpp/job": "%9",
	}
	fake.discovered = []attn.DiscoveredPane{
		{PaneInfo: tmux.PaneInfo{ID: "%7"}, ProcessFingerprint: "107:claude"},
		{PaneInfo: tmux.PaneInfo{ID: "%8"}, ProcessFingerprint: "108:claude"},
		{PaneInfo: tmux.PaneInfo{ID: "%9"}, ProcessFingerprint: "109:codex"},
	}

	for _, target := range []string{"%7", "work:2", "tpp/job"} {
		if _, err := executeAttentionCommand(newMarkCommand(), "unread", "-t", target); err != nil {
			t.Fatalf("mark unread -t %q error = %v", target, err)
		}
	}
	if got := strings.Join(fake.gotTargets, ","); got != "%7,work:2,tpp/job" {
		t.Fatalf("targets delegated to state layer = %q", got)
	}
	for i, paneID := range []string{"%7", "%8", "%9"} {
		if fake.writes[i].target != paneID {
			t.Fatalf("write %d target = %q, want %q", i, fake.writes[i].target, paneID)
		}
	}
}

func TestSnoozeRefreshesUnreadTimestamp(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.states["%current"] = attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: "%current"},
		Watch:    true, WatchSet: true, State: attn.StateUnread, Since: 100,
		Hash: "same", Proc: "101:claude", Fired: true,
	}
	fake.discovered = []attn.DiscoveredPane{{
		PaneInfo: tmux.PaneInfo{ID: "%current"}, ProcessFingerprint: "101:claude",
	}}

	if _, err := executeAttentionCommand(newSnoozeCommand()); err != nil {
		t.Fatalf("snooze error = %v", err)
	}
	got := fake.lastWrite().state
	if got.State != attn.StateUnread || got.Since != 500 || got.Since <= 100 {
		t.Fatalf("snoozed state = %#v, want unread with fresh timestamp", got)
	}
}

func TestMarkUnreadRearmsOptedOutAgentAndRejectsNonAgent(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.states["agent"] = attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: "%agent"},
		WatchSet: true, State: attn.StateQuiet, Since: 100,
		Hash: "same", Proc: "old:claude", Fired: true,
	}
	fake.resolved["agent"] = "%agent"
	fake.discovered = []attn.DiscoveredPane{{
		PaneInfo: tmux.PaneInfo{ID: "%agent"}, ProcessFingerprint: "101:claude",
	}}

	if _, err := executeAttentionCommand(newMarkCommand(), "unread", "-t", "agent"); err != nil {
		t.Fatalf("mark unread opted-out agent error = %v", err)
	}
	got := fake.lastWrite().state
	if !got.Watch || got.State != attn.StateUnread || got.Since != 500 || !got.Fired ||
		got.Proc != "101:claude" {
		t.Fatalf("re-armed state = %#v", got)
	}

	fake.states["shell"] = attn.PaneState{PaneInfo: tmux.PaneInfo{ID: "%shell"}}
	fake.resolved["shell"] = "%shell"
	writesBefore := len(fake.writes)
	_, err := executeAttentionCommand(newMarkCommand(), "unread", "-t", "shell")
	if err == nil || !strings.Contains(err.Error(), "not an agent pane") ||
		!strings.Contains(err.Error(), "manual watch-add is not supported") {
		t.Fatalf("non-agent error = %v, want clear v1-scope error", err)
	}
	if len(fake.writes) != writesBefore {
		t.Fatal("non-agent mark unread wrote attention state")
	}
}

func TestUnwatchClearsUnreadAggregateAndExplicitUnreadRearms(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.unreadCount = 1
	fake.states["%current"] = attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: "%current"},
		Watch:    true, WatchSet: true, State: attn.StateUnread, Since: 100,
		Hash: "same", Proc: "101:claude", Fired: true,
	}
	fake.discovered = []attn.DiscoveredPane{{
		PaneInfo: tmux.PaneInfo{ID: "%current"}, ProcessFingerprint: "101:claude",
	}}

	if _, err := executeAttentionCommand(newUnwatchCommand()); err != nil {
		t.Fatalf("unwatch error = %v", err)
	}
	got := fake.lastWrite().state
	if got.Watch || got.State != attn.StateQuiet || !got.Fired {
		t.Fatalf("unwatched state = %#v, want unwatched with no unread flag", got)
	}
	if fake.unreadCount != 0 {
		t.Fatalf("window unread aggregate = %d, want 0", fake.unreadCount)
	}

	fake.states["%current"] = got
	if _, err := executeAttentionCommand(newMarkCommand(), "unread"); err != nil {
		t.Fatalf("explicit mark unread after unwatch error = %v", err)
	}
	rearmed := fake.lastWrite().state
	if !rearmed.Watch || rearmed.State != attn.StateUnread {
		t.Fatalf("explicitly re-armed state = %#v", rearmed)
	}
}

func TestAttentionDefaultTargetOutsideTmuxRequiresTarget(t *testing.T) {
	fake := newFakeAttentionCommands(t)
	fake.inside = false

	_, err := executeAttentionCommand(newSnoozeCommand())
	if err == nil || !strings.Contains(err.Error(), "pass -t") {
		t.Fatalf("outside-tmux error = %v, want target guidance", err)
	}
	if len(fake.gotTargets) != 0 {
		t.Fatal("target lookup ran without an explicit or current pane")
	}
}

type attentionWrite struct {
	target string
	state  attn.PaneState
}

type fakeAttentionCommands struct {
	states             map[string]attn.PaneState
	resolved           map[string]string
	discovered         []attn.DiscoveredPane
	gotTargets         []string
	conditionalTargets []string
	writes             []attentionWrite
	unreadCount        int
	inside             bool
	conditionalErr     error
	captureErr         error
	setErr             error
	captureTargets     []string
	logs               []string
	baselineLines      int
	baselineProcess    string
}

func newFakeAttentionCommands(t *testing.T) *fakeAttentionCommands {
	t.Helper()
	fake := &fakeAttentionCommands{
		states:          make(map[string]attn.PaneState),
		resolved:        make(map[string]string),
		inside:          true,
		baselineLines:   30,
		baselineProcess: "101:claude",
	}

	originalGet, originalSet := getAttentionState, setAttentionState
	originalUpdateIfUnread := updateAttentionIfUnread
	originalDiscover, originalCurrent := discoverAttentionPanes, currentAttentionPane
	originalInside, originalNow := insideTmux, attentionNow
	originalLoadAgents := loadAttentionAgents
	originalCaptureBaseline, originalLogEvent := captureAttentionBaseline, logAttentionEvent
	originalCaptureScreen := captureAttentionScreen
	t.Cleanup(func() {
		getAttentionState, setAttentionState = originalGet, originalSet
		updateAttentionIfUnread = originalUpdateIfUnread
		discoverAttentionPanes, currentAttentionPane = originalDiscover, originalCurrent
		insideTmux, attentionNow = originalInside, originalNow
		loadAttentionAgents = originalLoadAgents
		captureAttentionBaseline, logAttentionEvent = originalCaptureBaseline, originalLogEvent
		captureAttentionScreen = originalCaptureScreen
	})

	getAttentionState = func(target string) (attn.PaneState, error) {
		fake.gotTargets = append(fake.gotTargets, target)
		resolved := target
		if paneID, ok := fake.resolved[target]; ok {
			resolved = paneID
		}
		state := fake.states[target]
		if state.ID == "" {
			state = fake.states[resolved]
		}
		state.ID = resolved
		return state, nil
	}
	setAttentionState = func(target string, state attn.PaneState) error {
		if fake.setErr != nil {
			return fake.setErr
		}
		previous := fake.states[target]
		if previous.State == attn.StateUnread && state.State != attn.StateUnread && fake.unreadCount > 0 {
			fake.unreadCount--
		}
		if previous.State != attn.StateUnread && state.State == attn.StateUnread {
			fake.unreadCount++
		}
		fake.states[target] = state
		fake.writes = append(fake.writes, attentionWrite{target: target, state: state})
		return nil
	}
	updateAttentionIfUnread = func(target string, update func(attn.PaneState) attn.PaneState) error {
		fake.conditionalTargets = append(fake.conditionalTargets, target)
		if fake.conditionalErr != nil {
			return fake.conditionalErr
		}
		state := fake.states[target]
		if state.State != attn.StateUnread {
			return nil
		}
		next := update(state)
		if next == state {
			return nil
		}
		return setAttentionState(target, next)
	}
	discoverAttentionPanes = func([]string) ([]attn.DiscoveredPane, error) {
		return fake.discovered, nil
	}
	currentAttentionPane = func() (string, error) { return "%current", nil }
	insideTmux = func() bool { return fake.inside }
	attentionNow = func() time.Time { return time.Unix(500, 0) }
	loadAttentionAgents = func() ([]string, error) { return []string{"claude", "codex"}, nil }
	captureAttentionBaseline = func(target string) (attentionReadBaseline, error) {
		fake.captureTargets = append(fake.captureTargets, target)
		if fake.captureErr != nil {
			return attentionReadBaseline{}, fake.captureErr
		}
		return attentionReadBaseline{
			Hash:    "baseline:" + target,
			Lines:   fake.baselineLines,
			Process: fake.baselineProcess,
		}, nil
	}
	logAttentionEvent = func(format string, args ...any) error {
		fake.logs = append(fake.logs, fmt.Sprintf(format, args...))
		return nil
	}
	return fake
}

func (f *fakeAttentionCommands) lastWrite() attentionWrite {
	if len(f.writes) == 0 {
		return attentionWrite{}
	}
	return f.writes[len(f.writes)-1]
}

func executeAttentionCommand(command *cobra.Command, args ...string) (string, error) {
	command.SilenceErrors = true
	command.SilenceUsage = true
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}
