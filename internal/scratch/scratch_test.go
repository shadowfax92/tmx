package scratch

import (
	"errors"
	"testing"
	"time"

	"tmx/internal/tmux"
)

func fixtureNow() time.Time {
	return time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
}

func TestSelectReapCandidatesDefaultsToOrphans(t *testing.T) {
	restore := stubInventory(t, []scratchFixture{
		{name: "gs/sh/1", parentPane: "%1", parentExists: false, cwd: "/live", lastActiveAt: fixtureNow().Add(-10 * time.Minute)},
		{name: "gs/vim/2", parentPane: "%2", parentExists: true, cwd: "/live", lastActiveAt: fixtureNow().Add(-10 * time.Minute)},
	})
	defer restore()

	got, err := SelectReapCandidates(ReapOptions{})
	if err != nil {
		t.Fatalf("SelectReapCandidates() error = %v", err)
	}
	if len(got) != 1 || got[0].SessionName != "gs/sh/1" || got[0].Reason != ReapOrphan {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestSelectReapCandidatesIncludesIdleSessions(t *testing.T) {
	restore := stubInventory(t, []scratchFixture{
		{name: "gs/sh/1", parentPane: "%1", parentExists: true, cwd: "/live", lastActiveAt: fixtureNow().Add(-2 * time.Hour)},
		{name: "gs/vim/2", parentPane: "%2", parentExists: true, cwd: "/live", lastActiveAt: fixtureNow().Add(-10 * time.Minute)},
	})
	defer restore()

	got, err := SelectReapCandidates(ReapOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("SelectReapCandidates() error = %v", err)
	}
	if len(got) != 1 || got[0].SessionName != "gs/sh/1" || got[0].Reason != ReapIdle {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestSelectReapCandidatesIdleUsesMostRecentOfActiveOrToggled(t *testing.T) {
	// Active long ago, but toggled recently → not idle.
	restore := stubInventory(t, []scratchFixture{
		{
			name: "gs/sh/1", parentPane: "%1", parentExists: true, cwd: "/live",
			lastActiveAt: fixtureNow().Add(-5 * time.Hour),
			metadata:     map[string]string{lastToggledAtKey: fixtureNow().Add(-10 * time.Minute).Format(time.RFC3339)},
		},
	})
	defer restore()

	got, err := SelectReapCandidates(ReapOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("SelectReapCandidates() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no idle candidates (recently toggled), got %#v", got)
	}
}

func TestSelectReapCandidatesIncludesDeadCwd(t *testing.T) {
	restore := stubInventory(t, []scratchFixture{
		{name: "gs/sh/1", parentPane: "%1", parentExists: true, cwd: "/gone", deadCwd: true, lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
		{name: "gs/vim/2", parentPane: "%2", parentExists: true, cwd: "/live", lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
	})
	defer restore()

	got, err := SelectReapCandidates(ReapOptions{})
	if err != nil {
		t.Fatalf("SelectReapCandidates() error = %v", err)
	}
	if len(got) != 1 || got[0].SessionName != "gs/sh/1" || got[0].Reason != ReapDeadCwd {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestSelectReapCandidatesTreatsMissingMetadataAsOrphan(t *testing.T) {
	restore := stubInventory(t, []scratchFixture{
		{name: "gs/sh/1", parentErr: errors.New("missing"), cwd: "/live", lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
	})
	defer restore()

	got, err := SelectReapCandidates(ReapOptions{})
	if err != nil {
		t.Fatalf("SelectReapCandidates() error = %v", err)
	}
	if len(got) != 1 || got[0].Reason != ReapOrphan {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestSelectReapCandidatesAllModeIncludesEverything(t *testing.T) {
	restore := stubInventory(t, []scratchFixture{
		{name: "gs/sh/1", parentPane: "%1", parentExists: true, cwd: "/live", lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
		{name: "gs/vim/2", parentPane: "%2", parentExists: true, cwd: "/live", lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
	})
	defer restore()

	got, err := SelectReapCandidates(ReapOptions{All: true})
	if err != nil {
		t.Fatalf("SelectReapCandidates() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestReapKillsMatchedSessions(t *testing.T) {
	restore := stubInventory(t, []scratchFixture{
		{name: "gs/sh/1", parentPane: "%1", parentExists: false, cwd: "/live", lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
	})
	defer restore()

	var killed []string
	killSession = func(name string) error {
		killed = append(killed, name)
		return nil
	}
	defer func() { killSession = defaultKillSession }()

	report, err := Reap(ReapOptions{})
	if err != nil {
		t.Fatalf("Reap() error = %v", err)
	}
	if len(report.Removed) != 1 || len(killed) != 1 || killed[0] != "gs/sh/1" {
		t.Fatalf("unexpected report: %#v killed=%#v", report, killed)
	}
}

func TestReapReturnsPartialFailure(t *testing.T) {
	restore := stubInventory(t, []scratchFixture{
		{name: "gs/sh/1", parentPane: "%1", parentExists: false, cwd: "/live", lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
		{name: "gs/sh/2", parentPane: "%2", parentExists: false, cwd: "/live", lastActiveAt: fixtureNow().Add(-1 * time.Minute)},
	})
	defer restore()

	killSession = func(name string) error {
		if name == "gs/sh/2" {
			return errors.New("boom")
		}
		return nil
	}
	defer func() { killSession = defaultKillSession }()

	report, err := Reap(ReapOptions{})
	if err == nil {
		t.Fatal("Reap() error = nil, want partial failure")
	}
	if len(report.Removed) != 1 || len(report.Failed) != 1 {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestEnsureStoresOpenedTimestampWhenCreating(t *testing.T) {
	origExists, origNew, origSet, origNow := sessionExists, newSessionWithCommand, setSessionVar, now
	defer func() {
		sessionExists, newSessionWithCommand, setSessionVar, now = origExists, origNew, origSet, origNow
	}()

	sessionExists = func(string) bool { return false }
	var gotEnv []string
	var gotCmd string
	newSessionWithCommand = func(name, startDir string, env []string, command string) error {
		gotEnv, gotCmd = env, command
		return nil
	}
	now = fixtureNow
	values := map[string]string{}
	setSessionVar = func(session, key, value string) error {
		values[key] = value
		return nil
	}

	if err := Ensure("gs/sh/1", "/tmp/project", "sh", "%1", "lazygit"); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got := values[openedAtKey]; got != fixtureNow().Format(time.RFC3339) {
		t.Fatalf("%s = %q, want %q", openedAtKey, got, fixtureNow().Format(time.RFC3339))
	}
	if gotCmd != "lazygit" {
		t.Fatalf("command = %q, want lazygit", gotCmd)
	}
	if len(gotEnv) == 0 || gotEnv[1] != "TMX_SCRATCH=1" {
		t.Fatalf("env = %#v, want TMX_SCRATCH marker", gotEnv)
	}
}

func TestMarkToggledPersistsTimestamp(t *testing.T) {
	origSet, origNow := setSessionVar, now
	defer func() { setSessionVar, now = origSet, origNow }()

	now = fixtureNow
	var gotSession, gotKey, gotValue string
	setSessionVar = func(session, key, value string) error {
		gotSession, gotKey, gotValue = session, key, value
		return nil
	}

	if err := MarkToggled("gs/sh/1"); err != nil {
		t.Fatalf("MarkToggled() error = %v", err)
	}
	if gotSession != "gs/sh/1" || gotKey != lastToggledAtKey || gotValue != fixtureNow().Format(time.RFC3339) {
		t.Fatalf("SetSessionVar(%q,%q,%q) unexpected", gotSession, gotKey, gotValue)
	}
}

type scratchFixture struct {
	name         string
	parentPane   string
	parentErr    error
	parentExists bool
	cwd          string
	deadCwd      bool
	lastActiveAt time.Time
	createdAt    time.Time
	metadata     map[string]string
}

func stubInventory(t *testing.T, fixtures []scratchFixture) func() {
	t.Helper()

	origList, origGet, origPaneExists, origPathExists, origNow := listSessionSnapshotsByPrefix, getSessionVar, paneExists, pathExists, now

	snapshots := make([]tmux.SessionSnapshot, 0, len(fixtures))
	parents := map[string]string{}
	parentErrs := map[string]error{}
	parentExists := map[string]bool{}
	metadata := map[string]map[string]string{}
	cwds := map[string]string{}
	deadCwds := map[string]bool{}

	for _, f := range fixtures {
		created := f.createdAt
		if created.IsZero() {
			created = fixtureNow().Add(-24 * time.Hour)
		}
		active := f.lastActiveAt
		if active.IsZero() {
			active = fixtureNow().Add(-time.Hour)
		}
		snapshots = append(snapshots, tmux.SessionSnapshot{Name: f.name, Created: created, Activity: active})
		parents[f.name] = f.parentPane
		parentErrs[f.name] = f.parentErr
		parentExists[f.parentPane] = f.parentExists
		metadata[f.name] = f.metadata
		cwds[f.name] = f.cwd
		if f.cwd != "" && f.deadCwd {
			deadCwds[f.cwd] = true
		}
	}

	listSessionSnapshotsByPrefix = func(string) ([]tmux.SessionSnapshot, error) { return snapshots, nil }
	getSessionVar = func(session, key string) (string, error) {
		switch key {
		case parentPaneKey:
			return parents[session], parentErrs[session]
		case cwdKey:
			return cwds[session], nil
		default:
			if m := metadata[session]; m != nil {
				return m[key], nil
			}
			return "", nil
		}
	}
	paneExists = func(id string) bool { return parentExists[id] }
	pathExists = func(p string) bool {
		if p == "" {
			return true
		}
		return !deadCwds[p]
	}
	now = fixtureNow

	return func() {
		listSessionSnapshotsByPrefix, getSessionVar, paneExists, pathExists, now = origList, origGet, origPaneExists, origPathExists, origNow
	}
}
