package cmd

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"tmx/internal/mux"
	"tmx/internal/tmux"
)

func TestRunScratchWithBackendOpensRmuxPopup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX", "")

	backend := newScratchFakeBackend("rmux")
	backend.currentClient = "123"
	backend.currentSession = "alpha"
	backend.paneID = "%9"
	backend.paneCwd = t.TempDir()
	backend.livePanes = map[string]bool{"%9": true}

	if err := runScratchWithBackend([]string{"vim"}, backend); err != nil {
		t.Fatalf("runScratchWithBackend() error = %v", err)
	}

	if len(backend.newSessions) != 1 {
		t.Fatalf("new sessions = %#v, want 1", backend.newSessions)
	}
	created := backend.newSessions[0]
	if created.name != "gs/vim/9" || created.startDir != backend.paneCwd || created.command != "nvim" {
		t.Fatalf("created session = %#v", created)
	}
	if !slices.Contains(created.env, "TMX_PARENT_PANE=%9") || !slices.Contains(created.env, "TMX_SCRATCH=1") {
		t.Fatalf("env = %#v, want scratch markers", created.env)
	}
	if got := backend.sessionVar("gs/vim/9", "shadow_client_name"); got != "123" {
		t.Fatalf("shadow_client_name = %q, want 123", got)
	}
	if len(backend.popups) != 1 {
		t.Fatalf("popups = %#v, want 1", backend.popups)
	}
	popup := backend.popups[0]
	if popup.client != "123" || popup.width != "80%" || popup.height != "95%" {
		t.Fatalf("popup = %#v", popup)
	}
	if want := "exec rmux attach-session -E -t '=gs/vim/9'"; popup.command != want {
		t.Fatalf("popup command = %q, want %q", popup.command, want)
	}
}

func TestRunScratchWithBackendClosesStoredRmuxPopupFromScratchSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX", "")

	backend := newScratchFakeBackend("rmux")
	backend.currentClient = "fallback"
	backend.currentSession = "gs/vim/9"
	backend.paneID = "%99"
	backend.setSessionVarValue("gs/vim/9", "shadow_client_name", "123")
	backend.setSessionVarValue("gs/vim/9", "shadow_parent_pane", "%9")

	if err := runScratchWithBackend([]string{"vim"}, backend); err != nil {
		t.Fatalf("runScratchWithBackend() error = %v", err)
	}

	if !slices.Equal(backend.closedPopups, []string{"123"}) {
		t.Fatalf("closed popups = %#v, want [123]", backend.closedPopups)
	}
	if len(backend.popups) != 0 || len(backend.newSessions) != 0 {
		t.Fatalf("unexpected open work: popups=%#v newSessions=%#v", backend.popups, backend.newSessions)
	}
	if got := backend.sessionVar("gs/vim/9", "shadow_last_toggled_at"); got == "" {
		t.Fatal("shadow_last_toggled_at was not recorded")
	}
}

func TestRunScratchWithBackendUsesExplicitKeybindContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMUX", "")

	backend := newScratchFakeBackend("tmux")
	backend.failCurrentLookups = true
	backend.paneCwd = t.TempDir()
	backend.livePanes = map[string]bool{"%5": true}

	if err := runScratchWithBackend([]string{"sh", "client-x", "session-x", "%5"}, backend); err != nil {
		t.Fatalf("runScratchWithBackend() error = %v", err)
	}
	if len(backend.newSessions) != 1 || backend.newSessions[0].name != "gs/sh/5" {
		t.Fatalf("new sessions = %#v, want gs/sh/5", backend.newSessions)
	}
	if got := backend.sessionVar("gs/sh/5", "shadow_client_name"); got != "client-x" {
		t.Fatalf("shadow_client_name = %q, want client-x", got)
	}
}

func TestRunScratchRequiresMultiplexerContext(t *testing.T) {
	t.Setenv("RMUX", "")
	t.Setenv("TMUX", "")

	err := runScratch([]string{"vim"})
	if !errors.Is(err, mux.ErrNoScratchBackend) {
		t.Fatalf("runScratch() error = %v, want ErrNoScratchBackend", err)
	}
}

type scratchFakeBackend struct {
	name               string
	currentSession     string
	currentClient      string
	paneID             string
	paneCwd            string
	failCurrentLookups bool
	vars               map[string]map[string]string
	livePanes          map[string]bool
	newSessions        []fakeNewSession
	popups             []fakePopup
	closedPopups       []string
}

type fakeNewSession struct {
	name     string
	startDir string
	env      []string
	command  string
}

type fakePopup struct {
	client  string
	width   string
	height  string
	command string
}

func newScratchFakeBackend(name string) *scratchFakeBackend {
	return &scratchFakeBackend{
		name:           name,
		currentClient:  "client",
		currentSession: "session",
		paneID:         "%1",
		paneCwd:        os.TempDir(),
		vars:           map[string]map[string]string{},
		livePanes:      map[string]bool{"%1": true},
	}
}

func (b *scratchFakeBackend) Name() string { return b.name }

func (b *scratchFakeBackend) CurrentSession() (string, error) {
	if b.failCurrentLookups {
		return "", errors.New("CurrentSession should not be called")
	}
	return b.currentSession, nil
}

func (b *scratchFakeBackend) CurrentClient() (string, error) {
	if b.failCurrentLookups {
		return "", errors.New("CurrentClient should not be called")
	}
	return b.currentClient, nil
}

func (b *scratchFakeBackend) PaneID() (string, error) {
	if b.failCurrentLookups {
		return "", errors.New("PaneID should not be called")
	}
	return b.paneID, nil
}

func (b *scratchFakeBackend) SessionExists(name string) bool {
	_, ok := b.vars[name]
	return ok
}

func (b *scratchFakeBackend) NewSessionWithCommand(name, startDir string, env []string, command string) error {
	b.newSessions = append(b.newSessions, fakeNewSession{
		name:     name,
		startDir: startDir,
		env:      append([]string(nil), env...),
		command:  command,
	})
	if _, ok := b.vars[name]; !ok {
		b.vars[name] = map[string]string{}
	}
	return nil
}

func (b *scratchFakeBackend) KillSession(name string) error {
	delete(b.vars, name)
	return nil
}

func (b *scratchFakeBackend) SetSessionVar(session, key, value string) error {
	b.setSessionVarValue(session, key, value)
	return nil
}

func (b *scratchFakeBackend) GetSessionVar(session, key string) (string, error) {
	if value := b.sessionVar(session, key); value != "" {
		return value, nil
	}
	return "", nil
}

func (b *scratchFakeBackend) ListScratchSnapshots(prefix string) ([]tmux.ScratchSnapshot, error) {
	var snapshots []tmux.ScratchSnapshot
	for session, vars := range b.vars {
		if !strings.HasPrefix(session, prefix) {
			continue
		}
		snapshots = append(snapshots, tmux.ScratchSnapshot{
			Name:          session,
			Created:       time.Now().Add(-time.Hour),
			Activity:      time.Now(),
			Cwd:           vars["shadow_cwd"],
			ParentPane:    vars["shadow_parent_pane"],
			OpenedAt:      vars["shadow_opened_at"],
			LastToggledAt: vars["shadow_last_toggled_at"],
		})
	}
	return snapshots, nil
}

func (b *scratchFakeBackend) LivePaneIDs() (map[string]bool, error) {
	return b.livePanes, nil
}

func (b *scratchFakeBackend) PaneExists(paneID string) bool {
	return b.livePanes[paneID]
}

func (b *scratchFakeBackend) PaneCwd(target string) (string, error) {
	if !b.PaneExists(target) {
		return "", fmt.Errorf("missing pane %s", target)
	}
	return b.paneCwd, nil
}

func (b *scratchFakeBackend) DisplayPopup(client, width, height, command string) error {
	b.popups = append(b.popups, fakePopup{
		client:  client,
		width:   width,
		height:  height,
		command: command,
	})
	return nil
}

func (b *scratchFakeBackend) ClosePopup(client string) error {
	b.closedPopups = append(b.closedPopups, client)
	return nil
}

func (b *scratchFakeBackend) PopupAttachCommand(targetSession string) string {
	return fmt.Sprintf("exec %s attach-session%s -t '=%s'", b.name, attachFlag(b.name), targetSession)
}

func (b *scratchFakeBackend) setSessionVarValue(session, key, value string) {
	if _, ok := b.vars[session]; !ok {
		b.vars[session] = map[string]string{}
	}
	b.vars[session][key] = value
}

func (b *scratchFakeBackend) sessionVar(session, key string) string {
	if vars, ok := b.vars[session]; ok {
		return vars[key]
	}
	return ""
}

func attachFlag(name string) string {
	if name == "rmux" {
		return " -E"
	}
	return ""
}
