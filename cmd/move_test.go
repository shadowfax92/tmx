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
	}, "dev")
	want := []string{"ops", "g/review"}

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
		Target:  "dev:2",
		Session: "dev",
		Index:   2,
		Name:    "server",
	})

	fields := strings.Split(line, "\t")
	if len(fields) != 3 {
		t.Fatalf("field count = %d, want 3: %q", len(fields), line)
	}
	if fields[0] != "dev:2" {
		t.Fatalf("hidden target = %q, want dev:2", fields[0])
	}
	if !strings.HasPrefix(fields[1], "  2:server") {
		t.Fatalf("first visible field = %q, want window", fields[1])
	}
	if fields[2] != "dev" {
		t.Fatalf("second visible field = %q, want session", fields[2])
	}
}
