// Package config owns tmx's YAML config at ~/.config/tmx/config.yaml.
//
// tmx only configures scratch popups, so the schema is a single `scratch`
// block: a TTL for idle reaping, a per-type keybind map, a per-type popup
// definition (command + size), and optional width-matched profiles that
// override sizes for specific clients (e.g. a small laptop screen).
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultTTL         = 6 * time.Hour
	defaultPopupWidth  = "90%"
	defaultPopupHeight = "90%"
)

type Config struct {
	Scratch ScratchConfig `yaml:"scratch"`
}

type ScratchConfig struct {
	TTL      Duration             `yaml:"ttl"`
	Keys     map[string]string    `yaml:"keys"`
	Popups   map[string]PopupSpec `yaml:"popups"`
	Profiles []PopupProfile       `yaml:"profiles"`
}

// PopupSpec defines one scratch type: the command to run (empty = login shell)
// and the popup's default size.
type PopupSpec struct {
	Cmd    string `yaml:"cmd"`
	Width  string `yaml:"width,omitempty"`
	Height string `yaml:"height,omitempty"`
}

type PopupSize struct {
	Width  string `yaml:"width,omitempty"`
	Height string `yaml:"height,omitempty"`
}

type PopupMatch struct {
	MinClientWidth int `yaml:"min_client_width,omitempty"`
	MaxClientWidth int `yaml:"max_client_width,omitempty"`
}

// PopupProfile overrides popup sizes for clients matching a width band (or
// selected explicitly via $TMX_PROFILE). Only sizes are per-profile; the
// command always comes from the top-level popup definition.
type PopupProfile struct {
	Name   string               `yaml:"name"`
	Match  PopupMatch           `yaml:"match,omitempty"`
	Popups map[string]PopupSize `yaml:"popups,omitempty"`
}

// Duration is a time.Duration that unmarshals from YAML strings like "6h",
// "90m", or "1d" (the day shorthand time.ParseDuration doesn't accept).
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := ParseTTL(value.Value)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

// ParseTTL parses a duration accepting time.ParseDuration units plus a "d"
// (days) suffix. Empty string yields 0 (no limit).
func ParseTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid duration %q (examples: 1h, 90m, 1d)", raw)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q (examples: 1h, 90m, 1d)", raw)
	}
	return d, nil
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "tmx", "config.yaml"), nil
}

func Load() (*Config, error) {
	path, err := DefaultConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return createDefault(path)
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	cfg.resolve()
	return &cfg, nil
}

// resolve fills defaults so callers never see zero values. A config with no
// popups configured gets working vim + shell toggles so a fresh install is
// usable immediately.
func (c *Config) resolve() {
	if c.Scratch.TTL <= 0 {
		c.Scratch.TTL = Duration(DefaultTTL)
	}
	if len(c.Scratch.Popups) == 0 {
		c.Scratch.Popups = map[string]PopupSpec{
			"vim": {Cmd: "nvim", Width: "80%", Height: "95%"},
			"sh":  {Cmd: "", Width: "90%", Height: "95%"},
		}
	}
	if len(c.Scratch.Keys) == 0 {
		c.Scratch.Keys = map[string]string{"vim": "M-v", "sh": "M-b"}
	}
}

const defaultConfigBody = `# tmx configuration — scratch popup sessions
# Docs: tmx scratch --help, tmx reap --help

scratch:
  # Kill scratch sessions idle longer than this (units: s, m, h, or d for days).
  ttl: 6h

  # Keybinds installed by 'tmx init'. Each maps a scratch type to a tmux key.
  keys:
    vim: "M-v"
    sh: "M-b"

  # Per-type popup definitions. cmd is what runs inside the popup
  # (empty cmd = a login shell). width/height size the popup.
  popups:
    vim: { cmd: nvim, width: "80%", height: "95%" }
    sh: { cmd: "", width: "90%", height: "95%" }
    # git: { cmd: lazygit, width: "90%", height: "90%" }

  # Optional: override popup sizes for clients matching a width band, or
  # selected explicitly via $TMX_PROFILE.
  # profiles:
  #   - name: laptop
  #     match: { max_client_width: 310 }
  #     popups:
  #       vim: { width: "95%", height: "95%" }
  #       sh: { width: "95%", height: "95%" }
`

func createDefault(path string) (*Config, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(defaultConfigBody), 0644); err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(defaultConfigBody), &cfg); err != nil {
		return nil, err
	}
	cfg.resolve()
	return &cfg, nil
}

// CmdFor returns the command to run for a scratch type (empty = login shell).
func (c ScratchConfig) CmdFor(typ string) string {
	return c.Popups[typ].Cmd
}

func (c ScratchConfig) HasType(typ string) bool {
	_, ok := c.Popups[typ]
	return ok
}

// Types returns the configured scratch type names, sorted.
func (c ScratchConfig) Types() []string {
	types := make([]string, 0, len(c.Popups))
	for t := range c.Popups {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// PopupFor returns the popup size for a scratch type. Resolution order
// (first non-empty wins per field): matched profile → top-level popup → 90%.
func (c ScratchConfig) PopupFor(typ string) PopupSize {
	size, _ := c.ResolvePopup(typ)
	return size
}

// ResolvePopup picks the popup size and returns the active profile name
// ("" when no profile matched).
func (c ScratchConfig) ResolvePopup(typ string) (PopupSize, string) {
	profile := c.SelectProfile(TmuxClientWidth())

	size := PopupSize{}
	if profile != nil {
		size = profile.Popups[typ]
	}
	base := c.Popups[typ]
	if size.Width == "" {
		size.Width = base.Width
	}
	if size.Height == "" {
		size.Height = base.Height
	}
	if size.Width == "" {
		size.Width = defaultPopupWidth
	}
	if size.Height == "" {
		size.Height = defaultPopupHeight
	}

	name := ""
	if profile != nil {
		name = profile.Name
	}
	return size, name
}

// SelectProfile returns the profile chosen by $TMX_PROFILE or by matching the
// given tmux client width. Returns nil when none applies.
func (c ScratchConfig) SelectProfile(clientWidth int) *PopupProfile {
	if name := strings.TrimSpace(os.Getenv("TMX_PROFILE")); name != "" {
		for i := range c.Profiles {
			if c.Profiles[i].Name == name {
				return &c.Profiles[i]
			}
		}
	}
	if clientWidth <= 0 {
		return nil
	}
	for i := range c.Profiles {
		if c.Profiles[i].Match.Matches(clientWidth) {
			return &c.Profiles[i]
		}
	}
	return nil
}

func (m PopupMatch) Matches(width int) bool {
	if m.MinClientWidth == 0 && m.MaxClientWidth == 0 {
		return false
	}
	if m.MinClientWidth > 0 && width < m.MinClientWidth {
		return false
	}
	if m.MaxClientWidth > 0 && width > m.MaxClientWidth {
		return false
	}
	return true
}

// TmuxClientWidth returns the current tmux client width in cells, or 0 if
// unavailable (not inside tmux, command failed, etc.).
func TmuxClientWidth() int {
	if os.Getenv("TMUX") == "" {
		return 0
	}
	out, err := exec.Command("tmux", "display", "-p", "#{client_width}").Output()
	if err != nil {
		return 0
	}
	w, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return w
}
