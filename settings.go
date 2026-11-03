package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// settingsVersion 2 keeps one record per monitor, keyed by its identity, and
// the global choices at the top level. Version 1 was a flat file for the one
// external display; its fields are still written so an older build can read
// the global ones after a rollback.
const settingsVersion = 2

// settings are the choices kept between launches, in a JSON file.
type settings struct {
	Version int `json:"version,omitempty"`

	// Global choices.
	Tab            string            `json:"tab"`
	LaunchedBefore bool              `json:"launchedBefore"`
	Shortcuts      map[string]string `json:"shortcuts,omitempty"`
	MediaKeys      bool              `json:"mediaKeys"`
	HUD            bool              `json:"hud"`
	Language       string            `json:"language,omitempty"`
	DayStart       string            `json:"dayStart"` // "07:00"
	NightStart     string            `json:"nightStart"`

	// Per-monitor choices, keyed by display identity.
	Selected string                   `json:"selected,omitempty"`
	Monitors map[string]*monitorPrefs `json:"monitors,omitempty"`

	// Version 1's flat per-display fields. They feed the migration, and stay
	// readable for an older build after a rollback.
	SoftBrightness float64 `json:"softBrightness"` // gamma %, 20...100
	Warmth         float64 `json:"warmth"`         // 0...100
	NetworkHost    string  `json:"networkHost"`    // the display found last time
	AutoSwitch     bool    `json:"autoSwitch"`
	DayScene       string  `json:"dayScene"`
	NightScene     string  `json:"nightScene"`
	AutoPeriod     string  `json:"autoPeriod"` // the period whose scene was applied last
	FollowBuiltin  bool    `json:"followBuiltin"`
	FollowMin      float64 `json:"followMin"`
	FollowMax      float64 `json:"followMax"`

	path string
}

// monitorPrefs are one monitor's choices: its picture, its LAN remote and
// its automatic switching.
type monitorPrefs struct {
	SoftBrightness float64 `json:"softBrightness"` // gamma %, 20...100
	Warmth         float64 `json:"warmth"`         // 0...100
	NetworkHost    string  `json:"networkHost,omitempty"`

	FollowBuiltin bool    `json:"followBuiltin"`
	FollowMin     float64 `json:"followMin"`
	FollowMax     float64 `json:"followMax"`

	AutoSwitch bool   `json:"autoSwitch"`
	DayScene   string `json:"dayScene"`
	NightScene string `json:"nightScene"`
	AutoPeriod string `json:"autoPeriod,omitempty"` // the period whose scene was applied last
}

// defaultMonitorPrefs are what a monitor starts with: full software
// brightness, no warmth, following and auto switching off.
func defaultMonitorPrefs() monitorPrefs {
	return monitorPrefs{SoftBrightness: 100, FollowMin: 10, FollowMax: 100, DayScene: "day", NightScene: "night"}
}

// legacyPrefs turns the version 1 flat fields into a monitor record.
func (s *settings) legacyPrefs() monitorPrefs {
	p := defaultMonitorPrefs()
	p.SoftBrightness, p.Warmth = s.SoftBrightness, s.Warmth
	p.NetworkHost = s.NetworkHost
	p.FollowBuiltin, p.FollowMin, p.FollowMax = s.FollowBuiltin, s.FollowMin, s.FollowMax
	p.AutoSwitch, p.DayScene, p.NightScene, p.AutoPeriod = s.AutoSwitch, s.DayScene, s.NightScene, s.AutoPeriod
	return p
}

// needsMigration reports a file from before version 2, whose flat choices
// belong to the first external display that appears.
func (s *settings) needsMigration() bool { return s.Version < settingsVersion }

// adoptLegacy gives identity the version 1 choices, once. It returns whether
// it wrote them: an empty identity (no external display yet) keeps the flat
// fields until one appears.
func (s *settings) adoptLegacy(identity string) bool {
	if !s.needsMigration() || identity == "" {
		return false
	}
	if s.Monitors == nil {
		s.Monitors = map[string]*monitorPrefs{}
	}
	if _, ok := s.Monitors[identity]; !ok {
		p := s.legacyPrefs()
		s.Monitors[identity] = &p
	}
	s.Version = settingsVersion
	s.Selected = identity
	return true
}

// monitor returns identity's choices, creating them with defaults.
func (s *settings) monitor(identity string) *monitorPrefs {
	if p := s.Monitors[identity]; p != nil {
		return p
	}
	if s.Monitors == nil {
		s.Monitors = map[string]*monitorPrefs{}
	}
	p := defaultMonitorPrefs()
	s.Monitors[identity] = &p
	return &p
}

// dataDir is where MonitorBar keeps its files.
func dataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "MonitorBar")
}

func loadSettings() *settings {
	defaults := func() *settings {
		return &settings{SoftBrightness: 100, DayStart: "07:00", NightStart: "19:00", DayScene: "day", NightScene: "night",
			FollowMin: 10, FollowMax: 100, HUD: true,
			path: filepath.Join(dataDir(), "settings.json")}
	}
	s := defaults()
	if data, err := os.ReadFile(s.path); err == nil {
		// A damaged file starts from defaults: half of an old file is not
		// better than none of it.
		if err := json.Unmarshal(data, s); err != nil {
			s = defaults()
		}
	}
	if s.Monitors == nil {
		s.Monitors = map[string]*monitorPrefs{}
	}
	if s.SoftBrightness == 0 {
		s.SoftBrightness = 100 // the version 1 default, for a file that left it out
	}
	// Before 1.7 the scenes were kept by their Chinese names.
	legacy := map[string]string{"日间": "day", "阅读": "reading", "观影": "movie", "夜间": "night"}
	for _, id := range []*string{&s.DayScene, &s.NightScene} {
		if v, ok := legacy[*id]; ok {
			*id = v
		}
	}
	// A monitor record may predate later defaults: fill those in without
	// touching what the file has.
	for _, p := range s.Monitors {
		if p == nil {
			continue
		}
		if p.SoftBrightness == 0 {
			p.SoftBrightness = 100
		}
		if p.FollowMax == 0 {
			p.FollowMin, p.FollowMax = 10, 100
		}
		if p.DayScene == "" {
			p.DayScene = "day"
		}
		if p.NightScene == "" {
			p.NightScene = "night"
		}
	}
	return s
}

func (s *settings) save() {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	_ = os.WriteFile(s.path, data, 0o644)
}
