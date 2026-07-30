package config

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseTTLAcceptsDayShorthand(t *testing.T) {
	got, err := ParseTTL("1d")
	if err != nil {
		t.Fatalf("ParseTTL() error = %v", err)
	}
	if got != 24*time.Hour {
		t.Fatalf("ParseTTL() = %v, want %v", got, 24*time.Hour)
	}
}

func TestParseTTLRejectsInvalidValue(t *testing.T) {
	if _, err := ParseTTL("nope"); err == nil {
		t.Fatal("ParseTTL() error = nil, want parse error")
	}
}

func TestDurationUnmarshalsFromYAML(t *testing.T) {
	var s struct {
		TTL Duration `yaml:"ttl"`
	}
	if err := yaml.Unmarshal([]byte("ttl: 90m\n"), &s); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if s.TTL.Duration() != 90*time.Minute {
		t.Fatalf("TTL = %v, want 90m", s.TTL.Duration())
	}
}

func TestResolveSeedsUsableDefaults(t *testing.T) {
	c := &Config{}
	c.resolve()

	if c.Scratch.TTL.Duration() != DefaultTTL {
		t.Fatalf("default TTL = %v, want %v", c.Scratch.TTL.Duration(), DefaultTTL)
	}
	if !c.Scratch.HasType("vim") || !c.Scratch.HasType("sh") {
		t.Fatalf("expected seeded vim+sh popups, got %v", c.Scratch.Types())
	}
	if c.Scratch.Keys["vim"] != "M-v" || c.Scratch.Keys["sh"] != "M-b" {
		t.Fatalf("expected seeded keys, got %v", c.Scratch.Keys)
	}
	if c.Watch.Poll.Duration() != DefaultWatchPoll {
		t.Fatalf("default watch poll = %v, want %v", c.Watch.Poll.Duration(), DefaultWatchPoll)
	}
	if c.Watch.GracePeriods != DefaultGracePeriods {
		t.Fatalf("default grace periods = %d, want %d", c.Watch.GracePeriods, DefaultGracePeriods)
	}
	if c.Watch.Period.Duration() != DefaultWatchPeriod {
		t.Fatalf("default watch period = %v, want %v", c.Watch.Period.Duration(), DefaultWatchPeriod)
	}
	if c.Watch.CaptureLines != DefaultCaptureLines {
		t.Fatalf("default capture lines = %d, want %d", c.Watch.CaptureLines, DefaultCaptureLines)
	}
	if !slices.Equal(c.Watch.Agents, []string{"claude", "codex"}) {
		t.Fatalf("default agents = %#v, want claude+codex", c.Watch.Agents)
	}
	if c.Watch.ReapTTL.Duration() != DefaultWatchReapTTL {
		t.Fatalf("default reap TTL = %v, want %v", c.Watch.ReapTTL.Duration(), DefaultWatchReapTTL)
	}
	if c.Watch.InboxZero != NotificationTmux {
		t.Fatalf("default inbox zero backend = %q, want tmux", c.Watch.InboxZero)
	}
}

func TestResolvePreservesConfiguredWatchValues(t *testing.T) {
	var c Config
	err := yaml.Unmarshal([]byte(`
watch:
  poll: 5s
  grace_periods: 4
  period: 45s
  capture_lines: 50
  agents: [aider, codex]
  reap_ttl: 2d
  inbox_zero: mac-notify
`), &c)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	c.resolve()

	if c.Watch.Poll.Duration() != 5*time.Second ||
		c.Watch.GracePeriods != 4 ||
		c.Watch.Period.Duration() != 45*time.Second ||
		c.Watch.CaptureLines != 50 ||
		!slices.Equal(c.Watch.Agents, []string{"aider", "codex"}) ||
		c.Watch.ReapTTL.Duration() != 48*time.Hour ||
		c.Watch.InboxZero != NotificationMacNotify {
		t.Fatalf("resolved watch config = %+v, want configured values", c.Watch)
	}
}

func TestNotificationBackendRejectsUnknownValue(t *testing.T) {
	var c Config
	err := yaml.Unmarshal([]byte(`
watch:
  inbox_zero: growl
`), &c)
	if err == nil || !strings.Contains(err.Error(), "invalid notification backend") {
		t.Fatalf("Unmarshal() error = %v, want invalid notification backend", err)
	}
}

func TestJumpActionUsesWidthProfile(t *testing.T) {
	var c Config
	err := yaml.Unmarshal([]byte(`
scratch:
  profiles:
    - name: laptop
      match: { max_client_width: 310 }
      jump_action: zoom
    - name: desktop
      match: { min_client_width: 311 }
      jump_action: focus
`), &c)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	action, profile := c.Scratch.JumpActionFor(200)
	if action != JumpActionZoom || profile != "laptop" {
		t.Fatalf("JumpActionFor(200) = %q (%q), want zoom (laptop)", action, profile)
	}
	action, profile = c.Scratch.JumpActionFor(400)
	if action != JumpActionFocus || profile != "desktop" {
		t.Fatalf("JumpActionFor(400) = %q (%q), want focus (desktop)", action, profile)
	}
}

func TestJumpActionRejectsUnknownValue(t *testing.T) {
	var c Config
	err := yaml.Unmarshal([]byte(`
scratch:
  profiles:
    - name: laptop
      jump_action: teleport
`), &c)
	if err == nil || !strings.Contains(err.Error(), "invalid jump_action") {
		t.Fatalf("Unmarshal() error = %v, want invalid jump_action", err)
	}
}

func TestLoadCreatesStarterConfigWithSixHourTTL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Scratch.TTL.Duration() != DefaultTTL {
		t.Fatalf("TTL = %v, want %v", cfg.Scratch.TTL.Duration(), DefaultTTL)
	}

	path, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if !strings.Contains(string(data), "ttl: 6h") {
		t.Fatalf("starter config missing ttl: 6h:\n%s", string(data))
	}
}

func TestPopupForFallsBackToNinetyPercent(t *testing.T) {
	c := ScratchConfig{Popups: map[string]PopupSpec{"vim": {Cmd: "nvim"}}}
	size := c.PopupFor("vim")
	if size.Width != "90%" || size.Height != "90%" {
		t.Fatalf("PopupFor() = %+v, want 90%%/90%%", size)
	}
}

func TestPopupForUsesConfiguredSize(t *testing.T) {
	c := ScratchConfig{Popups: map[string]PopupSpec{"vim": {Cmd: "nvim", Width: "80%", Height: "95%"}}}
	size := c.PopupFor("vim")
	if size.Width != "80%" || size.Height != "95%" {
		t.Fatalf("PopupFor() = %+v, want 80%%/95%%", size)
	}
}

func TestSelectProfilePrefersEnvOverride(t *testing.T) {
	t.Setenv("TMX_PROFILE", "laptop")
	c := ScratchConfig{Profiles: []PopupProfile{{Name: "desktop"}, {Name: "laptop"}}}
	got := c.SelectProfile(0)
	if got == nil || got.Name != "laptop" {
		t.Fatalf("SelectProfile() = %+v, want laptop", got)
	}
}

func TestSelectProfileMatchesByWidth(t *testing.T) {
	os.Unsetenv("TMX_PROFILE")
	c := ScratchConfig{Profiles: []PopupProfile{{Name: "laptop", Match: PopupMatch{MaxClientWidth: 310}}}}
	if got := c.SelectProfile(200); got == nil || got.Name != "laptop" {
		t.Fatalf("SelectProfile(200) = %+v, want laptop", got)
	}
	if got := c.SelectProfile(400); got != nil {
		t.Fatalf("SelectProfile(400) = %+v, want nil", got)
	}
}

func TestResolvePopupAppliesProfileSizeOverride(t *testing.T) {
	t.Setenv("TMX_PROFILE", "laptop")
	c := ScratchConfig{
		Popups: map[string]PopupSpec{"vim": {Cmd: "nvim", Width: "80%", Height: "85%"}},
		Profiles: []PopupProfile{{
			Name:   "laptop",
			Popups: map[string]PopupSize{"vim": {Width: "95%", Height: "95%"}},
		}},
	}
	size, name := c.ResolvePopup("vim")
	if name != "laptop" || size.Width != "95%" || size.Height != "95%" {
		t.Fatalf("ResolvePopup() = %+v (profile %q), want 95%%/95%% laptop", size, name)
	}
}
