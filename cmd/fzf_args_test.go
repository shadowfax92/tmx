package cmd

import (
	"os/exec"
	"strings"
	"testing"
)

func TestWindowFzfArgsBindWindowKeysToAbort(t *testing.T) {
	args := windowFzfArgs()

	if !hasArgPair(args, "--bind", "alt-u:abort,alt-w:abort") {
		t.Fatalf("windowFzfArgs() = %v, want Alt+u and Alt+w abort bindings", args)
	}
}

func TestPaneFzfArgsBindPaneKeyToAbort(t *testing.T) {
	args := paneFzfArgs()

	if !hasArgPair(args, "--bind", "alt-p:abort") {
		t.Fatalf("paneFzfArgs() = %v, want Alt+p abort binding", args)
	}
}

func TestSessionTreeFzfArgsBindSessionKeyToAbort(t *testing.T) {
	args := sessionTreeFzfArgs()

	if !hasArgPair(args, "--bind", "alt-s:abort") {
		t.Fatalf("sessionTreeFzfArgs() = %v, want Alt+s abort binding", args)
	}
}

func TestJumpFzfArgsIgnoreCase(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "session tree", args: sessionTreeFzfArgs()},
		{name: "window", args: windowFzfArgs()},
		{name: "pane", args: paneFzfArgs()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !hasArg(tt.args, "--ignore-case") {
				t.Fatalf("%s args = %v, want --ignore-case", tt.name, tt.args)
			}
		})
	}
}

// TestPickerSearchIsCaseInsensitive guards against re-introducing the
// --with-nth/--nth field-numbering mismatch that previously made search
// either match nothing (session tree) or only the rightmost column
// (windows/panes). Each case asserts that a lowercase query finds the
// uppercase row, which depends on both --ignore-case AND the correct
// search-field scope.
func TestPickerSearchIsCaseInsensitive(t *testing.T) {
	if _, err := exec.LookPath("fzf"); err != nil {
		t.Skip("fzf not installed")
	}

	tests := []struct {
		name  string
		args  []string
		lines []string
		query string
		want  string
	}{
		{
			name:  "session tree matches visible name",
			args:  sessionTreeFzfArgs(),
			lines: []string{"g/code\t  SETUP", "g/admin\t  ADMIN"},
			query: "setup",
			want:  "g/code",
		},
		{
			name:  "window picker matches window name (not just session)",
			args:  windowFzfArgs(),
			lines: []string{"main:5.0\t  5:SETUP-1\tg/CODE", "main:1.0\t  1:ADMIN\tg/CODE"},
			query: "setup",
			want:  "main:5.0",
		},
		{
			name:  "pane picker matches pane label (not just trailing path)",
			args:  paneFzfArgs(),
			lines: []string{"main:5.0\t  0:SETUP\t5:cli\tnvim\t~/code", "main:1.0\t  0:other\t1:ADMIN\tsh\t~"},
			query: "setup",
			want:  "main:5.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--filter", tt.query, "--delimiter", "\t"}, sanitizePreviewArgs(tt.args)...)
			cmd := exec.Command("fzf", args...)
			cmd.Stdin = strings.NewReader(strings.Join(tt.lines, "\n"))
			out, _ := cmd.Output()
			got := strings.TrimSpace(string(out))
			if !strings.HasPrefix(got, tt.want+"\t") && got != tt.want {
				t.Fatalf("fzf --filter %q over %v = %q, want match starting with %q", tt.query, tt.args, got, tt.want)
			}
		})
	}
}

// sanitizePreviewArgs strips --preview/--preview-window so the filter-mode
// test doesn't try to shell out to tmux for a fake target.
func sanitizePreviewArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--preview" || args[i] == "--preview-window" {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func hasArg(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

func hasArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
