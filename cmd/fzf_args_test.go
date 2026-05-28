package cmd

import "testing"

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
