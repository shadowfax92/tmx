package mux

import (
	"errors"
	"slices"
	"testing"
)

func TestSelectScratchBackendPrefersRmux(t *testing.T) {
	t.Setenv("RMUX", "/tmp/rmux-501/default,1,0")
	t.Setenv("TMUX", "/tmp/tmux-501/default,2,0")

	backend, err := SelectScratchBackend()
	if err != nil {
		t.Fatalf("SelectScratchBackend() error = %v", err)
	}
	if backend.Name() != "rmux" {
		t.Fatalf("backend = %s, want rmux", backend.Name())
	}
}

func TestSelectScratchBackendFallsBackToTmux(t *testing.T) {
	t.Setenv("RMUX", "")
	t.Setenv("TMUX", "/tmp/tmux-501/default,2,0")

	backend, err := SelectScratchBackend()
	if err != nil {
		t.Fatalf("SelectScratchBackend() error = %v", err)
	}
	if backend.Name() != "tmux" {
		t.Fatalf("backend = %s, want tmux", backend.Name())
	}
}

func TestSelectScratchBackendRequiresMultiplexer(t *testing.T) {
	t.Setenv("RMUX", "")
	t.Setenv("TMUX", "")

	_, err := SelectScratchBackend()
	if !errors.Is(err, ErrNoScratchBackend) {
		t.Fatalf("SelectScratchBackend() error = %v, want ErrNoScratchBackend", err)
	}
}

func TestPopupAttachCommands(t *testing.T) {
	if got, want := (TmuxBackend{}).PopupAttachCommand("gs/vim/9"), "exec tmux attach-session -t '=gs/vim/9'"; got != want {
		t.Fatalf("tmux attach command = %q, want %q", got, want)
	}
	if got, want := (RmuxBackend{}).PopupAttachCommand("gs/vim/9"), "exec rmux attach-session -E -t '=gs/vim/9'"; got != want {
		t.Fatalf("rmux attach command = %q, want %q", got, want)
	}
}

func TestRmuxPaneIDPrefersEnvironment(t *testing.T) {
	t.Setenv("RMUX_PANE", "%42")
	var called bool
	restore := stubRunCommand(t, func(program string, args ...string) (string, error) {
		called = true
		return "", nil
	})
	defer restore()

	got, err := (RmuxBackend{}).PaneID()
	if err != nil {
		t.Fatalf("PaneID() error = %v", err)
	}
	if got != "%42" {
		t.Fatalf("PaneID() = %q, want %%42", got)
	}
	if called {
		t.Fatal("PaneID() should not shell out when RMUX_PANE is set")
	}
}

func TestRmuxDisplayPopupCommandShape(t *testing.T) {
	var got commandCall
	restore := stubRunCommand(t, func(program string, args ...string) (string, error) {
		got = commandCall{program: program, args: append([]string(nil), args...)}
		return "", nil
	})
	defer restore()

	err := (RmuxBackend{}).DisplayPopup("123", "80%", "95%", "exec rmux attach-session -E -t '=gs/vim/9'")
	if err != nil {
		t.Fatalf("DisplayPopup() error = %v", err)
	}

	want := commandCall{
		program: "rmux",
		args: []string{
			"display-popup", "-c", "123", "-w", "80%", "-h", "95%",
			"-E", "exec rmux attach-session -E -t '=gs/vim/9'",
		},
	}
	if got.program != want.program || !slices.Equal(got.args, want.args) {
		t.Fatalf("call = %#v, want %#v", got, want)
	}
}

func TestRmuxNewSessionWithCommandShape(t *testing.T) {
	var got commandCall
	restore := stubRunCommand(t, func(program string, args ...string) (string, error) {
		got = commandCall{program: program, args: append([]string(nil), args...)}
		return "", nil
	})
	defer restore()

	err := (RmuxBackend{}).NewSessionWithCommand(
		"gs/sh/9",
		"/tmp/project",
		[]string{"TMX_PARENT_PANE=%9", "TMX_SCRATCH=1"},
		"nvim",
	)
	if err != nil {
		t.Fatalf("NewSessionWithCommand() error = %v", err)
	}

	want := commandCall{
		program: "rmux",
		args: []string{
			"new-session", "-d", "-s", "gs/sh/9", "-c", "/tmp/project",
			"-e", "TMX_PARENT_PANE=%9", "-e", "TMX_SCRATCH=1", "nvim",
		},
	}
	if got.program != want.program || !slices.Equal(got.args, want.args) {
		t.Fatalf("call = %#v, want %#v", got, want)
	}
}

func TestBindRmuxKeyRawCommandShape(t *testing.T) {
	var got commandCall
	restore := stubRunCommand(t, func(program string, args ...string) (string, error) {
		got = commandCall{program: program, args: append([]string(nil), args...)}
		return "", nil
	})
	defer restore()

	err := BindRmuxKeyRaw("-n", "M-I", "run-shell", "-b", `tmx scratch vim`)
	if err != nil {
		t.Fatalf("BindRmuxKeyRaw() error = %v", err)
	}

	want := commandCall{
		program: "rmux",
		args:    []string{"bind-key", "-n", "M-I", "run-shell", "-b", "tmx scratch vim"},
	}
	if got.program != want.program || !slices.Equal(got.args, want.args) {
		t.Fatalf("call = %#v, want %#v", got, want)
	}
}

type commandCall struct {
	program string
	args    []string
}

func stubRunCommand(t *testing.T, fn func(string, ...string) (string, error)) func() {
	t.Helper()
	orig := runCommand
	runCommand = fn
	return func() { runCommand = orig }
}
