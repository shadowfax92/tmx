package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"tmx/internal/attn"
	"tmx/internal/config"
	"tmx/internal/tmux"
)

func TestWatchStartReportsStartedAndAlreadyRunning(t *testing.T) {
	original := startWatchDaemon
	t.Cleanup(func() { startWatchDaemon = original })

	calls := 0
	startWatchDaemon = func() (attn.DaemonStatus, bool, error) {
		calls++
		return attn.DaemonStatus{Running: true, PID: 77}, calls == 1, nil
	}

	output, err := executeWatchCommand("start")
	if err != nil || output != "watcher started (pid 77)\n" {
		t.Fatalf("first watch start = %q, %v", output, err)
	}
	output, err = executeWatchCommand("start")
	if err != nil || output != "watcher already running (pid 77)\n" {
		t.Fatalf("second watch start = %q, %v", output, err)
	}
}

func TestWatchStopIsNoopWhenNotRunning(t *testing.T) {
	original := stopWatchDaemon
	t.Cleanup(func() { stopWatchDaemon = original })

	stopWatchDaemon = func() (attn.DaemonStatus, bool, error) {
		return attn.DaemonStatus{}, false, nil
	}
	output, err := executeWatchCommand("stop")
	if err != nil || output != "watcher not running\n" {
		t.Fatalf("watch stop = %q, %v", output, err)
	}
}

func TestWatchStatusExitReflectsProcessState(t *testing.T) {
	original := statusWatchDaemon
	t.Cleanup(func() { statusWatchDaemon = original })

	statusWatchDaemon = func() (attn.DaemonStatus, error) {
		return attn.DaemonStatus{Running: true, PID: 88}, nil
	}
	output, err := executeWatchCommand("status")
	if err != nil || output != "watcher running (pid 88)\n" {
		t.Fatalf("running watch status = %q, %v", output, err)
	}

	statusWatchDaemon = func() (attn.DaemonStatus, error) {
		return attn.DaemonStatus{}, nil
	}
	output, err = executeWatchCommand("status")
	if !errors.Is(err, errWatcherStopped) || output != "" {
		t.Fatalf("stopped watch status = %q, %v", output, err)
	}
}

func TestWatchRunLoadsResolvedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	original := runWatchDaemon
	t.Cleanup(func() { runWatchDaemon = original })

	var got attn.WatchOptions
	runWatchDaemon = func(_ context.Context, options attn.WatchOptions) error {
		got = options
		return nil
	}
	if output, err := executeWatchCommand("run"); err != nil || output != "" {
		t.Fatalf("watch run = %q, %v", output, err)
	}
	if got.Poll != config.DefaultWatchPoll ||
		got.GracePeriods != config.DefaultGracePeriods ||
		got.Period != config.DefaultWatchPeriod ||
		got.CaptureLines != config.DefaultCaptureLines ||
		strings.Join(got.Agents, ",") != "claude,codex" {
		t.Fatalf("watch options = %+v, want resolved defaults", got)
	}
}

func TestWatchListShowsEveryTrackedPaneInPriorityOrder(t *testing.T) {
	states := []attn.PaneState{
		inboxState("%active", "work:3.0", attn.StateActive, true, 400, "", "build"),
		inboxState("%new", "work:1.1", attn.StateUnread, true, 200, "new", "review"),
		inboxState("%unwatched", "work:4.0", attn.StateQuiet, false, 50, "done", "done"),
		inboxState("%quiet", "work:2.0", attn.StateQuiet, true, 300, "", "agent"),
		inboxState("%old", "other:7.2", attn.StateUnread, true, 100, "old", "review"),
		{PaneInfo: tmux.PaneInfo{ID: "%untracked", Target: "work:5.0"}, State: attn.StateUnread},
	}
	for i := range states {
		states[i].Proc = "codex"
	}
	stubWatchList(t, states)

	output, err := executeWatchCommand("ls")
	if err != nil {
		t.Fatalf("watch ls error = %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	wantOrder := []string{"other:7.2", "work:1.1", "work:2.0", "work:3.0", "work:4.0"}
	if len(lines) != len(wantOrder) {
		t.Fatalf("watch ls line count = %d, want %d:\n%s", len(lines), len(wantOrder), output)
	}
	for i, target := range wantOrder {
		if !strings.Contains(lines[i], target) {
			t.Fatalf("watch ls line %d = %q, want %q", i, lines[i], target)
		}
	}
	for _, want := range []string{"unread", "15m", "quiet", "active", "unwatched", "codex", "build", "done"} {
		if !strings.Contains(output, want) {
			t.Fatalf("watch ls output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "%untracked") {
		t.Fatalf("watch ls included pane without @attn_watch:\n%s", output)
	}
}

func TestWatchListJSONQuietAndEmptyOutputs(t *testing.T) {
	states := []attn.PaneState{
		inboxState("%quiet", "work:2.0", attn.StateQuiet, true, 300, "", "agent"),
		inboxState("%unread", "work:1.0", attn.StateUnread, true, 100, "review", "agent"),
	}
	states[0].Proc, states[1].Proc = "claude", "codex"
	stubWatchList(t, states)

	output, err := executeWatchCommand("ls", "--json", "-q")
	if err != nil {
		t.Fatalf("watch ls --json error = %v", err)
	}
	var got []watchListJSONEntry
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decoding watch ls JSON: %v\n%s", err, output)
	}
	if len(got) != 2 || got[0].Target != "work:1.0" || got[0].State != "unread" ||
		got[0].Age != "15m" || got[0].AgeSeconds != 900 || got[0].Since != 100 ||
		got[0].Process != "codex" || got[0].Label != "review" {
		t.Fatalf("watch ls JSON = %#v", got)
	}

	output, err = executeWatchCommand("l", "-q")
	if err != nil || output != "work:1.0\nwork:2.0\n" {
		t.Fatalf("watch l -q = %q, %v", output, err)
	}

	stubWatchList(t, nil)
	if output, err = executeWatchCommand("ls"); err != nil || output != emptyWatchListMessage+"\n" {
		t.Fatalf("empty watch ls = %q, %v", output, err)
	}
	if output, err = executeWatchCommand("ls", "--json"); err != nil || output != "[]\n" {
		t.Fatalf("empty watch ls --json = %q, %v", output, err)
	}
	if output, err = executeWatchCommand("ls", "-q"); err != nil || output != "" {
		t.Fatalf("empty watch ls -q = %q, %v", output, err)
	}
}

func stubWatchList(t *testing.T, states []attn.PaneState) {
	t.Helper()
	originalList, originalNow := listWatchPanes, watchListNow
	t.Cleanup(func() {
		listWatchPanes, watchListNow = originalList, originalNow
	})
	listWatchPanes = func() ([]attn.PaneState, error) {
		return append([]attn.PaneState(nil), states...), nil
	}
	watchListNow = func() time.Time { return time.Unix(1000, 0) }
}

func TestWatchReapUsesConfiguredTTLAndFlagOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	originalFind, originalNow := findWatchReap, watchReapNow
	t.Cleanup(func() {
		findWatchReap, watchReapNow = originalFind, originalNow
	})
	watchReapNow = func() time.Time { return time.Unix(100, 0) }

	var gotTTL time.Duration
	findWatchReap = func(ttl time.Duration, _ time.Time) (attn.ReapSelection, error) {
		gotTTL = ttl
		return attn.ReapSelection{}, nil
	}
	if output, err := executeWatchCommand("reap", "--dry-run"); err != nil ||
		output != "No stale agent panes matched.\n" {
		t.Fatalf("default reap = %q, %v", output, err)
	}
	if gotTTL != config.DefaultWatchReapTTL {
		t.Fatalf("default reap TTL = %v, want %v", gotTTL, config.DefaultWatchReapTTL)
	}

	if _, err := executeWatchCommand("reap", "--dry-run", "--ttl", "2h"); err != nil {
		t.Fatalf("override reap error = %v", err)
	}
	if gotTTL != 2*time.Hour {
		t.Fatalf("override reap TTL = %v, want 2h", gotTTL)
	}
}

func TestWatchReapDryRunListsNamesAndAgesWithoutKilling(t *testing.T) {
	originalFind, originalReap := findWatchReap, reapWatchPanes
	t.Cleanup(func() {
		findWatchReap, reapWatchPanes = originalFind, originalReap
	})
	findWatchReap = func(time.Duration, time.Time) (attn.ReapSelection, error) {
		return attn.ReapSelection{
			Candidates: []attn.ReapCandidate{
				watchReapCandidate("%1", "work:1.0", "review", 49*time.Hour),
				watchReapCandidate("%2", "work:2.0", "", 25*time.Hour),
			},
			MissingTimestamps: 1,
		}, nil
	}
	reapCalls := 0
	reapWatchPanes = func([]attn.ReapCandidate) (attn.ReapReport, error) {
		reapCalls++
		return attn.ReapReport{}, nil
	}

	output, err := executeWatchCommand("reap", "--dry-run", "--ttl", "24h")
	if err != nil {
		t.Fatalf("dry-run error = %v", err)
	}
	for _, want := range []string{
		"Would reap 2 stale agent panes:",
		"work:1.0 (review)",
		"unchanged 2d",
		"work:2.0",
		"unchanged 1d",
		"Skipped 1 attention pane without a last-change timestamp.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "[y/N]") || reapCalls != 0 {
		t.Fatalf("dry-run prompted or killed: calls=%d output=%q", reapCalls, output)
	}
}

func TestWatchReapConfirmsOnceThenKillsWholeList(t *testing.T) {
	originalFind, originalReap := findWatchReap, reapWatchPanes
	t.Cleanup(func() {
		findWatchReap, reapWatchPanes = originalFind, originalReap
	})
	candidates := []attn.ReapCandidate{
		watchReapCandidate("%1", "work:1.0", "review", 2*24*time.Hour),
		watchReapCandidate("%2", "work:2.0", "", 25*time.Hour),
	}
	findWatchReap = func(time.Duration, time.Time) (attn.ReapSelection, error) {
		return attn.ReapSelection{Candidates: candidates}, nil
	}
	reapCalls := 0
	reapWatchPanes = func(got []attn.ReapCandidate) (attn.ReapReport, error) {
		reapCalls++
		if len(got) != 2 || got[0].ID != "%1" || got[1].ID != "%2" {
			t.Fatalf("confirmed candidates = %#v", got)
		}
		return attn.ReapReport{Removed: got}, nil
	}

	output, err := executeWatchCommandWithInput("y\n", "reap", "--ttl", "24h")
	if err != nil {
		t.Fatalf("confirmed reap error = %v", err)
	}
	if strings.Count(output, "[y/N]") != 1 || reapCalls != 1 {
		t.Fatalf("confirmation count/calls = %d/%d:\n%s", strings.Count(output, "[y/N]"), reapCalls, output)
	}
	if !strings.Contains(output, "Stale agent panes (2):") ||
		!strings.Contains(output, "Reaped 2 stale agent panes.") {
		t.Fatalf("confirmed reap output:\n%s", output)
	}
}

func TestWatchReapDeclineKillsNothing(t *testing.T) {
	originalFind, originalReap := findWatchReap, reapWatchPanes
	t.Cleanup(func() {
		findWatchReap, reapWatchPanes = originalFind, originalReap
	})
	findWatchReap = func(time.Duration, time.Time) (attn.ReapSelection, error) {
		return attn.ReapSelection{
			Candidates: []attn.ReapCandidate{
				watchReapCandidate("%1", "work:1.0", "", 2*24*time.Hour),
			},
		}, nil
	}
	reapCalls := 0
	reapWatchPanes = func([]attn.ReapCandidate) (attn.ReapReport, error) {
		reapCalls++
		return attn.ReapReport{}, nil
	}

	output, err := executeWatchCommandWithInput("n\n", "reap", "--ttl", "24h")
	if err != nil {
		t.Fatalf("declined reap error = %v", err)
	}
	if reapCalls != 0 || !strings.Contains(output, "No panes reaped.") {
		t.Fatalf("declined reap calls=%d output=%q", reapCalls, output)
	}
}

func watchReapCandidate(id, target, label string, age time.Duration) attn.ReapCandidate {
	return attn.ReapCandidate{
		PaneState: attn.PaneState{
			PaneInfo: tmux.PaneInfo{ID: id, Target: target, Label: label},
		},
		Age: age,
	}
}

func executeWatchCommand(args ...string) (string, error) {
	return executeWatchCommandWithInput("", args...)
}

func executeWatchCommandWithInput(input string, args ...string) (string, error) {
	command := newWatchCommand()
	command.SilenceErrors = true
	command.SilenceUsage = true
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetIn(strings.NewReader(input))
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}
