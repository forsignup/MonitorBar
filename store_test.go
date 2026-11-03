package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/binaryify/MonitorBar/internal/display"
	"github.com/binaryify/MonitorBar/internal/mediakeys"
	"github.com/binaryify/MonitorBar/internal/mitv"
	"github.com/binaryify/MonitorBar/internal/usage"
)

// testStore builds a store of the given displays, without touching the
// system or the network.
func testStore(t *testing.T, states ...*displayState) *store {
	t.Helper()
	s := &store{
		prefs:          &settings{DayScene: "day", NightScene: "night", DayStart: "07:00", NightStart: "19:00"},
		tracker:        usage.Open(filepath.Join(t.TempDir(), "usage.json")),
		redraw:         func() {},
		setGamma:       func(display.Monitor, float64, float64) error { return nil },
		soundToDisplay: func() bool { return true },
		soundOutput:    func() (string, bool) { return "", false },
		onAdjust:       func(hudKind) {},
		now:            func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local) },
		displays:       map[string]*displayState{},
		registered:     map[string]string{},
		keyHeld:        func(string) bool { return false },
	}
	s.dayStart, s.nightStart = parseClock("07:00", 0), parseClock("19:00", 0)
	for _, st := range states {
		if st.prefs == nil {
			p := defaultMonitorPrefs()
			st.prefs = &p
		}
		if st.id == "" {
			st.id = st.monitor.Identity
		}
		if st.monitor.Identity == "" {
			st.monitor.Identity = st.id
		}
		st.lastBuiltin = -1
		// Unit tests never probe the system: no scan, no DDC transaction.
		st.lanStarted, st.ddcStarted = true, true
		s.displays[st.id] = st
		s.order = append(s.order, st.id)
	}
	if len(states) > 0 {
		s.targetID = states[0].id
	}
	return s
}

// testDisplay is a state with a name and an identity, nothing else.
func testDisplay(id, name, connection string) *displayState {
	p := defaultMonitorPrefs()
	return &displayState{id: id, monitor: display.Monitor{Identity: id, Name: name, Connection: connection},
		prefs: &p, lastBuiltin: -1}
}

func TestChooseTarget(t *testing.T) {
	order := []string{"a", "b", "c"}
	for _, c := range []struct{ selected, current, want string }{
		{"b", "a", "b"}, // the last choice is back
		{"", "b", "b"},  // no choice: the fallback stays
		{"gone", "b", "b"},
		{"gone", "gone", "a"}, // nothing known: the first display
		{"", "", "a"},
	} {
		if got := chooseTarget(order, c.selected, c.current); got != c.want {
			t.Errorf("chooseTarget(%v, %q, %q) = %q, want %q", order, c.selected, c.current, got, c.want)
		}
	}
	if got := chooseTarget(nil, "a", "b"); got != "" {
		t.Errorf("chooseTarget with no displays = %q", got)
	}
}

func TestSwitchTargetChangesOnlyTheTarget(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	a.volume, a.hasVolume, a.brightness, a.hasBrightness = 40, true, 60, true
	b.volume, b.hasVolume, b.brightness, b.hasBrightness = 80, true, 30, true
	s := testStore(t, a, b)

	s.selectTarget("b")
	if s.targetID != "b" || s.prefs.Selected != "b" {
		t.Fatalf("target %q, selected %q", s.targetID, s.prefs.Selected)
	}
	s.setVolume(70)
	s.stepBrightness(5)
	if b.volume != 70 || a.volume != 40 {
		t.Errorf("volumes: a %v b %v, want a 40 b 70", a.volume, b.volume)
	}
	if b.brightness != 40 || a.brightness != 60 {
		t.Errorf("backlights: a %v b %v, want a 60 b 40", a.brightness, b.brightness)
	}
	// The software dimming is per display too.
	s.setSoftBrightness(55)
	if b.prefs.SoftBrightness != 55 || a.prefs.SoftBrightness != 100 {
		t.Errorf("software: a %v b %v", a.prefs.SoftBrightness, b.prefs.SoftBrightness)
	}
}

func TestSwitcher(t *testing.T) {
	a, b := testDisplay("a", "Mi Monitor", "HDMI"), testDisplay("b", "Second", "DP")
	// The probes are done: selecting a display must not start a scan here.
	a.lanStarted, a.ddcStarted, b.lanStarted, b.ddcStarted = true, true, true, true
	s := testStore(t, a, b)
	p := &panel{s: s, maxContent: 640}
	tt := ui.NewTester(p.view, panelWidth, 1100)
	tt.SetLocale(testLocale)
	for range 3 {
		tt.Frame()
	}
	if !tt.HasText("Mi Monitor") {
		t.Fatalf("no target name in %q", tt.Texts())
	}
	if err := tt.Click("Mi Monitor"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	menu := tt.Menu()
	if len(menu) != 2 || menu[0] != "Mi Monitor · HDMI" || menu[1] != "Second · DP" {
		t.Fatalf("switcher = %v", menu)
	}
	if err := tt.ChooseMenuItem("Second · DP"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if s.targetID != "b" {
		t.Fatalf("target %q after choosing Second", s.targetID)
	}
	if !tt.HasText("Second") || !tt.HasText("外接显示器 · DP") {
		t.Errorf("the panel still shows the other display: %q", tt.Texts())
	}
}

// One external display keeps 1.11.1's header: plain text, no switcher.
// A version 1 file upgrades to the first external display; unplugging the
// target falls back to the other one, and plugging it back selects it again
// with its own settings.
func TestRefreshMigratesAndRestoresTheTarget(t *testing.T) {
	prefs := &settings{SoftBrightness: 64, Warmth: 35, NetworkHost: "192.168.1.20",
		FollowMin: 15, FollowMax: 85, DayScene: "day", NightScene: "night",
		Monitors: map[string]*monitorPrefs{}}
	builtin := prefs.legacyPrefs()
	prefs.Monitors["builtin"] = &builtin
	a := display.Monitor{Identity: "a", Name: "Alpha", Connection: "HDMI", Vendor: 1, Model: 2, Serial: 3}
	b := display.Monitor{Identity: "b", Name: "Beta", Connection: "DP", Vendor: 1, Model: 2, Serial: 4}
	s := &store{
		prefs: prefs, displays: map[string]*displayState{}, builtinPrefs: &builtin,
		allDisplays:       func() []display.Monitor { return []display.Monitor{a, b} },
		builtinBrightness: func() (float64, bool) { return 0, true },
		targetDisplay:     func() display.Monitor { return a },
	}
	s.refreshDisplays()
	if prefs.Version != settingsVersion {
		t.Fatalf("version = %d after the migration", prefs.Version)
	}
	if got := prefs.Monitors["a"]; got == nil || got.SoftBrightness != 64 || got.Warmth != 35 || got.NetworkHost != "192.168.1.20" {
		t.Fatalf("first display = %+v", got)
	}
	if got := prefs.Monitors["b"]; got == nil || got.SoftBrightness != 100 {
		t.Fatalf("second display = %+v", got)
	}
	if s.targetID != "a" || prefs.Selected != "a" {
		t.Fatalf("target %q, selected %q", s.targetID, prefs.Selected)
	}

	// Unplug the target: the other one is shown, but the choice stays.
	s.allDisplays = func() []display.Monitor { return []display.Monitor{b} }
	s.refreshDisplays()
	if s.targetID != "b" || prefs.Selected != "a" {
		t.Fatalf("after unplugging: target %q, selected %q", s.targetID, prefs.Selected)
	}
	if s.displays["a"] != nil {
		t.Error("the unplugged display is still in the list")
	}
	if prefs.Monitors["a"].SoftBrightness != 64 {
		t.Error("the unplugged display lost its settings")
	}

	// Plug it back: it is selected again, with its settings.
	s.allDisplays = func() []display.Monitor { return []display.Monitor{a, b} }
	s.refreshDisplays()
	if s.targetID != "a" {
		t.Errorf("after plugging back: target %q", s.targetID)
	}
	if got := s.displays["a"].prefs.SoftBrightness; got != 64 {
		t.Errorf("the returning display's brightness = %v", got)
	}
}

// With no external display, the built-in one keeps the version 1 choices and
// the file stays a version 1 one; the first external display later takes
// them.
func TestBuiltinKeepsVersionOneUntilAnExternalAppears(t *testing.T) {
	prefs := &settings{SoftBrightness: 64, Warmth: 35, DayScene: "day", NightScene: "night",
		Monitors: map[string]*monitorPrefs{}}
	builtinPrefs := prefs.legacyPrefs()
	prefs.Monitors["builtin"] = &builtinPrefs
	builtinMonitor := display.Monitor{Identity: "builtin", Name: "Built-in", Builtin: true}
	external := display.Monitor{Identity: "a", Name: "Alpha", Vendor: 1, Model: 2, Serial: 3}
	s := &store{
		prefs: prefs, displays: map[string]*displayState{}, builtinPrefs: &builtinPrefs,
		allDisplays:       func() []display.Monitor { return nil },
		targetDisplay:     func() display.Monitor { return builtinMonitor },
		builtinBrightness: func() (float64, bool) { return 0, true },
	}
	s.refreshDisplays()
	if s.targetID != "builtin" || s.targetPrefs().SoftBrightness != 64 {
		t.Fatalf("target %q, brightness %v", s.targetID, s.targetPrefs().SoftBrightness)
	}
	if prefs.Version != 0 || prefs.SoftBrightness != 64 {
		t.Errorf("version %d, legacy brightness %v; want an untouched version 1 file", prefs.Version, prefs.SoftBrightness)
	}

	s.allDisplays = func() []display.Monitor { return []display.Monitor{external} }
	s.refreshDisplays()
	if prefs.Version != settingsVersion || s.targetID != "a" {
		t.Fatalf("version %d, target %q after the external display", prefs.Version, s.targetID)
	}
	if got := prefs.Monitors["a"]; got == nil || got.SoftBrightness != 64 || got.Warmth != 35 {
		t.Errorf("migrated record = %+v", got)
	}
}

func TestSwitcherMarksDetectionAndPorts(t *testing.T) {
	a, b := testDisplay("a", "Same", ""), testDisplay("b", "Same", "")
	a.monitor.Position = "37D8832A-2D66-02CA-B9F7-8F30A301B230"
	b.monitor.Position = "37D8832A-2D66-02CA-B9F7-8F30A301B231"
	a.probingDDC = true
	s := testStore(t, a, b)
	if got := s.switcherLabel("a"); got != "Same · A301B230 · 检测中" {
		t.Errorf("detecting label = %q", got)
	}
	if got := s.switcherLabel("b"); got != "Same · A301B231" {
		t.Errorf("port label = %q", got)
	}
}

// A record saved with a port key is the same panel when it is the only one
// of its EDID online: the lookup falls back to the panel's own key.
func TestUniquePanelRecordFollowsTheDisplay(t *testing.T) {
	prefs := &settings{Monitors: map[string]*monitorPrefs{
		"0001:0002:00000003@old": {SoftBrightness: 42, Warmth: 12},
	}}
	s := &store{prefs: prefs, displays: map[string]*displayState{}}
	got := s.assignPrefs([]display.Monitor{{Identity: "0001:0002:00000003"}})
	if p := got["0001:0002:00000003"]; p == nil || p.SoftBrightness != 42 || p.Warmth != 12 {
		t.Fatalf("matched %+v", p)
	}

	// Two panels of the same EDID online: one record cannot serve both.
	prefs.Monitors = map[string]*monitorPrefs{"0001:0002:00000003": {SoftBrightness: 42}}
	s = &store{prefs: prefs, displays: map[string]*displayState{}}
	got = s.assignPrefs([]display.Monitor{{Identity: "0001:0002:00000003@a"}, {Identity: "0001:0002:00000003@b"}})
	if got["0001:0002:00000003@a"] == got["0001:0002:00000003@b"] {
		t.Fatal("two same-panel displays share one record")
	}
	if got["0001:0002:00000003@a"].SoftBrightness != 42 {
		t.Errorf("the first display lost the record: %v", got["0001:0002:00000003@a"].SoftBrightness)
	}
	if got["0001:0002:00000003@b"].SoftBrightness != 100 {
		t.Errorf("the second display took the record: %v", got["0001:0002:00000003@b"].SoftBrightness)
	}
}

func TestPairingAfterAFailedProbe(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	s := testStore(t, a, b)
	s.selectTarget("b")
	b.prefs.NetworkHost = "192.168.1.10"
	b.lanFailed = true
	s.devices = []mitv.Device{{Host: "192.168.1.11", Name: "Beta"}}
	if !s.unpaired() {
		t.Error("a paired address that no longer answers can't be replaced")
	}
	if free := s.freeDevices(); len(free) != 1 || free[0].Host != "192.168.1.11" {
		t.Errorf("free devices = %v", free)
	}
	b.lanFailed = false
	if s.unpaired() {
		t.Error("a paired display is offered a new device without a failed probe")
	}
}

func TestApplyLinkRefusesAnotherDisplaysDevice(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	a.prefs.NetworkHost = "192.168.1.10"
	s := testStore(t, a, b)
	s.applyLink(b, mitv.New("192.168.1.10"), nil, 0, nil)
	if b.link != nil || !b.lanFailed {
		t.Errorf("link %v, failed %v", b.link, b.lanFailed)
	}
	if b.prefs.NetworkHost != "" {
		t.Errorf("the refused device was paired: %q", b.prefs.NetworkHost)
	}
}

func TestSwitcherNumbersSameNames(t *testing.T) {
	a, b := testDisplay("a", "Same", ""), testDisplay("b", "Same", "")
	s := testStore(t, a, b)
	if got := s.switcherLabel("a"); got != "Same · 1" {
		t.Errorf("first label = %q", got)
	}
	if got := s.switcherLabel("b"); got != "Same · 2" {
		t.Errorf("second label = %q", got)
	}
	a.monitor.Connection, b.monitor.Connection = "HDMI", "DP"
	if got := s.switcherLabel("a"); got != "Same · HDMI" || s.switcherLabel("b") != "Same · DP" {
		t.Errorf("distinct connections still numbered: %q, %q", s.switcherLabel("a"), s.switcherLabel("b"))
	}
}

func TestSingleDisplayHasNoSwitcher(t *testing.T) {
	s := testStore(t, testDisplay("a", "Only", "HDMI"))
	p := &panel{s: s, maxContent: 640}
	tt := ui.NewTester(p.view, panelWidth, 1100)
	tt.SetLocale(testLocale)
	for range 3 {
		tt.Frame()
	}
	if err := tt.Click("Only"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if menu := tt.Menu(); len(menu) != 0 {
		t.Errorf("the switcher opened with one display: %v", menu)
	}
}

func TestHUDNamesTheDisplayWithSeveral(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	b.volume, b.hasVolume = 55, true
	s := testStore(t, a, b)
	s.selectTarget("b")
	if _, title, percent := s.hudLevel(hudVolume); percent != 55 || title != "Beta · DP · 显示器音量" {
		t.Errorf("HUD = %q %v", title, percent)
	}
	// One display keeps the plain title.
	s.displays = map[string]*displayState{"a": a}
	s.order, s.prefs.Selected = []string{"a"}, ""
	s.targetID = "a"
	a.volume, a.hasVolume = 33, true
	if _, title, _ := s.hudLevel(hudVolume); title != "显示器音量" {
		t.Errorf("single-display HUD = %q", title)
	}
}

func TestAutoSwitchPerDisplay(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	for _, st := range []*displayState{a, b} {
		st.prefs.AutoSwitch = true
		st.prefs.DayScene, st.prefs.NightScene = "day", "night"
	}
	a.prefs.DayScene = "reading" // each has its own day scene
	s := testStore(t, a, b)

	s.autoCheck()
	if a.prefs.SoftBrightness != 55 || a.prefs.Warmth != 25 {
		t.Errorf("a at noon: %v / %v, want the reading scene", a.prefs.SoftBrightness, a.prefs.Warmth)
	}
	if b.prefs.SoftBrightness != 80 || b.prefs.Warmth != 0 {
		t.Errorf("b at noon: %v / %v, want the day scene", b.prefs.SoftBrightness, b.prefs.Warmth)
	}
	if a.prefs.AutoPeriod != "day" || b.prefs.AutoPeriod != "day" {
		t.Errorf("periods: a %q b %q", a.prefs.AutoPeriod, b.prefs.AutoPeriod)
	}

	// A manual change stays until the next switch, on each display.
	a.prefs.SoftBrightness = 42
	s.now = func() time.Time { return time.Date(2026, 10, 15, 20, 0, 0, 0, time.Local) }
	s.autoCheck()
	if a.prefs.SoftBrightness != 30 || b.prefs.SoftBrightness != 30 {
		t.Errorf("at night: a %v b %v, want the night scene", a.prefs.SoftBrightness, b.prefs.SoftBrightness)
	}
	if a.prefs.AutoPeriod != "night" || b.prefs.AutoPeriod != "night" {
		t.Errorf("night periods: a %q b %q", a.prefs.AutoPeriod, b.prefs.AutoPeriod)
	}

	// A display coming back whose period is stale catches up, without
	// touching the other one.
	c := testDisplay("c", "Gamma", "HDMI")
	c.prefs.AutoSwitch = true
	c.prefs.AutoPeriod = "day"
	s.displays["c"], s.order = c, append(s.order, "c")
	s.autoCheck()
	if c.prefs.SoftBrightness != 30 || c.prefs.AutoPeriod != "night" {
		t.Errorf("returning display: %v / %q", c.prefs.SoftBrightness, c.prefs.AutoPeriod)
	}
}

func TestFollowPerDisplay(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	a.hasBrightness, a.brightness, a.prefs.FollowBuiltin, a.prefs.FollowMin, a.prefs.FollowMax = true, 30, true, 10, 90
	b.hasBrightness, b.brightness, b.prefs.FollowBuiltin, b.prefs.FollowMin, b.prefs.FollowMax = true, 30, true, 20, 100
	s := testStore(t, a, b)
	s.hasBuiltin = true
	read := func() (float64, bool) { return 0.5, true }
	for range 30 {
		s.followTick(read)
	}
	if a.brightness != 50 || b.brightness != 60 {
		t.Errorf("followed to a %v b %v, want 50 and 60", a.brightness, b.brightness)
	}
	s.setBrightnessFor(a, 35) // a manual change on one display stays
	for range 3 {
		s.followTick(read)
	}
	if a.brightness != 35 || b.brightness != 60 {
		t.Errorf("after a manual change: a %v b %v", a.brightness, b.brightness)
	}
}

func TestSoundDisplayTakesTheVolumeKeys(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	a.volume, a.hasVolume = 70, true
	b.volume, b.hasVolume = 70, true
	s := testStore(t, a, b)
	s.prefs.HUD = true
	s.soundOutput = func() (string, bool) { return "Beta", true }
	step, _ := s.mediaKey(mediakeys.VolumeDown)
	if step == nil {
		t.Fatal("the volume key was left to macOS")
	}
	step()
	if b.volume != 65 || a.volume != 70 {
		t.Errorf("volumes: target a %v, sound b %v; want 70 and 65", a.volume, b.volume)
	}
	if _, title, _ := s.hudLevel(hudVolume); title != "Beta · DP · 显示器音量" {
		t.Errorf("HUD after the sound display's key = %q", title)
	}
}

func TestPairingOffersFreeDevices(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	b.lanStarted, b.ddcStarted = true, true // don't let the overview start a scan
	s := testStore(t, a, b)
	s.selectTarget("b")
	s.devices = []mitv.Device{
		{Host: "192.168.1.10", Name: "Alpha"},
		{Host: "192.168.1.11", Name: "Someone Else"},
	}
	if !s.unpaired() {
		t.Fatal("the target is paired before choosing")
	}
	free := s.freeDevices()
	if len(free) != 2 {
		t.Fatalf("free devices = %v", free)
	}
	p := &panel{s: s, maxContent: 640}
	tt := ui.NewTester(p.view, panelWidth, 1100)
	tt.SetLocale(testLocale)
	for range 3 {
		tt.Frame()
	}
	if !tt.HasText("配对局域网遥控") || !tt.HasText("Someone Else · 192.168.1.11") {
		t.Fatalf("no pairing card in %q", tt.Texts())
	}
	if err := tt.Click("Someone Else · 192.168.1.11"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if b.prefs.NetworkHost != "192.168.1.11" {
		t.Errorf("paired with %q", b.prefs.NetworkHost)
	}
	if b.link == nil || b.link.Host != "192.168.1.11" || len(b.inputs) != 0 {
		t.Errorf("link %v, inputs %v", b.link, b.inputs)
	}
	if tt.HasText("配对局域网遥控") {
		t.Error("the card stayed after pairing")
	}
}

func TestFooterSaysSoftwareOnlyWithSeveral(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	s := testStore(t, a, b)
	p := &panel{s: s, maxContent: 640}
	tt := ui.NewTester(p.view, panelWidth, 1100)
	tt.SetLocale(testLocale)
	for range 3 {
		tt.Frame()
	}
	if !tt.HasText("仅软件调光 · 没有硬件通道") {
		t.Errorf("no software-only footer in %q", tt.Texts())
	}
	// One display keeps 1.11.1's silence about software dimming.
	s.displays, s.order, s.targetID = map[string]*displayState{"a": a}, []string{"a"}, "a"
	tt = ui.NewTester(p.view, panelWidth, 1100)
	tt.SetLocale(testLocale)
	for range 3 {
		tt.Frame()
	}
	if tt.HasText("仅软件调光 · 没有硬件通道") {
		t.Error("the single display says software only")
	}
}

func TestFreeDevicesSkipsTheOtherDisplays(t *testing.T) {
	a, b := testDisplay("a", "Alpha", "HDMI"), testDisplay("b", "Beta", "DP")
	a.prefs.NetworkHost = "192.168.1.10"
	s := testStore(t, a, b)
	s.selectTarget("b")
	s.devices = []mitv.Device{{Host: "192.168.1.10", Name: "Alpha"}, {Host: "192.168.1.11", Name: "Gamma"}}
	free := s.freeDevices()
	if len(free) != 1 || free[0].Host != "192.168.1.11" {
		t.Errorf("free devices = %v", free)
	}
}
