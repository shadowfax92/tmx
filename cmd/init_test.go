package cmd

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"tmx/internal/config"

	"github.com/spf13/cobra"
)

func TestInitInstallsAttentionTableAndExistingBinds(t *testing.T) {
	cfg := &config.Config{
		Scratch: config.ScratchConfig{
			Keys: map[string]string{"vim": "M-v", "sh": "M-b"},
			Popups: map[string]config.PopupSpec{
				"vim": {Cmd: "nvim"},
				"sh":  {},
			},
		},
	}
	var calls [][]string
	command := newInitCommand(initDeps{
		loadConfig: func() (*config.Config, error) { return cfg, nil },
		executable: func() (string, error) { return "/opt/tmx", nil },
		bindKey: func(args ...string) error {
			calls = append(calls, slices.Clone(args))
			return nil
		},
	})

	if _, err := executeInitCommand(command); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"-n", "M-b", "run-shell", "-b", `"/opt/tmx" scratch sh "#{client_name}" "#{session_name}" "#{pane_id}" >/dev/null 2>&1 || true`},
		{"-n", "M-v", "run-shell", "-b", `"/opt/tmx" scratch vim "#{client_name}" "#{session_name}" "#{pane_id}" >/dev/null 2>&1 || true`},
		{"-n", "M-s", "display-popup", "-E", `"/opt/tmx"`},
		{"-n", "M-w", "display-popup", "-E", `"/opt/tmx" -w`},
		{"-n", "M-p", "display-popup", "-E", `"/opt/tmx" -p`},
		{"-n", "M-u", "switch-client", "-T", "attn"},
		{"-T", "attn", "-r", "u", "run-shell", `"/opt/tmx" jump`},
		{"-T", "attn", "s", "run-shell", `"/opt/tmx" snooze`},
		{"-T", "attn", "i", "run-shell", `"/opt/tmx" inbox`},
		{"-T", "attn", "d", "run-shell", `"/opt/tmx" unwatch`},
	}
	if !slices.EqualFunc(calls, want, slices.Equal[[]string]) {
		t.Fatalf("bind calls:\n%q\nwant:\n%q", calls, want)
	}
	for _, call := range calls {
		if slices.Contains(call, "M-y") {
			t.Fatalf("M-y must remain untouched: %q", call)
		}
	}
}

func TestInitNoJumpStillInstallsAttentionTable(t *testing.T) {
	var calls [][]string
	command := newInitCommand(initDeps{
		loadConfig: func() (*config.Config, error) { return &config.Config{}, nil },
		executable: func() (string, error) { return "/opt/tmx", nil },
		bindKey: func(args ...string) error {
			calls = append(calls, slices.Clone(args))
			return nil
		},
	})

	if _, err := executeInitCommand(command, "--no-jump"); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 5 {
		t.Fatalf("--no-jump bind count = %d, want the five attention binds; calls=%q", len(calls), calls)
	}
	for _, call := range calls {
		for _, oldKey := range []string{"M-s", "M-w", "M-p", "M-y"} {
			if slices.Contains(call, oldKey) {
				t.Fatalf("--no-jump unexpectedly touched %s: %q", oldKey, call)
			}
		}
	}
}

func TestInitAttnConfPrintsFragmentsWithoutBinding(t *testing.T) {
	command := newInitCommand(initDeps{
		loadConfig: func() (*config.Config, error) {
			t.Fatal("--attn-conf must not load config")
			return nil, nil
		},
		executable: func() (string, error) {
			t.Fatal("--attn-conf must not resolve the executable")
			return "", nil
		},
		bindKey: func(...string) error {
			t.Fatal("--attn-conf must not bind keys")
			return nil
		},
	})

	output, err := executeInitCommand(command, "--attn-conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#{?#{e|>:#{@attn_unread_count},0},#[fg=blue]",
		"#{?#{==:#{@attn_state},unread},#[fg=blue] [ATTN]",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("attention config missing %q:\n%s", want, output)
		}
	}
}

func executeInitCommand(command *cobra.Command, args ...string) (string, error) {
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.Execute()
	return output.String(), err
}
