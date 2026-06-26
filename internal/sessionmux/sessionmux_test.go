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

func TestExitCurrentKillsPrefixedTmuxSession(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"rmx/codex/feat-example"}}
	client := New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, runner)

	if err := client.ExitCurrent(); err != nil {
		t.Fatalf("ExitCurrent() error = %v", err)
	}

	want := []recordedCall{
		{program: "tmux", args: []string{"display-message", "-p", "#{session_name}"}},
		{program: "tmux", args: []string{"kill-session", "-t", "=rmx/codex/feat-example"}},
	}
	if !callsEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestExitCurrentRefusesNonPrefixedTmuxSession(t *testing.T) {
	runner := &fakeRunner{outputs: []string{"main"}}
	client := New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, runner)

	if err := client.ExitCurrent(); err == nil {
		t.Fatal("ExitCurrent() error = nil, want refusal")
	}

	if len(runner.calls) != 1 {
		t.Fatalf("calls = %#v, want only display-message", runner.calls)
	}
}

type fakeRunner struct {
	calls   []recordedCall
	outputs []string
}

type recordedCall struct {
	program string
	args    []string
}

func (r *fakeRunner) Run(program string, args ...string) (string, error) {
	r.calls = append(r.calls, recordedCall{program: program, args: append([]string(nil), args...)})
	if len(r.outputs) == 0 {
		return "", nil
	}
	out := r.outputs[0]
	r.outputs = r.outputs[1:]
	return out, nil
}

func (r *fakeRunner) RunInteractive(program string, args ...string) error {
	r.calls = append(r.calls, recordedCall{program: program, args: append([]string(nil), args...)})
	return nil
}

func callsEqual(got, want []recordedCall) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].program != want[i].program || !slices.Equal(got[i].args, want[i].args) {
			return false
		}
	}
	return true
}
