package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type SessionInfo struct {
	Name     string
	Windows  int
	Attached bool
	Activity int64
}

type WindowInfo struct {
	Target  string // session_name:window_index
	Session string
	Index   int
	Name    string
	Label   string
	Path    string
}

type PaneInfo struct {
	Target      string
	Session     string
	WindowIndex int
	WindowName  string
	PaneIndex   int
	PID         int
	Label       string
	Command     string
	Path        string
}

// ScratchSnapshot bundles a scratch session's metadata, read in a single
// list-sessions call (the @shadow_* user options are expanded inline). This is
// what keeps reap O(1) tmux calls instead of O(sessions) show-options calls.
type ScratchSnapshot struct {
	Name          string
	Created       time.Time
	Activity      time.Time
	Cwd           string // @shadow_cwd
	ParentPane    string // @shadow_parent_pane
	OpenedAt      string // raw @shadow_opened_at (RFC3339 or "")
	LastToggledAt string // raw @shadow_last_toggled_at
}

func run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %s (%w)", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

func SessionExists(name string) bool {
	_, err := run("has-session", "-t", "="+name)
	return err == nil
}

func NewSession(name, startDir string) error {
	_, err := run("new-session", "-d", "-s", name, "-c", startDir)
	return err
}

func NewSessionWithCommand(name, startDir string, env []string, command string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", startDir}
	for _, entry := range env {
		args = append(args, "-e", entry)
	}
	if command != "" {
		args = append(args, command)
	}
	_, err := run(args...)
	return err
}

func KillSession(name string) error {
	_, err := run("kill-session", "-t", "="+name)
	return err
}

func SwitchClient(target string) error {
	_, err := run("switch-client", "-t", "="+target)
	return err
}

func Attach(target string) error {
	cmd := exec.Command("tmux", "attach-session", "-t", "="+target)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func BindKeyRaw(args ...string) error {
	fullArgs := append([]string{"bind-key"}, args...)
	_, err := run(fullArgs...)
	return err
}

func ListSessions() ([]string, error) {
	out, err := run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func RenameSession(oldName, newName string) error {
	_, err := run("rename-session", "-t", "="+oldName, newName)
	return err
}

func CurrentSession() (string, error) {
	return run("display-message", "-p", "#{session_name}")
}

func CurrentClient() (string, error) {
	return run("display-message", "-p", "#{client_name}")
}

func CurrentTarget() (string, error) {
	return run("display-message", "-p", "#{session_name}:#{window_index}.#{pane_index}")
}

func ListSessionInfo() ([]SessionInfo, error) {
	out, err := run("list-sessions", "-F", "#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_activity}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var sessions []SessionInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		wins, _ := strconv.Atoi(parts[1])
		activity, _ := strconv.ParseInt(parts[3], 10, 64)
		sessions = append(sessions, SessionInfo{
			Name:     parts[0],
			Windows:  wins,
			Attached: parts[2] != "0",
			Activity: activity,
		})
	}
	return sessions, nil
}

// ActivePaneCommands maps each session name to the foreground command of its
// active pane (the active pane of the session's active window). Used by the
// session-tree picker to show what each session is currently running.
func ActivePaneCommands() (map[string]string, error) {
	out, err := run("list-panes", "-a", "-F", "#{session_name}\t#{window_active}\t#{pane_active}\t#{pane_current_command}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return map[string]string{}, nil
		}
		return nil, err
	}
	commands := map[string]string{}
	if out == "" {
		return commands, nil
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		if parts[1] != "1" || parts[2] != "1" {
			continue
		}
		commands[parts[0]] = parts[3]
	}
	return commands, nil
}

// PaneID returns the unique id of the pane this tmx process runs in.
//
// It prefers $TMUX_PANE — tmux sets this per-process at spawn time and it
// never changes when focus moves. `display-message` without a target instead
// resolves to the client's *active* pane, so a background agent triggering a
// rename after the user switched panes would label the wrong one. Fall back to
// display-message only when the env var is missing (e.g. invoked via a hook).
func PaneID() (string, error) {
	if p := os.Getenv("TMUX_PANE"); p != "" {
		return p, nil
	}
	return run("display-message", "-p", "#{pane_id}")
}

func SetPaneVar(target, key, value string) error {
	_, err := run("set-option", "-p", "-t", target, "@"+key, value)
	return err
}

func SetCurrentPaneLabel(value string) error {
	target, err := PaneID()
	if err != nil {
		return err
	}
	return SetPaneVar(target, "pane_label", value)
}

func UnsetCurrentPaneLabel() error {
	target, err := PaneID()
	if err != nil {
		return err
	}
	_, err = run("set-option", "-p", "-t", target, "-u", "@pane_label")
	return err
}

func UnsetSessionVar(session, key string) error {
	_, err := run("set-option", "-t", session, "-u", "@"+key)
	return err
}

func MoveCurrentWindow(targetSession string) error {
	_, err := run("move-window", "-t", "="+targetSession+":")
	return err
}

func KillWindow(target string) error {
	_, err := run("kill-window", "-t", target)
	return err
}

func RenameCurrentWindow(name string) error {
	target, err := PaneID()
	if err != nil {
		return err
	}
	// Target the calling pane's window (tmux resolves a pane id to its window)
	// rather than the client's active window, which may have moved.
	_, err = run("rename-window", "-t", target, name)
	return err
}

func DisableCurrentWindowAutoRename() error {
	target, err := PaneID()
	if err != nil {
		return err
	}
	_, err = run("set-option", "-w", "-t", target, "automatic-rename", "off")
	return err
}

func PaneCwd(target string) (string, error) {
	return run("display-message", "-t", target, "-p", "#{pane_current_path}")
}

func SetSessionVar(session, key, value string) error {
	_, err := run("set-option", "-t", session, "@"+key, value)
	return err
}

func GetSessionVar(session, key string) (string, error) {
	return run("show-options", "-t", session, "-v", "@"+key)
}

func DisplayPopup(client, width, height, command string) error {
	_, err := run("display-popup", "-c", client, "-w", width, "-h", height, "-E", command)
	return err
}

func ClosePopup(client string) error {
	_, err := run("display-popup", "-C", "-c", client)
	return err
}

// ListScratchSnapshots returns sessions whose name starts with prefix, with
// their scratch metadata expanded inline (one tmux call regardless of count).
func ListScratchSnapshots(prefix string) ([]ScratchSnapshot, error) {
	out, err := run("list-sessions", "-F", "#{session_name}\t#{session_created}\t#{session_activity}\t#{@shadow_cwd}\t#{@shadow_parent_pane}\t#{@shadow_opened_at}\t#{@shadow_last_toggled_at}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var snapshots []ScratchSnapshot
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 7)
		if len(parts) < 7 || !strings.HasPrefix(parts[0], prefix) {
			continue
		}
		created, _ := strconv.ParseInt(parts[1], 10, 64)
		activity, _ := strconv.ParseInt(parts[2], 10, 64)
		snapshots = append(snapshots, ScratchSnapshot{
			Name:          parts[0],
			Created:       time.Unix(created, 0).UTC(),
			Activity:      time.Unix(activity, 0).UTC(),
			Cwd:           parts[3],
			ParentPane:    parts[4],
			OpenedAt:      parts[5],
			LastToggledAt: parts[6],
		})
	}
	return snapshots, nil
}

// LivePaneIDs returns the set of every pane id across all sessions, so orphan
// detection is one tmux call instead of a has-pane probe per scratch session.
func LivePaneIDs() (map[string]bool, error) {
	out, err := run("list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	ids := map[string]bool{}
	if out == "" {
		return ids, nil
	}
	for _, line := range strings.Split(out, "\n") {
		ids[line] = true
	}
	return ids, nil
}

func PaneExists(paneID string) bool {
	_, err := run("display-message", "-t", paneID, "-p", "")
	return err == nil
}

func ListWindowInfo() ([]WindowInfo, error) {
	out, err := run("list-windows", "-a", "-F", "#{session_name}:#{window_index}\t#{session_name}\t#{window_index}\t#{window_name}\t#{@pane_label}\t#{pane_current_path}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var windows []WindowInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}
		idx, _ := strconv.Atoi(parts[2])
		windows = append(windows, WindowInfo{
			Target:  parts[0],
			Session: parts[1],
			Index:   idx,
			Name:    parts[3],
			Label:   parts[4],
			Path:    parts[5],
		})
	}
	return windows, nil
}

func ListPaneInfo() ([]PaneInfo, error) {
	out, err := run("list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index}\t#{session_name}\t#{window_index}\t#{window_name}\t#{pane_index}\t#{pane_pid}\t#{@pane_label}\t#{pane_current_command}\t#{pane_current_path}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var panes []PaneInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 9)
		if len(parts) < 9 {
			continue
		}
		winIdx, _ := strconv.Atoi(parts[2])
		paneIdx, _ := strconv.Atoi(parts[4])
		pid, _ := strconv.Atoi(parts[5])
		panes = append(panes, PaneInfo{
			Target:      parts[0],
			Session:     parts[1],
			WindowIndex: winIdx,
			WindowName:  parts[3],
			PaneIndex:   paneIdx,
			PID:         pid,
			Label:       parts[6],
			Command:     parts[7],
			Path:        parts[8],
		})
	}
	return panes, nil
}
