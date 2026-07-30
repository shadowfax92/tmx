package cmd

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"tmx/internal/attn"
	"tmx/internal/config"
	"tmx/internal/tmux"
)

func TestJumpSelectsAndClearsOldestUnreadAcrossSessions(t *testing.T) {
	states := []attn.PaneState{
		jumpState("%new", "new", 1, 20),
		jumpState("%old", "old", 3, 10),
	}
	backend := newFakeJumpBackend("%new", "%old")
	var cleared []string

	err := runJump(config.JumpActionSelect, "", jumpDeps{
		backend:  backend,
		snapshot: func() ([]attn.PaneState, error) { return states, nil },
		markRead: func(state attn.PaneState) error {
			cleared = append(cleared, state.ID)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("runJump() error = %v", err)
	}
	wantCalls := []string{
		"switch client-1 old",
		"window =old:3",
		"pane %old",
	}
	if !slices.Equal(backend.calls, wantCalls) {
		t.Fatalf("navigation calls = %#v, want %#v", backend.calls, wantCalls)
	}
	if !slices.Equal(cleared, []string{"%old"}) {
		t.Fatalf("cleared panes = %#v, want oldest pane", cleared)
	}
}

func TestJumpActionsSelectZoomAndFocus(t *testing.T) {
	tests := []struct {
		name         string
		action       config.JumpAction
		focusCommand string
		zoomed       bool
		wantTail     []string
	}{
		{name: "select", action: config.JumpActionSelect},
		{name: "zoom", action: config.JumpActionZoom, wantTail: []string{"zoomed? %1", "zoom %1"}},
		{name: "already zoomed", action: config.JumpActionZoom, zoomed: true, wantTail: []string{"zoomed? %1"}},
		{name: "focus configured", action: config.JumpActionFocus, focusCommand: "focus-tool {pane}", wantTail: []string{"shell focus-tool %1"}},
		{name: "focus unset falls back to select", action: config.JumpActionFocus},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeJumpBackend("%1")
			backend.zoomed = test.zoomed
			state := jumpState("%1", "work", 2, 10)
			err := runJump(test.action, test.focusCommand, jumpDeps{
				backend:  backend,
				snapshot: func() ([]attn.PaneState, error) { return []attn.PaneState{state}, nil },
				markRead: func(attn.PaneState) error { return nil },
			})
			if err != nil {
				t.Fatalf("runJump() error = %v", err)
			}
			base := []string{"switch client-1 work", "window =work:2", "pane %1"}
			want := append(base, test.wantTail...)
			if !slices.Equal(backend.calls, want) {
				t.Fatalf("calls = %#v, want %#v", backend.calls, want)
			}
		})
	}
}

func TestJumpEmptyInboxDisplaysMessageWithoutFocusChange(t *testing.T) {
	backend := newFakeJumpBackend()
	err := runJump(config.JumpActionZoom, "", jumpDeps{
		backend: backend,
		snapshot: func() ([]attn.PaneState, error) {
			return []attn.PaneState{{State: attn.StateQuiet}}, nil
		},
		markRead: func(attn.PaneState) error { return errors.New("unexpected clear") },
	})
	if err != nil {
		t.Fatalf("runJump() error = %v", err)
	}
	if !slices.Equal(backend.calls, []string{"message client-1 inbox zero"}) {
		t.Fatalf("calls = %#v, want only inbox-zero message", backend.calls)
	}
}

func TestRepeatedJumpsWalkOldestToNewest(t *testing.T) {
	states := []attn.PaneState{
		jumpState("%new", "new", 1, 20),
		jumpState("%old", "old", 1, 10),
	}
	backend := newFakeJumpBackend("%new", "%old")
	deps := jumpDeps{
		backend:  backend,
		snapshot: func() ([]attn.PaneState, error) { return states, nil },
		markRead: func(state attn.PaneState) error {
			for i := range states {
				if states[i].ID == state.ID {
					states[i].State = attn.StateQuiet
				}
			}
			return nil
		},
	}
	if err := runJump(config.JumpActionSelect, "", deps); err != nil {
		t.Fatalf("first runJump() error = %v", err)
	}
	if err := runJump(config.JumpActionSelect, "", deps); err != nil {
		t.Fatalf("second runJump() error = %v", err)
	}
	var selected []string
	for _, call := range backend.calls {
		if len(call) > 5 && call[:5] == "pane " {
			selected = append(selected, call[5:])
		}
	}
	if !slices.Equal(selected, []string{"%old", "%new"}) {
		t.Fatalf("selected panes = %#v, want oldest then newest", selected)
	}
}

func TestJumpSkipsPaneThatVanishedAfterSnapshot(t *testing.T) {
	states := []attn.PaneState{
		jumpState("%gone", "gone", 1, 10),
		jumpState("%live", "live", 2, 20),
	}
	backend := newFakeJumpBackend("%live")
	var cleared string
	err := runJump(config.JumpActionSelect, "", jumpDeps{
		backend:  backend,
		snapshot: func() ([]attn.PaneState, error) { return states, nil },
		markRead: func(state attn.PaneState) error {
			cleared = state.ID
			return nil
		},
	})
	if err != nil {
		t.Fatalf("runJump() error = %v", err)
	}
	if cleared != "%live" {
		t.Fatalf("cleared pane = %q, want next live pane", cleared)
	}
	if slices.Contains(backend.calls, "pane %gone") {
		t.Fatalf("navigation attempted vanished pane: %#v", backend.calls)
	}
}

func jumpState(id, session string, window int, since int64) attn.PaneState {
	return attn.PaneState{
		PaneInfo: tmux.PaneInfo{ID: id, Session: session, WindowIndex: window},
		Watch:    true,
		WatchSet: true,
		State:    attn.StateUnread,
		Since:    since,
		Fired:    true,
	}
}

type fakeJumpBackend struct {
	live   map[string]bool
	zoomed bool
	calls  []string
}

func newFakeJumpBackend(live ...string) *fakeJumpBackend {
	panes := make(map[string]bool, len(live))
	for _, pane := range live {
		panes[pane] = true
	}
	return &fakeJumpBackend{live: panes}
}

func (b *fakeJumpBackend) CurrentClient() (string, error) { return "client-1", nil }
func (b *fakeJumpBackend) PaneExists(target string) bool  { return b.live[target] }
func (b *fakeJumpBackend) SwitchClient(client, session string) error {
	b.calls = append(b.calls, fmt.Sprintf("switch %s %s", client, session))
	return nil
}
func (b *fakeJumpBackend) SelectWindow(target string) error {
	b.calls = append(b.calls, "window "+target)
	return nil
}
func (b *fakeJumpBackend) SelectPane(target string) error {
	b.calls = append(b.calls, "pane "+target)
	return nil
}
func (b *fakeJumpBackend) PaneWindowZoomed(target string) (bool, error) {
	b.calls = append(b.calls, "zoomed? "+target)
	return b.zoomed, nil
}
func (b *fakeJumpBackend) TogglePaneZoom(target string) error {
	b.calls = append(b.calls, "zoom "+target)
	return nil
}
func (b *fakeJumpBackend) DisplayMessage(client, message string) error {
	b.calls = append(b.calls, fmt.Sprintf("message %s %s", client, message))
	return nil
}
func (b *fakeJumpBackend) RunShell(command string) error {
	b.calls = append(b.calls, "shell "+command)
	return nil
}
