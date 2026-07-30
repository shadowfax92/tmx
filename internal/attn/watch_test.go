package attn

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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

	state, changed = observePane(state, "screen-a", "claude", false, unixTime(500), quietFor)
	assertWatchState(t, state, changed, StateQuiet, false, true)

	state, changed = observePane(state, "screen-b", "claude", false, unixTime(501), quietFor)
	assertWatchState(t, state, changed, StateActive, true, false)

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

	state, changed = observePane(state, "screen", "claude", false, unixTime(400), 90*time.Second)
	assertWatchState(t, state, changed, StateQuiet, false, true)
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

func TestReapPanesClearsStateKillsEveryPaneAndReconcilesAggregates(t *testing.T) {
	originalClear, originalKill := watchClear, watchKillPane
	originalReconcile := watchReconcile
	t.Cleanup(func() {
		watchClear, watchKillPane = originalClear, originalKill
		watchReconcile = originalReconcile
	})

	var cleared, killed []string
	reconciled := 0
	watchClear = func(target string) error {
		cleared = append(cleared, target)
		return nil
	}
	watchKillPane = func(target string) error {
		killed = append(killed, target)
		return nil
	}
	watchReconcile = func() error {
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
	if !slices.Equal(cleared, []string{"%1", "%2"}) {
		t.Fatalf("cleared = %#v, want both panes", cleared)
	}
	if !slices.Equal(killed, []string{"%1", "%2"}) {
		t.Fatalf("killed = %#v, want both panes", killed)
	}
	if len(report.Removed) != 2 || len(report.Failed) != 0 || reconciled != 1 {
		t.Fatalf("report = %#v reconciled=%d, want 2 removed and one reconcile", report, reconciled)
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
		set: func(target string, state PaneState) error {
			written[target] = state
			return nil
		},
		clear: func(target string) error {
			cleared = append(cleared, target)
			return nil
		},
		reconcile: func() error {
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
	if got := written["%focus"]; got.State != StateQuiet || !got.Fired {
		t.Fatalf("focused pane state = %#v, want fired quiet", got)
	}
	if got := written["%new"]; got.State != StateActive || !got.Watch || got.Hash == "" {
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
		set:       func(string, PaneState) error { setCalls++; return nil },
		clear:     func(string) error { return nil },
		reconcile: func() error { return nil },
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

func unixTime(seconds int64) time.Time {
	return time.Unix(seconds, 0)
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
	set       func(string, PaneState) error
	clear     func(string) error
	reconcile func() error
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
