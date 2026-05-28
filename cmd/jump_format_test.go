package cmd

import (
	"strings"
	"testing"

	"tmx/internal/tmux"
)

func TestFormatWindowPickerLineShowsWindowBeforeSession(t *testing.T) {
	line := formatWindowPickerLine(tmux.WindowInfo{
		Target:  "g/CODE:7",
		Session: "g/CODE",
		Index:   7,
		Name:    "tmx",
	}, true)

	fields := strings.Split(line, "\t")
	if len(fields) != 3 {
		t.Fatalf("field count = %d, want 3: %q", len(fields), line)
	}
	if fields[0] != "g/CODE:7" {
		t.Fatalf("hidden target = %q, want g/CODE:7", fields[0])
	}
	if !strings.HasPrefix(fields[1], "● 7:tmx") {
		t.Fatalf("first visible field = %q, want current-window marker and window", fields[1])
	}
	if fields[2] != "g/CODE" {
		t.Fatalf("second visible field = %q, want session", fields[2])
	}
}

func TestFormatPanePickerLineShowsPaneBeforeWindow(t *testing.T) {
	line := formatPanePickerLine(tmux.PaneInfo{
		Target:      "g/CODE:7.2",
		WindowIndex: 7,
		WindowName:  "tmx",
		PaneIndex:   2,
		Label:       "tests",
		Command:     "nvim",
	}, "~/code/clis/tmx", true)

	fields := strings.Split(line, "\t")
	if len(fields) != 5 {
		t.Fatalf("field count = %d, want 5: %q", len(fields), line)
	}
	if fields[0] != "g/CODE:7.2" {
		t.Fatalf("hidden target = %q, want g/CODE:7.2", fields[0])
	}
	if !strings.HasPrefix(fields[1], "● 2:tests") {
		t.Fatalf("first visible field = %q, want current-pane marker and pane", fields[1])
	}
	if fields[2] != "7:tmx" {
		t.Fatalf("second visible field = %q, want window", fields[2])
	}
	if fields[3] != "nvim" {
		t.Fatalf("third visible field = %q, want command", fields[3])
	}
	if fields[4] != "~/code/clis/tmx" {
		t.Fatalf("fourth visible field = %q, want path", fields[4])
	}
}
