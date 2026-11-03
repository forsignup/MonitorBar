package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeSettings puts raw JSON where loadSettings reads it, in a temporary
// home directory.
func writeSettings(t *testing.T, data string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

const v1Settings = `{
  "softBrightness": 64,
  "warmth": 35,
  "networkHost": "192.168.1.23",
  "tab": "picture",
  "launchedBefore": true,
  "autoSwitch": true,
  "dayStart": "06:30",
  "nightStart": "20:00",
  "dayScene": "day",
  "nightScene": "夜间",
  "autoPeriod": "day",
  "followBuiltin": true,
  "followMin": 15,
  "followMax": 85,
  "shortcuts": {"brightnessUp": "F2"},
  "mediaKeys": true,
  "hud": false,
  "language": "zh-Hans"
}`

func TestMigrateV1(t *testing.T) {
	writeSettings(t, v1Settings)
	s := loadSettings()
	if !s.needsMigration() {
		t.Fatal("a version 1 file needs migration")
	}
	if !s.adoptLegacy("10ac:0a0a:00000000") {
		t.Fatal("the first external display took no legacy settings")
	}
	p := s.Monitors["10ac:0a0a:00000000"]
	if p == nil {
		t.Fatal("no record for the first display")
	}
	if p.SoftBrightness != 64 || p.Warmth != 35 || p.NetworkHost != "192.168.1.23" {
		t.Errorf("picture = %+v", *p)
	}
	if !p.FollowBuiltin || p.FollowMin != 15 || p.FollowMax != 85 {
		t.Errorf("follow = %+v", *p)
	}
	if !p.AutoSwitch || p.DayScene != "day" || p.NightScene != "night" || p.AutoPeriod != "day" {
		t.Errorf("auto = %+v", *p)
	}
	if s.Version != settingsVersion || s.Selected != "10ac:0a0a:00000000" {
		t.Errorf("version %d, selected %q", s.Version, s.Selected)
	}
	// Global choices stay theirs, and the old flat fields stay readable.
	if s.Tab != "picture" || s.HUD || !s.MediaKeys || s.DayStart != "06:30" || s.Shortcuts["brightnessUp"] != "F2" {
		t.Errorf("globals = %+v", s)
	}
	if s.SoftBrightness != 64 || s.NightScene != "night" {
		t.Errorf("legacy fields = %v, %q", s.SoftBrightness, s.NightScene)
	}
	if s.adoptLegacy("10ac:0a0a:00000000") || s.adoptLegacy("10ac:0a0a:00000009") {
		t.Error("the migration ran twice")
	}
}

func TestLoadedSettingsTakeRecords(t *testing.T) {
	writeSettings(t, v1Settings)
	s := loadSettings()
	if s.Monitors == nil {
		t.Fatal("a version 1 file left the monitors map nil")
	}
	p := defaultMonitorPrefs()
	s.Monitors["threaded"] = &p // the store writes the built-in record before migration
	if s.SoftBrightness != 64 {
		t.Errorf("legacy brightness = %v", s.SoftBrightness)
	}
}

func TestMigrateAMinimalV1File(t *testing.T) {
	writeSettings(t, `{"softBrightness": 40}`)
	s := loadSettings()
	if !s.adoptLegacy("a") {
		t.Fatal("a minimal version 1 file did not migrate")
	}
	p := s.Monitors["a"]
	if p.SoftBrightness != 40 || p.Warmth != 0 || p.FollowMin != 10 || p.FollowMax != 100 ||
		p.DayScene != "day" || p.NightScene != "night" {
		t.Errorf("migrated defaults = %+v", *p)
	}
	if !s.HUD || s.DayStart != "07:00" || s.NightStart != "19:00" {
		t.Errorf("global defaults = %+v", s)
	}
}

func TestMigrateOnlyOnceAcrossLaunches(t *testing.T) {
	writeSettings(t, v1Settings)
	s := loadSettings()
	s.adoptLegacy("10ac:0a0a:00000000")
	s.Monitors["10ac:0a0a:00000000"].SoftBrightness = 42 // a 2.0 change
	s.save()

	again := loadSettings()
	if again.needsMigration() {
		t.Fatal("a version 2 file still needs migration")
	}
	if again.adoptLegacy("10ac:0a0a:00000000") {
		t.Error("adoptLegacy ran on a version 2 file")
	}
	if got := again.Monitors["10ac:0a0a:00000000"].SoftBrightness; got != 42 {
		t.Errorf("brightness = %v after a second launch, want 42", got)
	}
}

func TestMigrateWaitsForAnExternalDisplay(t *testing.T) {
	writeSettings(t, v1Settings)
	s := loadSettings()
	if s.adoptLegacy("") {
		t.Fatal("the migration ran with no external display")
	}
	if !s.needsMigration() {
		t.Fatal("the file is no longer a version 1 one")
	}
	s.save()

	again := loadSettings()
	if !again.needsMigration() || again.SoftBrightness != 64 || again.NetworkHost != "192.168.1.23" {
		t.Errorf("after saving before a display appeared: %+v", again)
	}
	if !again.adoptLegacy("0001:0002:00000003") {
		t.Fatal("the first external display appearing later took no settings")
	}
	if got := again.Monitors["0001:0002:00000003"].SoftBrightness; got != 64 {
		t.Errorf("brightness = %v", got)
	}
}

func TestBrokenFileFallsBackToDefaults(t *testing.T) {
	writeSettings(t, `{
  "version": 2,
  "monitors": {
    "a": {"softBrightness": 70,`)
	// The file is cut off: the app starts with defaults rather than failing
	// to load half of it.
	s := loadSettings()
	if s.SoftBrightness != 100 || !s.HUD || s.Tab != "" {
		t.Errorf("defaults after a broken file: %+v", s)
	}
	if len(s.Monitors) != 0 {
		t.Errorf("a broken file loaded monitors: %v", s.Monitors)
	}
}

func TestV2RoundTrip(t *testing.T) {
	writeSettings(t, `{
  "version": 2,
  "selected": "a",
  "monitors": {
    "a": {"softBrightness": 70, "warmth": 10, "networkHost": "192.168.1.4", "followBuiltin": true, "followMin": 20, "followMax": 90, "autoSwitch": true, "dayScene": "reading", "nightScene": "movie", "autoPeriod": "night"},
    "b": {"softBrightness": 30, "warmth": 60}
  }
}`)
	s := loadSettings()
	p := s.monitor("a")
	if p.SoftBrightness != 70 || p.Warmth != 10 || p.NetworkHost != "192.168.1.4" ||
		!p.FollowBuiltin || p.FollowMin != 20 || p.FollowMax != 90 ||
		!p.AutoSwitch || p.DayScene != "reading" || p.NightScene != "movie" || p.AutoPeriod != "night" {
		t.Errorf("a = %+v", *p)
	}
	if b := s.monitor("b"); b.SoftBrightness != 30 || b.Warmth != 60 || b.FollowMin != 10 || b.FollowMax != 100 {
		t.Errorf("b = %+v", *b)
	}
	// A new display gets defaults, and existing records stay.
	if c := s.monitor("c"); c.SoftBrightness != 100 || c.DayScene != "day" || c.NightScene != "night" {
		t.Errorf("new display = %+v", *c)
	}
	if _, ok := s.Monitors["b"]; !ok {
		t.Error("adding a display dropped another record")
	}

	s.save()
	data, err := os.ReadFile(filepath.Join(dataDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var kept settings
	if err := json.Unmarshal(data, &kept); err != nil {
		t.Fatal(err)
	}
	if kept.Version != settingsVersion || kept.Selected != "a" || kept.Monitors["a"].SoftBrightness != 70 {
		t.Errorf("saved %+v", kept)
	}
	if kept.HUD != true {
		t.Error("a global default was lost")
	}
}

func TestMonitorDefaults(t *testing.T) {
	p := defaultMonitorPrefs()
	if p.SoftBrightness != 100 || p.Warmth != 0 || p.FollowMin != 10 || p.FollowMax != 100 ||
		p.DayScene != "day" || p.NightScene != "night" || p.FollowBuiltin || p.AutoSwitch {
		t.Errorf("defaults = %+v", p)
	}
}
