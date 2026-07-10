package cmd

import (
	"strings"
	"testing"

	"tmx/internal/config"

	"github.com/spf13/cobra"
)

func TestReapHelpDescribesManualSweep(t *testing.T) {
	help := reapCmd.Long
	if strings.Contains(help, "runs automatically") {
		t.Fatalf("reap help still claims automatic toggle reaping:\n%s", help)
	}
	for _, want := range []string{
		"explicit sweep",
		"normal scratch toggles do not scan the namespace",
		"default 6h",
		"orphan + dead-cwd + idle(>ttl)",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("reap help missing %q:\n%s", want, help)
		}
	}
}

func TestReapOptionsDefaultTTLComesFromConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	opts, err := reapOptionsFromFlags(newReapOptionsTestCommand(t))
	if err != nil {
		t.Fatalf("reapOptionsFromFlags() error = %v", err)
	}
	if opts.TTL != config.DefaultTTL {
		t.Fatalf("TTL = %v, want %v", opts.TTL, config.DefaultTTL)
	}
}

func newReapOptionsTestCommand(t *testing.T) *cobra.Command {
	t.Helper()

	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().String("ttl", "", "")
	return cmd
}
