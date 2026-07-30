package attn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	watchSnapshot  = Snapshot
	watchDiscover  = Discover
	watchCapture   = tmux.CapturePane
	watchFocused   = tmux.FocusedPaneIDs
	watchSet       = Set
	watchClear     = Clear
	watchReconcile = ReconcileWindowUnreadCounts
)

// Watcher ties discovery, screen capture, and the pane state machine together.
// Calls that race pane churn are deliberately best-effort; inventory failures
// are returned so the foreground loop can exit cleanly when tmux goes away.
type Watcher struct {
	options WatchOptions
}

func NewWatcher(options WatchOptions) (*Watcher, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	return &Watcher{options: options}, nil
}

// Run polls immediately and then at the configured interval until cancelled
// or the tmux inventory becomes unavailable.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.options.Poll)
	defer ticker.Stop()

	for {
		if err := w.Tick(time.Now()); err != nil {
			// A disappearing tmux server is a normal daemon shutdown. Pane-level
			// churn is already swallowed inside Tick.
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Tick evaluates every currently discovered agent pane once.
func (w *Watcher) Tick(now time.Time) error {
	states, err := watchSnapshot()
	if err != nil {
		return err
	}
	discovered, err := watchDiscover(w.options.Agents)
	if err != nil {
		return err
	}
	focused, err := watchFocused()
	if err != nil {
		return err
	}

	byID := make(map[string]PaneState, len(states))
	for _, state := range states {
		byID[state.ID] = state
	}
	agents := make(map[string]tmux.PaneInfo, len(discovered))
	for _, pane := range discovered {
		agents[pane.ID] = pane
	}

	// A live pane whose agent exited is no longer part of attention state.
	// If the pane vanished entirely, tmux already removed its pane options and
	// the aggregate reconciliation below repairs its old window count.
	for _, state := range states {
		if _, stillAgent := agents[state.ID]; stillAgent || !hasAttentionState(state) {
			continue
		}
		_ = watchClear(state.ID)
	}

	for _, pane := range discovered {
		captured, err := watchCapture(pane.ID)
		if err != nil {
			// The pane may have disappeared between discovery and capture.
			continue
		}

		state := byID[pane.ID]
		state.PaneInfo = pane
		next, changed := observePane(
			state,
			screenHash(captured, w.options.CaptureLines),
			processFingerprint(pane),
			focused[pane.ID],
			now,
			w.options.quietThreshold(),
		)
		if changed {
			// A failing Set is normal if this pane vanished mid-tick.
			_ = watchSet(pane.ID, next)
		}
	}

	return watchReconcile()
}

func hasAttentionState(state PaneState) bool {
	return state.WatchSet || state.State != "" || state.Since != 0 ||
		state.Hash != "" || state.Proc != "" || state.Fired
}

func processFingerprint(pane tmux.PaneInfo) string {
	return strings.TrimSpace(pane.Command)
}

// observePane is the pure episode state machine. Fired is intentionally
// persisted separately from the visible state: after read-on-visit the pane
// returns to quiet, but cannot flag again until its screen changes.
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
		next.Hash = hash
		next.Proc = process
		next.Fired = false
		return next, true
	}

	processChanged := state.Proc != "" && process != "" && state.Proc != process
	if !state.Watch {
		if processChanged {
			next.Watch = true
			next.State = StateActive
			next.Since = nowUnix
			next.Hash = hash
			next.Proc = process
			next.Fired = false
			return next, true
		}
		if next.Proc == "" && process != "" {
			next.Proc = process
		}
		if next.Hash != hash {
			next.State = StateActive
			next.Since = nowUnix
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
	defer logFile.Close()

	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, err
	}
	return command.Process.Pid, nil
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
// recorded process is alive and replaces stale pidfiles.
func (m *DaemonManager) Start() (DaemonStatus, bool, error) {
	m.ensureDefaults()
	if err := m.ensureDir(); err != nil {
		return DaemonStatus{}, false, err
	}

	var result DaemonStatus
	var started bool
	err := m.withLock(func() error {
		status, err := m.statusUnlocked()
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
		if err := writePID(m.Paths.PIDFile, pid); err != nil {
			_ = m.backend.Signal(pid, syscall.SIGTERM)
			return err
		}
		result = DaemonStatus{Running: true, PID: pid}
		started = true
		return nil
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
		status, err = m.statusUnlocked()
		return err
	})
	return status, err
}

func (m *DaemonManager) statusUnlocked() (DaemonStatus, error) {
	pid, err := readPID(m.Paths.PIDFile)
	if errors.Is(err, os.ErrNotExist) {
		return DaemonStatus{}, nil
	}
	if err != nil {
		_ = os.Remove(m.Paths.PIDFile)
		return DaemonStatus{}, nil
	}
	if !m.backend.Alive(pid) {
		_ = os.Remove(m.Paths.PIDFile)
		return DaemonStatus{}, nil
	}
	return DaemonStatus{Running: true, PID: pid}, nil
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
		status, err = m.statusUnlocked()
		if err != nil || !status.Running {
			return err
		}
		if err := m.backend.Signal(status.PID, syscall.SIGTERM); err != nil && m.backend.Alive(status.PID) {
			return err
		}
		return nil
	})
	if err != nil || !status.Running {
		return status, false, err
	}

	deadline := time.Now().Add(m.stopTimeout)
	for m.backend.Alive(status.PID) && time.Now().Before(deadline) {
		time.Sleep(m.pollInterval)
	}
	if m.backend.Alive(status.PID) {
		return status, false, fmt.Errorf("watcher pid %d did not stop within %s", status.PID, m.stopTimeout)
	}
	_ = removePIDIfMatches(m.Paths.PIDFile, status.PID)
	return DaemonStatus{}, true, nil
}

// RunForeground claims the pidfile for this process, executes the polling
// loop, and removes the claim on exit.
func (m *DaemonManager) RunForeground(ctx context.Context, options WatchOptions) error {
	m.ensureDefaults()
	if err := m.ensureDir(); err != nil {
		return err
	}
	watcher, err := NewWatcher(options)
	if err != nil {
		return err
	}

	pid := m.backend.CurrentPID()
	err = m.withLock(func() error {
		status, err := m.statusUnlocked()
		if err != nil {
			return err
		}
		if status.Running && status.PID != pid {
			return fmt.Errorf("watcher already running (pid %d)", status.PID)
		}
		return writePID(m.Paths.PIDFile, pid)
	})
	if err != nil {
		return err
	}
	defer removePIDIfMatches(m.Paths.PIDFile, pid) //nolint:errcheck

	return watcher.Run(ctx)
}

func (m *DaemonManager) withLock(operation func() error) error {
	lockPath := filepath.Join(m.Paths.Dir, "watch.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return operation()
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid watcher pidfile %s", path)
	}
	return pid, nil
}

func writePID(path string, pid int) error {
	temporary := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(temporary, []byte(strconv.Itoa(pid)+"\n"), 0600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func removePIDIfMatches(path string, pid int) error {
	recorded, err := readPID(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if recorded == pid {
		return os.Remove(path)
	}
	return nil
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
