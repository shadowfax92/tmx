package sessionmux

import (
	"slices"
	"testing"

	"tmx/internal/config"
)

func TestSessionNameMappingPrefixesLogicalNames(t *testing.T) {
	client := New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, nil)

	if got, want := client.PhysicalSessionName("codex/feat-example"), "rmx/codex/feat-example"; got != want {
		t.Fatalf("PhysicalSessionName() = %q, want %q", got, want)
	}
}

func TestSessionNameMappingAvoidsDoublePrefix(t *testing.T) {
	client := New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, nil)

	if got, want := client.PhysicalSessionName("rmx/codex/feat-example"), "rmx/codex/feat-example"; got != want {
		t.Fatalf("PhysicalSessionName() = %q, want %q", got, want)
	}
}

func TestTmuxCommandShapesForSfAutoMuxSubset(t *testing.T) {
	client := New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, nil)

	tests := []struct {
		name        string
		input       []string
		wantArgs    []string
		interactive bool
	}{
		{
			name:     "has session",
			input:    []string{"has-session", "-t", "codex/feat-example"},
			wantArgs: []string{"has-session", "-t", "=rmx/codex/feat-example"},
		},
		{
			name:     "new session",
			input:    []string{"new-session", "-d", "-s", "codex/feat-example", "-c", "/tmp/work", "launcher"},
			wantArgs: []string{"new-session", "-d", "-s", "rmx/codex/feat-example", "-c", "/tmp/work", "launcher"},
		},
		{
			name:     "set buffer",
			input:    []string{"set-buffer", "--", "line 1\nline 2"},
			wantArgs: []string{"set-buffer", "--", "line 1\nline 2"},
		},
		{
			name:     "paste buffer",
			input:    []string{"paste-buffer", "-t", "codex/feat-example", "-p"},
			wantArgs: []string{"paste-buffer", "-t", "=rmx/codex/feat-example", "-p"},
		},
		{
			name:     "send enter",
			input:    []string{"send-keys", "-t", "codex/feat-example", "Enter"},
			wantArgs: []string{"send-keys", "-t", "=rmx/codex/feat-example", "Enter"},
		},
		{
			name:        "attach",
			input:       []string{"attach-session", "-t", "codex/feat-example"},
			wantArgs:    []string{"attach-session", "-t", "=rmx/codex/feat-example"},
			interactive: true,
		},
		{
			name:     "kill",
			input:    []string{"kill-session", "-t", "codex/feat-example"},
			wantArgs: []string{"kill-session", "-t", "=rmx/codex/feat-example"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := client.Plan(tt.input)
			if err != nil {
				t.Fatalf("Plan() error = %v", err)
			}
			if got.Program != "tmux" {
				t.Fatalf("Program = %q, want tmux", got.Program)
			}
			if !slices.Equal(got.Args, tt.wantArgs) {
				t.Fatalf("Args = %#v, want %#v", got.Args, tt.wantArgs)
			}
			if got.Interactive != tt.interactive {
				t.Fatalf("Interactive = %v, want %v", got.Interactive, tt.interactive)
			}
		})
	}
}

func TestTmuxTargetMappingAvoidsDoublePrefix(t *testing.T) {
	client := New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, nil)

	got, err := client.Plan([]string{"send-keys", "-t", "rmx/codex/feat-example", "Enter"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := []string{"send-keys", "-t", "=rmx/codex/feat-example", "Enter"}
	if !slices.Equal(got.Args, want) {
		t.Fatalf("Args = %#v, want %#v", got.Args, want)
	}
}

func TestRmuxBackendForwardsWithoutPrefixing(t *testing.T) {
	client := New(config.SessionsConfig{Backend: "rmux", Prefix: "rmx"}, nil)

	got, err := client.Plan([]string{"new-session", "-d", "-s", "codex/feat-example"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	want := Command{Program: "rmux", Args: []string{"new-session", "-d", "-s", "codex/feat-example"}}
	if got.Program != want.Program || !slices.Equal(got.Args, want.Args) || got.Interactive != want.Interactive {
		t.Fatalf("Plan() = %#v, want %#v", got, want)
	}
}
