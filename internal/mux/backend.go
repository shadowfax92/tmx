package mux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"tmx/internal/tmux"
)

// ErrNoScratchBackend means no supported multiplexer context was detected.
var ErrNoScratchBackend = errors.New("tmx scratch must run inside tmux or rmux")

// ScratchBackend is the tmux-compatible command surface needed by scratch popups.
type ScratchBackend interface {
	Name() string
	CurrentSession() (string, error)
	CurrentClient() (string, error)
	PaneID() (string, error)
	SessionExists(name string) bool
	NewSessionWithCommand(name, startDir string, env []string, command string) error
	KillSession(name string) error
	SetSessionVar(session, key, value string) error
	GetSessionVar(session, key string) (string, error)
	ListScratchSnapshots(prefix string) ([]tmux.ScratchSnapshot, error)
	LivePaneIDs() (map[string]bool, error)
	PaneExists(paneID string) bool
	PaneCwd(target string) (string, error)
	DisplayPopup(client, width, height, command string) error
	ClosePopup(client string) error
	PopupAttachCommand(targetSession string) string
}

// TmuxBackend implements scratch popups against a tmux server.
type TmuxBackend struct{}

// RmuxBackend implements scratch popups against an rmux server.
type RmuxBackend struct{}

var runCommand = defaultRunCommand

// SelectScratchBackend chooses rmux inside rmux panes, otherwise tmux inside tmux.
func SelectScratchBackend() (ScratchBackend, error) {
	if os.Getenv("RMUX") != "" {
		return RmuxBackend{}, nil
	}
	if tmux.IsInsideTmux() {
		return TmuxBackend{}, nil
	}
	return nil, ErrNoScratchBackend
}

func (TmuxBackend) Name() string { return "tmux" }

func (TmuxBackend) CurrentSession() (string, error) { return tmux.CurrentSession() }

func (TmuxBackend) CurrentClient() (string, error) { return tmux.CurrentClient() }

func (TmuxBackend) PaneID() (string, error) { return tmux.PaneID() }

func (TmuxBackend) SessionExists(name string) bool { return tmux.SessionExists(name) }

func (TmuxBackend) NewSessionWithCommand(name, startDir string, env []string, command string) error {
	return tmux.NewSessionWithCommand(name, startDir, env, command)
}

func (TmuxBackend) KillSession(name string) error { return tmux.KillSession(name) }

func (TmuxBackend) SetSessionVar(session, key, value string) error {
	return tmux.SetSessionVar(session, key, value)
}

func (TmuxBackend) GetSessionVar(session, key string) (string, error) {
	return tmux.GetSessionVar(session, key)
}

func (TmuxBackend) ListScratchSnapshots(prefix string) ([]tmux.ScratchSnapshot, error) {
	return tmux.ListScratchSnapshots(prefix)
}

func (TmuxBackend) LivePaneIDs() (map[string]bool, error) { return tmux.LivePaneIDs() }

func (TmuxBackend) PaneExists(paneID string) bool { return tmux.PaneExists(paneID) }

func (TmuxBackend) PaneCwd(target string) (string, error) { return tmux.PaneCwd(target) }

func (TmuxBackend) DisplayPopup(client, width, height, command string) error {
	return tmux.DisplayPopup(client, width, height, command)
}

func (TmuxBackend) ClosePopup(client string) error { return tmux.ClosePopup(client) }

func (TmuxBackend) PopupAttachCommand(targetSession string) string {
	return fmt.Sprintf("exec tmux attach-session -t '=%s'", targetSession)
}

func (RmuxBackend) Name() string { return "rmux" }

func (RmuxBackend) CurrentSession() (string, error) {
	return runRmux("display-message", "-p", "#{session_name}")
}

func (RmuxBackend) CurrentClient() (string, error) {
	return runRmux("display-message", "-p", "#{client_name}")
}

func (RmuxBackend) PaneID() (string, error) {
	if pane := os.Getenv("RMUX_PANE"); pane != "" {
		return pane, nil
	}
	return runRmux("display-message", "-p", "#{pane_id}")
}

func (RmuxBackend) SessionExists(name string) bool {
	_, err := runRmux("has-session", "-t", "="+name)
	return err == nil
}

func (RmuxBackend) NewSessionWithCommand(name, startDir string, env []string, command string) error {
	args := []string{"new-session", "-d", "-s", name, "-c", startDir}
	for _, entry := range env {
		args = append(args, "-e", entry)
	}
	if command != "" {
		args = append(args, command)
	}
	_, err := runRmux(args...)
	return err
}

func (RmuxBackend) KillSession(name string) error {
	_, err := runRmux("kill-session", "-t", "="+name)
	return err
}

func (RmuxBackend) SetSessionVar(session, key, value string) error {
	_, err := runRmux("set-option", "-t", session, "@"+key, value)
	return err
}

func (RmuxBackend) GetSessionVar(session, key string) (string, error) {
	return runRmux("show-options", "-t", session, "-v", "@"+key)
}

func (RmuxBackend) ListScratchSnapshots(prefix string) ([]tmux.ScratchSnapshot, error) {
	out, err := runRmux("list-sessions", "-F", "#{session_name}\t#{session_created}\t#{session_activity}\t#{@shadow_cwd}\t#{@shadow_parent_pane}\t#{@shadow_opened_at}\t#{@shadow_last_toggled_at}")
	if err != nil {
		if isNoSessionsError(err) {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var snapshots []tmux.ScratchSnapshot
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 7)
		if len(parts) < 7 || !strings.HasPrefix(parts[0], prefix) {
			continue
		}
		created, _ := strconv.ParseInt(parts[1], 10, 64)
		activity, _ := strconv.ParseInt(parts[2], 10, 64)
		snapshots = append(snapshots, tmux.ScratchSnapshot{
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

func (RmuxBackend) LivePaneIDs() (map[string]bool, error) {
	out, err := runRmux("list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		if isNoSessionsError(err) {
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

func (RmuxBackend) PaneExists(paneID string) bool {
	_, err := runRmux("display-message", "-t", paneID, "-p", "")
	return err == nil
}

func (RmuxBackend) PaneCwd(target string) (string, error) {
	return runRmux("display-message", "-t", target, "-p", "#{pane_current_path}")
}

func (RmuxBackend) DisplayPopup(client, width, height, command string) error {
	args := []string{"display-popup", "-w", width, "-h", height, "-E", command}
	if client != "" {
		args = append([]string{"display-popup", "-c", client}, args[1:]...)
	}
	_, err := runRmux(args...)
	return err
}

func (RmuxBackend) ClosePopup(client string) error {
	args := []string{"display-popup", "-C"}
	if client != "" {
		args = append(args, "-c", client)
	}
	_, err := runRmux(args...)
	return err
}

func (RmuxBackend) PopupAttachCommand(targetSession string) string {
	return fmt.Sprintf("exec rmux attach-session -E -t '=%s'", targetSession)
}

func runRmux(args ...string) (string, error) {
	return runCommand("rmux", args...)
}

func defaultRunCommand(program string, args ...string) (string, error) {
	cmd := exec.Command(program, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %s (%w)", program, strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func isNoSessionsError(err error) bool {
	text := err.Error()
	return strings.Contains(text, "no server running") ||
		strings.Contains(text, "no server") ||
		strings.Contains(text, "no sessions")
}
