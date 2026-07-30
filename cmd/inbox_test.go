package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"tmx/internal/attn"
	"tmx/internal/config"
	"tmx/internal/tmux"
)

func TestInboxPlainListsLiveAttentionPanesInPriorityOrder(t *testing.T) {
	states := []attn.PaneState{
		inboxState("%active", "work:3.0", attn.StateActive, true, 400, "", "build"),
		inboxState("%new", "work:1.1", attn.StateUnread, true, 200, "new", "review"),
		inboxState("%unwatched", "work:4.0", attn.StateQuiet, false, 50, "done", "done"),
		inboxState("%quiet", "work:2.0", attn.StateQuiet, true, 300, "", "agent"),
		inboxState("%old", "other:7.2", attn.StateUnread, true, 100, "old", "review"),
		inboxState("%gone", "work:5.0", attn.StateActive, true, 500, "gone", "gone"),
		{
			PaneInfo: tmux.PaneInfo{ID: "%untracked", Target: "work:6.0"},
			State:    attn.StateUnread,
		},
	}
	captures := map[string]string{
		"%old":       "thinking\n\x1b[31mNeed approval\x1b[0m\n\n",
		"%new":       "new result",
		"%quiet":     "quiet line",
		"%active":    "working\tthrough   tests",
		"%unwatched": "finished",
	}
	deps := fakeInboxDeps(states, captures)
	deps.capture = func(target string, history int) (string, error) {
		if history != 0 {
			t.Fatalf("capture history = %d, want visible screen only", history)
		}
		if target == "%gone" {
			return "", errors.New("pane disappeared")
		}
		return captures[target], nil
	}

	output, err := executeInboxCommand(newInboxCommandWithDeps(deps))
	if err != nil {
		t.Fatalf("inbox error = %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("plain line count = %d, want 5:\n%s", len(lines), output)
	}
	wantOrder := []string{"other:7.2", "work:1.1", "work:2.0", "work:3.0", "work:4.0"}
	for i, target := range wantOrder {
		if !strings.Contains(lines[i], target) {
			t.Fatalf("line %d = %q, want target %q; output:\n%s", i, lines[i], target, output)
		}
	}
	for _, want := range []string{
		"unread", "15m", "old", "Need approval",
		"quiet", "agent", "quiet line",
		"active", "build", "working through tests",
		"unwatched", "done", "finished",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("plain output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\x1b[") ||
		strings.Contains(output, "%gone") ||
		strings.Contains(output, "%untracked") {
		t.Fatalf("plain output contains ANSI, vanished, or untracked pane:\n%q", output)
	}
}

func TestInboxJSONAndQuietAreScriptFriendly(t *testing.T) {
	states := []attn.PaneState{
		inboxState("%quiet", "work:2.0", attn.StateQuiet, true, 300, "quiet", "agent"),
		inboxState("%unread", "work:1.0", attn.StateUnread, true, 100, "review", "agent"),
	}

	t.Run("json", func(t *testing.T) {
		deps := fakeInboxDeps(states, map[string]string{
			"%unread": "last unread line",
			"%quiet":  "last quiet line",
		})
		deps.terminal = func(io.Writer) bool { return true }
		deps.pick = func(string, []string, []string) (string, error) {
			t.Fatal("JSON output opened fzf")
			return "", nil
		}

		output, err := executeInboxCommand(newInboxCommandWithDeps(deps), "--json")
		if err != nil {
			t.Fatalf("inbox --json error = %v", err)
		}
		var got []inboxJSONEntry
		if err := json.Unmarshal([]byte(output), &got); err != nil {
			t.Fatalf("decoding JSON: %v\n%s", err, output)
		}
		if len(got) != 2 || got[0].Target != "work:1.0" || got[1].Target != "work:2.0" {
			t.Fatalf("JSON targets/order = %#v", got)
		}
		if got[0].State != "unread" ||
			got[0].Age != "15m" ||
			got[0].AgeSeconds != 900 ||
			got[0].Since != 100 ||
			got[0].PaneID != "%unread" ||
			got[0].Label != "review" ||
			got[0].LastLine != "last unread line" {
			t.Fatalf("first JSON row = %#v", got[0])
		}
	})

	t.Run("quiet", func(t *testing.T) {
		captureCalls := 0
		deps := fakeInboxDeps(states, nil)
		deps.terminal = func(io.Writer) bool { return true }
		deps.capture = func(string, int) (string, error) {
			captureCalls++
			return "", nil
		}
		deps.pick = func(string, []string, []string) (string, error) {
			t.Fatal("quiet output opened fzf")
			return "", nil
		}

		output, err := executeInboxCommand(newInboxCommandWithDeps(deps), "-q")
		if err != nil {
			t.Fatalf("inbox -q error = %v", err)
		}
		if output != "work:1.0\nwork:2.0\n" {
			t.Fatalf("quiet output = %q, want bare sorted targets", output)
		}
		if captureCalls != 0 {
			t.Fatalf("quiet output captured %d panes, want none", captureCalls)
		}
	})
}

func TestInboxTTYUsesPanePreviewAndSharedJumpPath(t *testing.T) {
	state := inboxState("%unread", "other:7.2", attn.StateUnread, true, 100, "review", "agent")
	backend := newFakeJumpBackend("%unread")
	var cleared []string
	deps := fakeInboxDeps([]attn.PaneState{state}, map[string]string{"%unread": "waiting"})
	deps.terminal = func(io.Writer) bool { return true }
	deps.loadJump = func() (config.JumpAction, string, error) {
		return config.JumpActionZoom, "", nil
	}
	deps.pick = func(prompt string, lines, args []string) (string, error) {
		if prompt != "inbox > " {
			t.Fatalf("fzf prompt = %q", prompt)
		}
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "%unread\t") ||
			!strings.Contains(lines[0], "other:7.2") ||
			!strings.Contains(lines[0], "waiting") {
			t.Fatalf("fzf lines = %#v", lines)
		}
		if !slices.Equal(args, paneFzfArgs()) {
			t.Fatalf("fzf args = %#v, want pane picker args %#v", args, paneFzfArgs())
		}
		if !containsArgPair(args, "--preview", "tmux capture-pane -ep -t {1}") ||
			!containsArgPair(args, "--preview-window", "right:50%") {
			t.Fatalf("fzf args missing capture-pane preview: %#v", args)
		}
		return "%unread", nil
	}
	deps.jump = jumpDeps{
		backend:  backend,
		snapshot: func() ([]attn.PaneState, error) { return nil, nil },
		markRead: func(state attn.PaneState) error {
			cleared = append(cleared, state.ID)
			return nil
		},
	}

	output, err := executeInboxCommand(newInboxCommandWithDeps(deps))
	if err != nil {
		t.Fatalf("TTY inbox error = %v", err)
	}
	if output != "" {
		t.Fatalf("TTY inbox output = %q, want fzf-only UI", output)
	}
	wantCalls := []string{
		"switch client-1 other",
		"window =other:7",
		"pane %unread",
		"zoomed? %unread",
		"zoom %unread",
	}
	if !slices.Equal(backend.calls, wantCalls) {
		t.Fatalf("jump calls = %#v, want %#v", backend.calls, wantCalls)
	}
	if !slices.Equal(cleared, []string{"%unread"}) {
		t.Fatalf("cleared panes = %#v, want selected unread pane", cleared)
	}
}

func TestInboxPlainFlagBypassesFzfOnTTY(t *testing.T) {
	state := inboxState("%1", "work:1.0", attn.StateActive, true, 900, "build", "agent")
	deps := fakeInboxDeps([]attn.PaneState{state}, map[string]string{"%1": "running"})
	deps.terminal = func(io.Writer) bool { return true }
	deps.loadJump = func() (config.JumpAction, string, error) {
		t.Fatal("--plain loaded jump config")
		return "", "", nil
	}
	deps.pick = func(string, []string, []string) (string, error) {
		t.Fatal("--plain opened fzf")
		return "", nil
	}

	output, err := executeInboxCommand(newInboxCommandWithDeps(deps), "--plain")
	if err != nil {
		t.Fatalf("inbox --plain error = %v", err)
	}
	if !strings.Contains(output, "active") ||
		!strings.Contains(output, "work:1.0") ||
		!strings.Contains(output, "running") {
		t.Fatalf("plain output = %q", output)
	}
}

func TestInboxEmptyWatchSetIsFriendlyAndSuccessful(t *testing.T) {
	deps := fakeInboxDeps(nil, nil)
	deps.terminal = func(io.Writer) bool { return true }
	deps.loadJump = func() (config.JumpAction, string, error) {
		t.Fatal("empty inbox loaded jump config")
		return "", "", nil
	}
	deps.pick = func(string, []string, []string) (string, error) {
		t.Fatal("empty inbox opened fzf")
		return "", nil
	}

	output, err := executeInboxCommand(newInboxCommandWithDeps(deps))
	if err != nil {
		t.Fatalf("empty inbox error = %v", err)
	}
	if output != emptyInboxMessage+"\n" {
		t.Fatalf("empty inbox output = %q", output)
	}
}

func fakeInboxDeps(states []attn.PaneState, captures map[string]string) inboxDeps {
	return inboxDeps{
		snapshot: func() ([]attn.PaneState, error) {
			return append([]attn.PaneState(nil), states...), nil
		},
		capture: func(target string, _ int) (string, error) {
			return captures[target], nil
		},
		pick: func(string, []string, []string) (string, error) {
			return "", errors.New("unexpected fzf")
		},
		terminal: func(io.Writer) bool { return false },
		loadJump: func() (config.JumpAction, string, error) {
			return config.JumpActionSelect, "", nil
		},
		jump: jumpDeps{
			backend:  newFakeJumpBackend(),
			snapshot: func() ([]attn.PaneState, error) { return nil, nil },
			markRead: func(attn.PaneState) error { return nil },
		},
		now: func() time.Time { return time.Unix(1000, 0) },
	}
}

func inboxState(
	id string,
	target string,
	state attn.AttentionState,
	watch bool,
	since int64,
	label string,
	windowName string,
) attn.PaneState {
	session := strings.SplitN(target, ":", 2)[0]
	window := 0
	if target == "other:7.2" {
		window = 7
	}
	return attn.PaneState{
		PaneInfo: tmux.PaneInfo{
			ID:          id,
			Target:      target,
			Session:     session,
			WindowIndex: window,
			Label:       label,
			WindowName:  windowName,
		},
		Watch:    watch,
		WatchSet: true,
		State:    state,
		Since:    since,
	}
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func executeInboxCommand(command interface {
	SetOut(io.Writer)
	SetErr(io.Writer)
	SetArgs([]string)
	Execute() error
}, args ...string) (string, error) {
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}
