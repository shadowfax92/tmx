package attn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"

	"tmx/internal/tmux"
)

// WatchOptions controls screen stability detection.
type WatchOptions struct {
	Poll         time.Duration
	GracePeriods int
	Period       time.Duration
	CaptureLines int
	Agents       []string
}

func (o WatchOptions) validate() error {
	switch {
	case o.Poll <= 0:
		return errors.New("watch.poll must be greater than zero")
	case o.GracePeriods <= 0:
		return errors.New("watch.grace_periods must be greater than zero")
	case o.Period <= 0:
		return errors.New("watch.period must be greater than zero")
	case o.CaptureLines <= 0:
		return errors.New("watch.capture_lines must be greater than zero")
	default:
		return nil
	}
}

func (o WatchOptions) quietThreshold() time.Duration {
	return time.Duration(o.GracePeriods) * o.Period
}

var (
	watchSnapshot      = Snapshot
	watchDiscover      = DiscoverWithFingerprints
	watchCapture       = tmux.CapturePane
	watchFocused       = tmux.FocusedPaneIDs
	watchSet           = setPaneStateIfCurrent
	watchClear         = Clear
	watchKillPane      = tmux.KillPane
	watchReconcile     = ReconcileWindowUnreadCountsFromSnapshot
	watchReapReconcile = ReconcileWindowUnreadCounts
)

// Watcher ties discovery, screen capture, and the pane state machine together.
// Per-tick episode details stay here rather than in tmux user options.
type Watcher struct {
	options        WatchOptions
	panes          map[string]*watchedPane
	windowActivity map[string]int64
	ticks          uint64
	logger         *log.Logger
}

type watchedPane struct {
	current   PaneState
	published PaneState
	pending   *PaneState
}

// ReapCandidate is a live attention pane whose screen has not changed past
// the reap threshold.
type ReapCandidate struct {
	PaneState
	LastChanged time.Time
	Age         time.Duration
}

func (c ReapCandidate) DisplayName() string {
	name := c.Target
	if name == "" {
		name = c.ID
	}
	if c.Label != "" {
		return fmt.Sprintf("%s (%s)", name, c.Label)
	}
	return name
}

// ReapSelection reports selectable panes and attention panes skipped because
// the watcher has not recorded a last-screen-change timestamp for them.
type ReapSelection struct {
	Candidates        []ReapCandidate
	MissingTimestamps int
}

type ReapFailure struct {
	Candidate ReapCandidate
	Err       error
}

type ReapReport struct {
	Removed   []ReapCandidate
	Protected []ReapCandidate
	Failed    []ReapFailure
}

func NewWatcher(options WatchOptions) (*Watcher, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	return &Watcher{
		options:        options,
		panes:          make(map[string]*watchedPane),
		windowActivity: make(map[string]int64),
		logger:         log.New(io.Discard, "", 0),
	}, nil
}

// FindReapCandidates selects live, non-focused attention panes based only on
// the last screen change recorded by the watcher. Explicitly unwatched panes
// remain eligible; missing timestamps are never guessed by the reap command.
func FindReapCandidates(ttl time.Duration, now time.Time) (ReapSelection, error) {
	if ttl <= 0 {
		return ReapSelection{}, errors.New("reap ttl must be greater than zero")
	}
	states, err := watchSnapshot()
	if err != nil {
		return ReapSelection{}, err
	}
	focused, err := watchFocused()
	if err != nil {
		return ReapSelection{}, err
	}
	return selectReapCandidates(states, focused, ttl, now), nil
}

func selectReapCandidates(
	states []PaneState,
	focused map[string]bool,
	ttl time.Duration,
	now time.Time,
) ReapSelection {
	var selection ReapSelection
	for _, state := range states {
		if focused[state.ID] || !hasAttentionState(state) || state.State == StateActive {
			continue
		}
		if state.Changed <= 0 {
			selection.MissingTimestamps++
			continue
		}
		lastChanged := time.Unix(state.Changed, 0)
		age := now.Sub(lastChanged)
		if age <= ttl {
			continue
		}
		selection.Candidates = append(selection.Candidates, ReapCandidate{
			PaneState:   state,
			LastChanged: lastChanged,
			Age:         age,
		})
	}
	sort.Slice(selection.Candidates, func(i, j int) bool {
		left, right := selection.Candidates[i], selection.Candidates[j]
		if !left.LastChanged.Equal(right.LastChanged) {
			return left.LastChanged.Before(right.LastChanged)
		}
		return left.Target < right.Target
	})
	return selection
}

// ReapPanes protects panes focused since selection, kills the rest, and then
// repairs window aggregates from the surviving pane inventory. Successful
// kill-pane calls remove pane-scoped attention state with the pane itself.
func ReapPanes(candidates []ReapCandidate) (ReapReport, error) {
	var report ReapReport
	var errs []error
	focused, err := watchFocused()
	if err != nil {
		return report, fmt.Errorf("checking focused panes before reap: %w", err)
	}
	for _, candidate := range candidates {
		if focused[candidate.ID] {
			report.Protected = append(report.Protected, candidate)
			continue
		}
		if err := watchKillPane(candidate.ID); err != nil {
			wrapped := fmt.Errorf("killing %s: %w", candidate.ID, err)
			report.Failed = append(report.Failed, ReapFailure{Candidate: candidate, Err: wrapped})
			errs = append(errs, wrapped)
			continue
		}
		report.Removed = append(report.Removed, candidate)
	}
	if err := watchReapReconcile(); err != nil {
		wrapped := fmt.Errorf("reconciling attention counts: %w", err)
		errs = append(errs, wrapped)
	}
	return report, errors.Join(errs...)
}

// Run polls immediately and then at the configured interval until cancelled.
// Transient tick errors are logged and retried.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.Tick(time.Now()); err != nil {
		w.logf("tick error: %v", err)
	}
	return w.runAfterInitialTick(ctx)
}

func (w *Watcher) runAfterInitialTick(ctx context.Context) error {
	ticker := time.NewTicker(w.options.Poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.Tick(time.Now()); err != nil {
				w.logf("tick error: %v", err)
			}
		}
	}
}

// Tick evaluates every currently discovered agent pane once.
func (w *Watcher) Tick(now time.Time) error {
	states, err := watchSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	discovered, err := watchDiscover(w.options.Agents)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	focused, err := watchFocused()
	if err != nil {
		return fmt.Errorf("focused panes: %w", err)
	}

	byID := make(map[string]PaneState, len(states))
	for _, state := range states {
		byID[state.ID] = state
	}
	agents := make(map[string]DiscoveredPane, len(discovered))
	for _, pane := range discovered {
		agents[pane.ID] = pane
	}
	failedActivityWindows := make(map[string]bool)

	// A live pane whose agent exited is no longer part of attention state.
	// If the pane vanished entirely, tmux already removed its pane options and
	// the aggregate reconciliation below repairs its old window count.
	for _, state := range states {
		if _, stillAgent := agents[state.ID]; stillAgent || !hasAttentionState(state) {
			continue
		}
		if err := watchClear(state.ID); err != nil {
			w.logf("clear pane=%s error: %v", state.ID, err)
			continue
		}
		if state.State == StateUnread {
			adjustSnapshotWindowCount(byID, state, -1)
		}
		cleared := byID[state.ID]
		clearPublicState(&cleared)
		byID[state.ID] = cleared
		delete(w.panes, state.ID)
		w.logf("transition pane=%s state=%s->cleared", state.ID, state.State)
	}

	for _, pane := range discovered {
		observed := byID[pane.ID]
		memory, known := w.panes[pane.ID]
		if !known {
			memory = &watchedPane{
				current:   observed,
				published: publicState(observed),
			}
			w.panes[pane.ID] = memory
		} else if !samePublicState(observed, memory.published) {
			reconcilePublishedState(&memory.current, memory.published, observed)
			memory.published = publicState(observed)
			memory.pending = nil
			w.logf("external transition pane=%s state=%s watch=%t", pane.ID, observed.State, observed.Watch)
		}

		memory.current.PaneInfo = pane.PaneInfo
		memory.current.WindowID = observed.WindowID
		memory.current.WindowActivity = observed.WindowActivity
		memory.current.WindowUnreadCount = observed.WindowUnreadCount

		hash := memory.current.Hash
		windowKey := activityWindowKey(observed)
		previousActivity, sawWindow := w.windowActivity[windowKey]
		idleWindow := sawWindow && previousActivity == observed.WindowActivity
		firstCapture := !known || hash == ""
		if memory.pending != nil {
			desired := *memory.pending
			if err := watchSet(pane.ID, memory.published, desired); err != nil {
				if errors.Is(err, errPaneStateDrift) {
					w.logf("transition deferred pane=%s: public state changed during tick", pane.ID)
				} else {
					w.logf("transition pane=%s error: %v", pane.ID, err)
				}
				failedActivityWindows[windowKey] = true
				continue
			}
			w.recordPublishedTransition(byID, pane.ID, observed, memory, desired)
			memory.pending = nil
			// This tick was spent committing the pending state, so do not
			// accept a newer activity timestamp that was not captured.
			failedActivityWindows[windowKey] = true
			continue
		}
		if firstCapture || !idleWindow {
			captured, err := watchCapture(pane.ID, w.options.CaptureLines)
			if err != nil {
				// The pane may have disappeared between discovery and capture.
				w.logf("capture pane=%s error: %v", pane.ID, err)
				failedActivityWindows[windowKey] = true
				continue
			}
			hash = screenHash(captured, w.options.CaptureLines)
			if !known && observed.WatchSet && !observed.Watch {
				// Preserve an explicit opt-out across a daemon restart while
				// still learning the first private screen hash.
				memory.current.Hash = hash
				memory.current.Changed = now.Unix()
				memory.current.Fired = true
			}
		}

		previous := memory.current
		next, _ := observePane(
			previous,
			hash,
			pane.ProcessFingerprint,
			focused[pane.ID],
			now,
			w.options.quietThreshold(),
		)
		memory.current = next

		desired := desiredPublicState(next, memory.published)
		if samePublicState(desired, memory.published) {
			continue
		}
		if err := watchSet(pane.ID, memory.published, desired); err != nil {
			pending := desired
			memory.pending = &pending
			if errors.Is(err, errPaneStateDrift) {
				w.logf("transition deferred pane=%s: public state changed during tick", pane.ID)
			} else {
				w.logf("transition pane=%s error: %v", pane.ID, err)
			}
			continue
		}
		w.recordPublishedTransition(byID, pane.ID, observed, memory, desired)
	}

	for _, state := range states {
		windowKey := activityWindowKey(state)
		if !failedActivityWindows[windowKey] {
			w.windowActivity[windowKey] = state.WindowActivity
		}
	}
	w.ticks++
	if w.ticks == 1 || w.ticks%10 == 0 {
		reconcileStates := make([]PaneState, 0, len(byID))
		for id, state := range byID {
			reconcileStates = append(reconcileStates, state)
			if _, stillAgent := agents[id]; !hasAttentionState(state) && !stillAgent {
				delete(w.panes, id)
			}
		}
		for id := range w.panes {
			if _, live := byID[id]; !live {
				delete(w.panes, id)
			}
		}
		liveWindows := make(map[string]bool)
		for _, state := range states {
			liveWindows[activityWindowKey(state)] = true
		}
		for window := range w.windowActivity {
			if !liveWindows[window] {
				delete(w.windowActivity, window)
			}
		}
		if err := watchReconcile(reconcileStates); err != nil {
			w.ticks--
			return fmt.Errorf("reconcile unread counts: %w", err)
		}
	}
	return nil
}

func hasAttentionState(state PaneState) bool {
	return state.WatchSet || state.State != "" || state.Since != 0 ||
		state.Changed != 0 || state.Proc != ""
}

func publicState(state PaneState) PaneState {
	return PaneState{
		Watch:    state.Watch,
		WatchSet: state.WatchSet,
		State:    state.State,
		Since:    state.Since,
		Changed:  state.Changed,
		Proc:     state.Proc,
	}
}

func desiredPublicState(current, published PaneState) PaneState {
	desired := publicState(current)
	desired.WatchSet = true

	// Active-screen timestamps keep moving in memory. Publish the episode
	// start only when entering active, and publish last-changed only when the
	// pane settles from active to quiet.
	if current.State == StateActive {
		if published.State == StateActive && current.Proc == published.Proc {
			desired.Since = published.Since
		}
		desired.Changed = published.Changed
	} else if !(current.State == StateQuiet && published.State == StateActive) {
		desired.Changed = published.Changed
	}
	return desired
}

func reconcilePublishedState(current *PaneState, published, observed PaneState) {
	externalEpisodeChange := published.Watch != observed.Watch ||
		published.WatchSet != observed.WatchSet ||
		published.State != observed.State ||
		published.Since != observed.Since
	if published.Watch != observed.Watch || published.WatchSet != observed.WatchSet {
		current.Watch = observed.Watch
		current.WatchSet = observed.WatchSet
	}
	if published.State != observed.State {
		current.State = observed.State
	}
	if published.Since != observed.Since {
		current.Since = observed.Since
	}
	if published.Changed != observed.Changed {
		current.Changed = observed.Changed
	}
	if published.Proc != observed.Proc {
		current.Proc = observed.Proc
	}
	if externalEpisodeChange {
		current.Fired = observed.State != StateActive
	}
}

func updateSnapshotPublicState(states map[string]PaneState, paneID string, public PaneState) {
	state := states[paneID]
	state.Watch = public.Watch
	state.WatchSet = public.WatchSet
	state.State = public.State
	state.Since = public.Since
	state.Changed = public.Changed
	state.Proc = public.Proc
	states[paneID] = state
}

func clearPublicState(state *PaneState) {
	state.Watch = false
	state.WatchSet = false
	state.State = ""
	state.Since = 0
	state.Changed = 0
	state.Proc = ""
}

func adjustSnapshotWindowCount(states map[string]PaneState, pane PaneState, delta int) {
	target := windowTarget(pane)
	for id, state := range states {
		if windowTarget(state) != target {
			continue
		}
		state.WindowUnreadCount += delta
		if state.WindowUnreadCount < 0 {
			state.WindowUnreadCount = 0
		}
		states[id] = state
	}
}

func activityWindowKey(state PaneState) string {
	if state.WindowID != "" {
		return state.WindowID
	}
	return windowTarget(state)
}

func (w *Watcher) logf(format string, args ...any) {
	w.logger.Printf(format, args...)
}

func (w *Watcher) recordPublishedTransition(
	states map[string]PaneState,
	paneID string,
	observed PaneState,
	memory *watchedPane,
	desired PaneState,
) {
	if memory.published.State != StateUnread && desired.State == StateUnread {
		adjustSnapshotWindowCount(states, observed, 1)
	} else if memory.published.State == StateUnread && desired.State != StateUnread {
		adjustSnapshotWindowCount(states, observed, -1)
	}
	updateSnapshotPublicState(states, paneID, desired)
	w.logf(
		"transition pane=%s state=%s->%s watch=%t proc=%q",
		paneID, memory.published.State, desired.State, desired.Watch, desired.Proc,
	)
	memory.published = desired
}

// observePane is the pure episode state machine. Fired is daemon-private:
// after read-on-visit the pane returns to quiet, but cannot flag again until
// its screen changes.
func observePane(
	state PaneState,
	hash string,
	process string,
	focused bool,
	now time.Time,
	quietFor time.Duration,
) (PaneState, bool) {
	next := state
	nowUnix := now.Unix()

	if !state.WatchSet {
		next.Watch = true
		next.WatchSet = true
		next.State = StateActive
		next.Since = nowUnix
		next.Changed = nowUnix
		next.Hash = hash
		next.Proc = process
		next.Fired = false
		return next, true
	}

	// Older watcher state has no dedicated screen-change timestamp. Initialize
	// it to now, which deliberately underestimates age instead of guessing that
	// the pane is stale.
	if next.Changed == 0 {
		next.Changed = nowUnix
	}

	processChanged := state.Proc != "" && process != "" && state.Proc != process
	if !state.Watch {
		if processChanged {
			next.Watch = true
			next.State = StateActive
			next.Since = nowUnix
			next.Changed = nowUnix
			next.Hash = hash
			next.Proc = process
			next.Fired = false
			return next, true
		}
		if next.Proc == "" && process != "" {
			next.Proc = process
		}
		if next.State == StateUnread {
			next.State = StateQuiet
			next.Since = nowUnix
			next.Fired = true
		}
		if next.Hash != hash {
			next.State = StateActive
			next.Since = nowUnix
			next.Changed = nowUnix
			next.Hash = hash
			next.Fired = false
		}
		if !validState(next.State) {
			next.State = StateActive
			next.Since = nowUnix
		}
		return next, !sameWatchFields(state, next)
	}

	if processChanged || state.Hash == "" || state.Hash != hash {
		next.State = StateActive
		next.Since = nowUnix
		next.Changed = nowUnix
		next.Hash = hash
		next.Proc = process
		next.Fired = false
		return next, true
	}
	if next.Proc == "" && process != "" {
		next.Proc = process
	}

	switch state.State {
	case StateActive:
		next.State = StateQuiet
		if next.Since == 0 {
			next.Since = nowUnix
		}
	case StateQuiet:
		if !state.Fired && now.Sub(time.Unix(state.Since, 0)) >= quietFor {
			next.Since = nowUnix
			next.Fired = true
			if focused {
				// The user is already looking at the pane, so consume this
				// episode without briefly creating an unread badge.
				next.State = StateQuiet
			} else {
				next.State = StateUnread
			}
		}
	case StateUnread:
		if !next.Fired {
			next.Fired = true
		}
		if focused {
			next.State = StateQuiet
			next.Since = nowUnix
		}
	default:
		next.State = StateActive
		next.Since = nowUnix
		next.Fired = false
	}
	return next, !sameWatchFields(state, next)
}

func validState(state AttentionState) bool {
	return state == StateActive || state == StateQuiet || state == StateUnread
}

func sameWatchFields(left, right PaneState) bool {
	return left.Watch == right.Watch &&
		left.WatchSet == right.WatchSet &&
		left.State == right.State &&
		left.Since == right.Since &&
		left.Changed == right.Changed &&
		left.Hash == right.Hash &&
		left.Proc == right.Proc &&
		left.Fired == right.Fired
}

// screenHash strips terminal controls and hashes only the configured tail.
func screenHash(captured string, lineLimit int) string {
	stripped := ansi.Strip(strings.ReplaceAll(captured, "\r", ""))
	stripped = strings.TrimRight(stripped, "\n")
	lines := strings.Split(stripped, "\n")
	if lineLimit > 0 && len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// DaemonPaths holds per-tmux-socket watcher state.
type DaemonPaths struct {
	Dir     string
	PIDFile string
	LogFile string
}

// CurrentDaemonPaths resolves ~/.local/state/tmx/<socket>/ by default.
// TMX_STATE_DIR and XDG_STATE_HOME are honored for overrides.
func CurrentDaemonPaths() (DaemonPaths, error) {
	root := strings.TrimSpace(os.Getenv("TMX_STATE_DIR"))
	if root == "" {
		if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
			root = filepath.Join(xdg, "tmx")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return DaemonPaths{}, err
			}
			root = filepath.Join(home, ".local", "state", "tmx")
		}
	}

	socket := "default"
	if tmuxEnv := os.Getenv("TMUX"); tmuxEnv != "" {
		socket = strings.SplitN(tmuxEnv, ",", 2)[0]
	}
	dir := filepath.Join(root, encodeStateComponent(socket))
	return DaemonPaths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "watch.pid"),
		LogFile: filepath.Join(dir, "watch.log"),
	}, nil
}

func encodeStateComponent(value string) string {
	var encoded strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'a' && b <= 'z',
			b >= 'A' && b <= 'Z',
			b >= '0' && b <= '9',
			b == '.', b == '_', b == '-':
			encoded.WriteByte(b)
		default:
			fmt.Fprintf(&encoded, "%%%02X", b)
		}
	}
	return encoded.String()
}

// DaemonStatus reports whether the socket's watcher process is alive.
type DaemonStatus struct {
	Running bool
	PID     int
	LogFile string
}

type daemonBackend interface {
	CurrentExecutable() (string, error)
	CurrentPID() int
	Spawn(executable string, args []string, logPath string) (int, error)
	Alive(pid int) bool
	Signal(pid int, signal os.Signal) error
}

type systemDaemonBackend struct{}

func (systemDaemonBackend) CurrentExecutable() (string, error) {
	return os.Executable()
}

func (systemDaemonBackend) CurrentPID() int {
	return os.Getpid()
}

func (systemDaemonBackend) Spawn(executable string, args []string, logPath string) (int, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer func() { _ = logFile.Close() }()

	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	if err := command.Process.Release(); err != nil {
		_ = command.Process.Kill()
		return 0, err
	}
	return pid, nil
}

func (systemDaemonBackend) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (systemDaemonBackend) Signal(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}

// DaemonManager owns pidfile lifecycle for one socket.
type DaemonManager struct {
	Paths        DaemonPaths
	backend      daemonBackend
	startTimeout time.Duration
	stopTimeout  time.Duration
	pollInterval time.Duration
}

func currentDaemonManager() (*DaemonManager, error) {
	paths, err := CurrentDaemonPaths()
	if err != nil {
		return nil, err
	}
	return &DaemonManager{
		Paths:        paths,
		backend:      systemDaemonBackend{},
		startTimeout: 5 * time.Second,
		stopTimeout:  5 * time.Second,
		pollInterval: 50 * time.Millisecond,
	}, nil
}

func (m *DaemonManager) ensureDefaults() {
	if m.backend == nil {
		m.backend = systemDaemonBackend{}
	}
	if m.stopTimeout <= 0 {
		m.stopTimeout = 5 * time.Second
	}
	if m.startTimeout <= 0 {
		m.startTimeout = 5 * time.Second
	}
	if m.pollInterval <= 0 {
		m.pollInterval = 50 * time.Millisecond
	}
}

func (m *DaemonManager) ensureDir() error {
	if err := os.MkdirAll(m.Paths.Dir, 0700); err != nil {
		return err
	}
	return os.Chmod(m.Paths.Dir, 0700)
}

// Start spawns the current executable as "watch run". It is a no-op while the
// pidfile is locked by a watcher and replaces stale, unlocked pidfiles.
func (m *DaemonManager) Start() (DaemonStatus, bool, error) {
	m.ensureDefaults()
	if err := m.ensureDir(); err != nil {
		return DaemonStatus{}, false, err
	}

	var result DaemonStatus
	var started bool
	err := m.withLock(func() error {
		status, err := m.statusUnlocked(true)
		if err != nil {
			return err
		}
		if status.Running {
			result = status
			return nil
		}

		executable, err := m.backend.CurrentExecutable()
		if err != nil {
			return err
		}
		pid, err := m.backend.Spawn(executable, []string{"watch", "run"}, m.Paths.LogFile)
		if err != nil {
			return fmt.Errorf("starting watcher: %w", err)
		}

		deadline := time.Now().Add(m.startTimeout)
		for {
			status, err := m.statusUnlocked(false)
			if err != nil {
				return err
			}
			if status.Running {
				if status.PID != pid {
					return fmt.Errorf("watcher pid %d claimed the socket while starting pid %d", status.PID, pid)
				}
				result = status
				started = true
				return nil
			}
			if !m.backend.Alive(pid) {
				return fmt.Errorf("watcher exited before becoming ready; see %s", m.Paths.LogFile)
			}
			if !time.Now().Before(deadline) {
				_ = m.backend.Signal(pid, syscall.SIGTERM)
				return fmt.Errorf("watcher pid %d did not become ready within %s; see %s", pid, m.startTimeout, m.Paths.LogFile)
			}
			time.Sleep(m.pollInterval)
		}
	})
	return result, started, err
}

// Status removes stale pidfiles and reports the current watcher.
func (m *DaemonManager) Status() (DaemonStatus, error) {
	m.ensureDefaults()
	if err := m.ensureDir(); err != nil {
		return DaemonStatus{}, err
	}
	var status DaemonStatus
	err := m.withLock(func() error {
		var err error
		status, err = m.statusUnlocked(true)
		return err
	})
	return status, err
}

func (m *DaemonManager) statusUnlocked(removeStale bool) (DaemonStatus, error) {
	pidFile, err := os.OpenFile(m.Paths.PIDFile, os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return DaemonStatus{LogFile: m.Paths.LogFile}, nil
	}
	if err != nil {
		return DaemonStatus{}, err
	}
	defer func() { _ = pidFile.Close() }()

	err = syscall.Flock(int(pidFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		if removeStale && sameFileAtPath(pidFile, m.Paths.PIDFile) {
			_ = os.Remove(m.Paths.PIDFile)
		}
		_ = syscall.Flock(int(pidFile.Fd()), syscall.LOCK_UN)
		return DaemonStatus{LogFile: m.Paths.LogFile}, nil
	}
	if !lockWouldBlock(err) {
		return DaemonStatus{}, err
	}

	pid, err := readPIDFile(pidFile)
	if err != nil {
		// A watcher holds the file lock but has not marked its first tick
		// ready yet.
		return DaemonStatus{LogFile: m.Paths.LogFile}, nil
	}
	return DaemonStatus{Running: true, PID: pid, LogFile: m.Paths.LogFile}, nil
}

// Stop asks a live watcher to terminate and waits briefly for its pidfile to
// become stale. Stopping an already-stopped watcher is a successful no-op.
func (m *DaemonManager) Stop() (DaemonStatus, bool, error) {
	m.ensureDefaults()
	if err := m.ensureDir(); err != nil {
		return DaemonStatus{}, false, err
	}

	var status DaemonStatus
	err := m.withLock(func() error {
		var err error
		status, err = m.statusUnlocked(true)
		if err != nil || !status.Running {
			return err
		}
		if signalErr := m.backend.Signal(status.PID, syscall.SIGTERM); signalErr != nil {
			current, currentErr := m.statusUnlocked(true)
			if currentErr != nil {
				return errors.Join(signalErr, currentErr)
			}
			if current.Running && current.PID == status.PID {
				return signalErr
			}
		}
		return nil
	})
	if err != nil || !status.Running {
		return status, false, err
	}

	deadline := time.Now().Add(m.stopTimeout)
	for time.Now().Before(deadline) {
		current, statusErr := m.Status()
		if statusErr != nil {
			return status, false, statusErr
		}
		if !current.Running || current.PID != status.PID {
			return DaemonStatus{}, true, nil
		}
		time.Sleep(m.pollInterval)
	}
	return status, false, fmt.Errorf("watcher pid %d did not stop within %s", status.PID, m.stopTimeout)
}

// RunForeground holds the pidfile lock for the process lifetime. It writes the
// PID only after the first successful inventory tick, which is the detached
// start command's readiness handshake.
func (m *DaemonManager) RunForeground(ctx context.Context, options WatchOptions) (runErr error) {
	m.ensureDefaults()
	if err := m.ensureDir(); err != nil {
		return err
	}
	watcher, err := NewWatcher(options)
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(m.Paths.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	watcher.logger = log.New(logFile, "", log.LstdFlags|log.Lmicroseconds)

	guard, err := acquirePIDGuard(m.Paths.PIDFile, m.backend.CurrentPID(), m.startTimeout, m.pollInterval)
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()
	pid := m.backend.CurrentPID()
	watcher.logf("watcher starting pid=%d", pid)
	defer func() {
		if runErr != nil {
			watcher.logf("watcher stopped pid=%d error=%v", pid, runErr)
		} else {
			watcher.logf("watcher stopped pid=%d", pid)
		}
	}()

	for {
		if err := watcher.Tick(time.Now()); err == nil {
			break
		} else {
			watcher.logf("initial tick error: %v", err)
		}
		timer := time.NewTimer(options.Poll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil
		case <-timer.C:
		}
	}
	if err := guard.MarkReady(); err != nil {
		return err
	}
	return watcher.runAfterInitialTick(ctx)
}

func (m *DaemonManager) withLock(operation func() error) error {
	lockPath := filepath.Join(m.Paths.Dir, "watch.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return operation()
}

func readPIDFile(file *os.File) (int, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return 0, err
	}
	data := make([]byte, 64)
	n, err := file.Read(data)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data[:n])))
	if err != nil || pid <= 0 {
		return 0, errors.New("invalid watcher pidfile")
	}
	return pid, nil
}

func writePID(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0600)
}

type pidGuard struct {
	file *os.File
	path string
	pid  int
}

func acquirePIDGuard(path string, pid int, timeout, poll time.Duration) (*pidGuard, error) {
	deadline := time.Now().Add(timeout)
	observedOwner := 0
	ownerObservations := 0
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if !sameFileAtPath(file, path) {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
				continue
			}
			if err := file.Truncate(0); err != nil {
				_ = file.Close()
				return nil, err
			}
			return &pidGuard{file: file, path: path, pid: pid}, nil
		}
		if !lockWouldBlock(err) {
			_ = file.Close()
			return nil, err
		}
		owner, ownerErr := readPIDFile(file)
		_ = file.Close()
		if ownerErr == nil {
			if owner == observedOwner {
				ownerObservations++
			} else {
				observedOwner = owner
				ownerObservations = 1
			}
			// Lifecycle commands briefly lock stale pidfiles while inspecting
			// them. Require the same locked owner twice before treating it as
			// the long-held daemon lock.
			if ownerObservations >= 2 {
				return nil, fmt.Errorf("watcher already running (pid %d)", owner)
			}
		} else {
			observedOwner = 0
			ownerObservations = 0
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("watcher pidfile remained locked without a ready owner")
		}
		time.Sleep(poll)
	}
}

func (g *pidGuard) MarkReady() error {
	if err := g.file.Truncate(0); err != nil {
		return err
	}
	if _, err := g.file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(g.file, "%d\n", g.pid); err != nil {
		return err
	}
	return g.file.Sync()
}

func (g *pidGuard) Close() error {
	var errs []error
	if sameFileAtPath(g.file, g.path) {
		if err := os.Remove(g.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := syscall.Flock(int(g.file.Fd()), syscall.LOCK_UN); err != nil {
		errs = append(errs, err)
	}
	if err := g.file.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func lockWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func sameFileAtPath(file *os.File, path string) bool {
	opened, openedErr := file.Stat()
	current, currentErr := os.Stat(path)
	return openedErr == nil && currentErr == nil && os.SameFile(opened, current)
}

func StartDaemon() (DaemonStatus, bool, error) {
	manager, err := currentDaemonManager()
	if err != nil {
		return DaemonStatus{}, false, err
	}
	return manager.Start()
}

func StopDaemon() (DaemonStatus, bool, error) {
	manager, err := currentDaemonManager()
	if err != nil {
		return DaemonStatus{}, false, err
	}
	return manager.Stop()
}

func StatusDaemon() (DaemonStatus, error) {
	manager, err := currentDaemonManager()
	if err != nil {
		return DaemonStatus{}, err
	}
	return manager.Status()
}

func RunDaemon(ctx context.Context, options WatchOptions) error {
	manager, err := currentDaemonManager()
	if err != nil {
		return err
	}
	return manager.RunForeground(ctx, options)
}
