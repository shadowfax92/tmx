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

func TestWindowAdjustmentFormat(t *testing.T) {
	if got, want := windowAdjustmentFormat("attn_unread_count", 1), "#{e|+:#{@attn_unread_count},1}"; got != want {
		t.Fatalf("increment format = %q, want %q", got, want)
	}
	if got, want := windowAdjustmentFormat("attn_unread_count", -1), "#{?#{e|>:#{@attn_unread_count},1},#{e|-:#{@attn_unread_count},1},0}"; got != want {
		t.Fatalf("decrement format = %q, want %q", got, want)
	}
}
