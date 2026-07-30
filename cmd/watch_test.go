package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"tmx/internal/attn"
	"tmx/internal/config"
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

func executeWatchCommand(args ...string) (string, error) {
	command := newWatchCommand()
	command.SilenceErrors = true
	command.SilenceUsage = true
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}
