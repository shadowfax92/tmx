package attn

import (
	"slices"
	"testing"

	"tmx/internal/tmux"
)

func TestDiscoverFindsAgentProcessTreesAcrossSessions(t *testing.T) {
	listCalls := 0
	stubDiscovery(t, []tmux.PaneInfo{
		{ID: "%1", Session: "work", PID: 100, Command: "zsh"},
		{ID: "%2", Session: "work", PID: 200, Command: "zsh"},
		{ID: "%3", Session: "other", PID: 300, Command: "codex"},
	}, snapshot(
		process(100, 1, "/bin/zsh"),
		process(101, 100, "/opt/bin/claude"),
		process(200, 1, "/bin/zsh"),
		process(201, 200, "nvim"),
		process(300, 1, "codex"),
	))
	originalList := listPaneInfo
	listPaneInfo = func() ([]tmux.PaneInfo, error) {
		listCalls++
		return originalList()
	}

	got, err := Discover([]string{"claude", "codex"})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if ids := paneIDs(got); !slices.Equal(ids, []string{"%1", "%3"}) {
		t.Fatalf("Discover() pane IDs = %#v, want %%1 and %%3", ids)
	}
	if listCalls != 1 {
		t.Fatalf("list-panes calls = %d, want 1", listCalls)
	}
}

func TestDiscoverPersistsMatchingAgentProcessIdentity(t *testing.T) {
	stubDiscovery(t, []tmux.PaneInfo{
		{ID: "%1", Session: "work", PID: 100, Command: "claude"},
	}, snapshot(
		process(100, 1, "/bin/zsh"),
		process(101, 100, "/opt/bin/claude"),
	))

	got, err := DiscoverWithFingerprints([]string{"claude"})
	if err != nil {
		t.Fatalf("DiscoverWithFingerprints() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != "%1" || got[0].ProcessFingerprint != "101:claude" {
		t.Fatalf("discovered panes = %#v, want %%1 fingerprint 101:claude", got)
	}
}

func TestDiscoverExcludesScratchAndFIPBufferPanes(t *testing.T) {
	stubDiscovery(t, []tmux.PaneInfo{
		{ID: "%1", Session: "gs/sh/1", PID: 100},
		{ID: "%2", Session: "work", PID: 200, FIPBuffer: true},
		{ID: "%3", Session: "work", PID: 300},
	}, snapshot(
		process(100, 1, "claude"),
		process(200, 1, "claude"),
		process(300, 1, "claude"),
	))

	got, err := Discover([]string{"claude"})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if ids := paneIDs(got); !slices.Equal(ids, []string{"%3"}) {
		t.Fatalf("Discover() pane IDs = %#v, want only %%3", ids)
	}
}

func TestDiscoverSkipsPaneThatDisappearedBeforeInspection(t *testing.T) {
	stubDiscovery(t, []tmux.PaneInfo{
		{ID: "%gone", Session: "work", PID: 100},
		{ID: "%live", Session: "work", PID: 200},
	}, snapshot(
		process(200, 1, "zsh"),
		process(201, 200, "codex"),
	))

	got, err := Discover([]string{"codex"})
	if err != nil {
		t.Fatalf("Discover() error = %v, want vanished pane skipped", err)
	}
	if ids := paneIDs(got); !slices.Equal(ids, []string{"%live"}) {
		t.Fatalf("Discover() pane IDs = %#v, want only %%live", ids)
	}
}

type processFixture struct {
	pid     int
	parent  int
	command string
}

func process(pid, parent int, command string) processFixture {
	return processFixture{pid: pid, parent: parent, command: command}
}

func snapshot(processes ...processFixture) processSnapshot {
	result := processSnapshot{
		byPID:    make(map[int]processInfo, len(processes)),
		children: make(map[int][]int),
	}
	for _, process := range processes {
		result.byPID[process.pid] = processInfo{
			command: process.command,
		}
		result.children[process.parent] = append(result.children[process.parent], process.pid)
	}
	return result
}

func stubDiscovery(t *testing.T, panes []tmux.PaneInfo, processes processSnapshot) {
	t.Helper()

	originalList, originalProcesses := listPaneInfo, loadProcesses
	listPaneInfo = func() ([]tmux.PaneInfo, error) { return panes, nil }
	loadProcesses = func() (processSnapshot, error) { return processes, nil }
	t.Cleanup(func() {
		listPaneInfo, loadProcesses = originalList, originalProcesses
	})
}

func paneIDs(panes []tmux.PaneInfo) []string {
	ids := make([]string, 0, len(panes))
	for _, pane := range panes {
		ids = append(ids, pane.ID)
	}
	return ids
}
