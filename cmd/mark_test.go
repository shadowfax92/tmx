package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"tmx/internal/attn"
	"tmx/internal/tmux"

	"github.com/spf13/cobra"
)

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
	if got.state.Since != 500 {
		t.Fatalf("mark read since = %d, want 500", got.state.Since)
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
			got.state.Since != 500 || !got.state.Watch || !got.state.Fired {
			t.Fatalf("conditional read write = %#v, want watched quiet pane", got)
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
		})
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
}

func newFakeAttentionCommands(t *testing.T) *fakeAttentionCommands {
	t.Helper()
	fake := &fakeAttentionCommands{
		states:   make(map[string]attn.PaneState),
		resolved: make(map[string]string),
		inside:   true,
	}

	originalGet, originalSet := getAttentionState, setAttentionState
	originalUpdateIfUnread := updateAttentionIfUnread
	originalDiscover, originalCurrent := discoverAttentionPanes, currentAttentionPane
	originalInside, originalNow := insideTmux, attentionNow
	originalLoadAgents := loadAttentionAgents
	t.Cleanup(func() {
		getAttentionState, setAttentionState = originalGet, originalSet
		updateAttentionIfUnread = originalUpdateIfUnread
		discoverAttentionPanes, currentAttentionPane = originalDiscover, originalCurrent
		insideTmux, attentionNow = originalInside, originalNow
		loadAttentionAgents = originalLoadAgents
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
		return setAttentionState(target, next)
	}
	discoverAttentionPanes = func([]string) ([]attn.DiscoveredPane, error) {
		return fake.discovered, nil
	}
	currentAttentionPane = func() (string, error) { return "%current", nil }
	insideTmux = func() bool { return fake.inside }
	attentionNow = func() time.Time { return time.Unix(500, 0) }
	loadAttentionAgents = func() ([]string, error) { return []string{"claude", "codex"}, nil }
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
