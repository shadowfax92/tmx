package tmux

import (
	"slices"
	"testing"
)

func TestMoveWindowArgsUseExplicitSourceAndTarget(t *testing.T) {
	got := moveWindowArgs("@42", "ops")
	want := []string{"move-window", "-s", "@42", "-t", "=ops:"}

	if !slices.Equal(got, want) {
		t.Fatalf("move window args = %#v, want %#v", got, want)
	}
}
