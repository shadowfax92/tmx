package cmd

import (
	"slices"
	"testing"

	"tmx/internal/config"
)

func TestInitScratchBindingsForTmuxUseOuterContext(t *testing.T) {
	cfg := &config.Config{Scratch: config.ScratchConfig{
		Keys:   map[string]string{"vim": "M-i"},
		Popups: map[string]config.PopupSpec{"vim": {Cmd: "nvim"}},
	}}

	bindings := scratchInitBindings(cfg, `"/tmp/tmx"`, config.KeyTargetTmux)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want 1", bindings)
	}

	wantArgs := []string{
		"-n", "M-i", "run-shell", "-b",
		`"/tmp/tmx" scratch vim "#{client_name}" "#{session_name}" "#{pane_id}" >/dev/null 2>&1 || true`,
	}
	if !slices.Equal(bindings[0].args, wantArgs) {
		t.Fatalf("binding args = %#v, want %#v", bindings[0].args, wantArgs)
	}
}

func TestInitScratchBindingsForRmuxUseTrustedContext(t *testing.T) {
	cfg := &config.Config{Scratch: config.ScratchConfig{
		Keys:   map[string]string{"vim": "M-i"},
		Popups: map[string]config.PopupSpec{"vim": {Cmd: "nvim"}},
	}}

	bindings := scratchInitBindings(cfg, `"/tmp/tmx"`, config.KeyTargetRmux)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want 1", bindings)
	}

	wantArgs := []string{
		"-n", "M-i", "run-shell", "-b",
		`TMX_SCRATCH_CONTEXT=rmux "/tmp/tmx" scratch vim "#{client_name}" "#{session_name}" "#{pane_id}" >/dev/null 2>&1 || true`,
	}
	if !slices.Equal(bindings[0].args, wantArgs) {
		t.Fatalf("binding args = %#v, want %#v", bindings[0].args, wantArgs)
	}
}

func TestInitScratchBindingsForRmuxUseOverridesAndSkipEmpty(t *testing.T) {
	cfg := &config.Config{Scratch: config.ScratchConfig{
		Keys:     map[string]string{"vim": "M-i", "sh": "M-o"},
		RmuxKeys: map[string]string{"vim": "M-I", "sh": ""},
		Popups: map[string]config.PopupSpec{
			"vim": {Cmd: "nvim"},
			"sh":  {Cmd: ""},
		},
	}}

	bindings := scratchInitBindings(cfg, `"/tmp/tmx"`, config.KeyTargetRmux)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want only vim", bindings)
	}
	if bindings[0].key != "M-I" || bindings[0].typ != "vim" {
		t.Fatalf("binding = %#v, want vim on M-I", bindings[0])
	}
}

func TestInitPlanForRmuxDoesNotInstallJumpBindings(t *testing.T) {
	cfg := &config.Config{Scratch: config.ScratchConfig{
		Keys:   map[string]string{"vim": "M-i"},
		Popups: map[string]config.PopupSpec{"vim": {Cmd: "nvim"}},
	}}

	plan := initBindings(cfg, `"/tmp/tmx"`, initBindOptions{target: config.KeyTargetRmux, noJump: false})
	if len(plan) != 1 {
		t.Fatalf("plan = %#v, want only scratch binding", plan)
	}
	if plan[0].typ != "vim" {
		t.Fatalf("plan = %#v, want scratch binding only", plan)
	}
}
