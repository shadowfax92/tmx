package config

import (
	"os"
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
