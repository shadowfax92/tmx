package cmd

import (
	"bytes"
	"slices"
	"testing"

	"tmx/internal/config"
	"tmx/internal/sessionmux"

	"github.com/spf13/cobra"
)

func TestRunRmxDryRunPrintsMappedCommand(t *testing.T) {
	client := sessionmux.New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, nil)
	cmd, out := testRmxCommand()

	err := runRmxArgs(cmd, client, []string{"--dry-run", "new-session", "-d", "-s", "codex/feat", "-c", "/tmp/start", "launcher"})
	if err != nil {
		t.Fatalf("runRmxArgs() error = %v", err)
	}

	want := "+ tmux new-session -d -s rmx/codex/feat -c /tmp/start launcher\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestRunRmxPrintsCommandOutput(t *testing.T) {
	runner := &cmdFakeRunner{outputs: []string{"hello"}}
	client := sessionmux.New(config.SessionsConfig{Backend: "tmux", Prefix: "rmx"}, runner)
	cmd, out := testRmxCommand()

	err := runRmxArgs(cmd, client, []string{"display-message", "-p", "hello"})
	if err != nil {
		t.Fatalf("runRmxArgs() error = %v", err)
	}

	if out.String() != "hello\n" {
		t.Fatalf("output = %q, want hello newline", out.String())
	}
	want := []string{"display-message", "-p", "hello"}
	if len(runner.calls) != 1 || runner.calls[0].program != "tmux" || !slices.Equal(runner.calls[0].args, want) {
		t.Fatalf("calls = %#v, want tmux %#v", runner.calls, want)
	}
}

func testRmxCommand() (*cobra.Command, *bytes.Buffer) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	return cmd, out
}

type cmdFakeRunner struct {
	calls   []cmdRecordedCall
	outputs []string
}

type cmdRecordedCall struct {
	program string
	args    []string
}

func (r *cmdFakeRunner) Run(program string, args ...string) (string, error) {
	r.calls = append(r.calls, cmdRecordedCall{program: program, args: append([]string(nil), args...)})
	if len(r.outputs) == 0 {
		return "", nil
	}
	out := r.outputs[0]
	r.outputs = r.outputs[1:]
	return out, nil
}

func (r *cmdFakeRunner) RunInteractive(program string, args ...string) error {
	r.calls = append(r.calls, cmdRecordedCall{program: program, args: append([]string(nil), args...)})
	return nil
}
