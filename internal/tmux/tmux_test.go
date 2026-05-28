package tmux

import (
	"slices"
	"testing"
)

func TestMoveWindowArgsUseExplicitSourceAndTarget(t *testing.T) {
	got := moveWindowArgs("dev:2", "ops")
	want := []string{"move-window", "-s", "=dev:2", "-t", "=ops:"}

	if !slices.Equal(got, want) {
		t.Fatalf("move window args = %#v, want %#v", got, want)
	}
}
