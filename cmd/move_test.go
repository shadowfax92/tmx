package cmd

import (
	"slices"
	"strings"
	"testing"

	"tmx/internal/tmux"
)

func TestMoveTargetSessionsExcludeSourceAndScratch(t *testing.T) {
	got := moveTargetSessions([]string{
		"dev",
		"ops",
		"gs/vim/%1",
		"g/review",
	}, []tmux.WindowInfo{{Session: "dev"}})
	want := []string{"ops", "g/review"}

	if !slices.Equal(got, want) {
		t.Fatalf("move target sessions = %#v, want %#v", got, want)
	}
}

func TestMoveTargetSessionsKeepSourceSessionsForMixedSelection(t *testing.T) {
	got := moveTargetSessions([]string{
		"dev",
		"ops",
		"gs/vim/%1",
		"g/review",
	}, []tmux.WindowInfo{{Session: "dev"}, {Session: "ops"}})
	want := []string{"dev", "ops", "g/review"}

	if !slices.Equal(got, want) {
		t.Fatalf("move target sessions = %#v, want %#v", got, want)
	}
}

func TestMoveSourceWindowsExcludeScratchSessions(t *testing.T) {
	got := moveSourceWindows([]tmux.WindowInfo{
		{Target: "dev:0", Session: "dev", Index: 0, Name: "editor"},
		{Target: "gs/vim/%1:0", Session: "gs/vim/%1", Index: 0, Name: "scratch"},
		{Target: "ops:2", Session: "ops", Index: 2, Name: "logs"},
	})
	want := []tmux.WindowInfo{
		{Target: "dev:0", Session: "dev", Index: 0, Name: "editor"},
		{Target: "ops:2", Session: "ops", Index: 2, Name: "logs"},
	}

	if !slices.Equal(got, want) {
		t.Fatalf("move source windows = %#v, want %#v", got, want)
	}
}

func TestFormatMoveWindowPickerLineKeepsTargetHidden(t *testing.T) {
	line := formatMoveWindowPickerLine(tmux.WindowInfo{
		ID:      "@9",
		Target:  "dev:2",
		Session: "dev",
		Index:   2,
		Name:    "server",
	})

	fields := strings.Split(line, "\t")
	if len(fields) != 3 {
		t.Fatalf("field count = %d, want 3: %q", len(fields), line)
	}
	if fields[0] != "@9" {
		t.Fatalf("hidden target = %q, want @9", fields[0])
	}
	if !strings.HasPrefix(fields[1], "  2:server") {
		t.Fatalf("first visible field = %q, want window", fields[1])
	}
	if fields[2] != "dev" {
		t.Fatalf("second visible field = %q, want session", fields[2])
	}
}

func TestMoveWindowFzfArgsEnableMultiSelect(t *testing.T) {
	args := moveWindowFzfArgs()

	if !hasArg(args, "--multi") {
		t.Fatalf("move window fzf args = %v, want --multi", args)
	}
}

func TestMoveWindowsForTargetSkipsAlreadyThere(t *testing.T) {
	got := moveWindowsForTarget([]tmux.WindowInfo{
		{ID: "@1", Target: "dev:1", Session: "dev"},
		{ID: "@2", Target: "ops:1", Session: "ops"},
	}, "dev")
	want := []tmux.WindowInfo{
		{ID: "@2", Target: "ops:1", Session: "ops"},
	}

	if !slices.Equal(got, want) {
		t.Fatalf("move windows = %#v, want %#v", got, want)
	}
}
