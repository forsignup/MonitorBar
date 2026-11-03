package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	"github.com/binaryify/MonitorBar/internal/display"
	"github.com/binaryify/MonitorBar/internal/keystate"
	"github.com/binaryify/MonitorBar/internal/mitv"
	"github.com/binaryify/MonitorBar/internal/schedule"
	"github.com/binaryify/MonitorBar/internal/usage"
	"github.com/binaryify/MonitorBar/locales"
)

// scene sets brightness and warmth at once. Its id is kept in the
// settings, for switching by the time.
type scene struct {
	id                 string
	label              func() string
	icon               string
	brightness, warmth float64
}

var scenes = []scene{
	{"day", locales.SceneDay, "sun", 80, 0},
	{"reading", locales.SceneReading, "book", 55, 25},
	{"movie", locales.SceneMovie, "film", 100, 5},
	{"night", locales.SceneNight, "moon", 30, 55},
}

// sceneByID finds a scene, the first for an id no scene has.
func sceneByID(id string) scene {
	for _, sc := range scenes {
		if sc.id == id {
			return sc
		}
	}
	return scenes[0]
}

// displayState is one monitor's runtime state: what the panel shows and the
// channels that reach it. The settings it reads and writes live in prefs.
type displayState struct {
	id      string
	monitor display.Monitor
	prefs   *monitorPrefs

	modes       []display.Mode
	current     display.Mode
	hasCurrent  bool
	switchingTo string // the mode the LAN remote is switching the monitor to

	link      *mitv.Link // the monitor's LAN remote, nil until found
	detecting bool       // a scan is queued or running, for the switcher
	lanFailed bool       // the last scan did not reach the paired device
	inputs    []mitv.Source
	input     int // the input chosen in the app, 0 before any
	volume    float64
	hasVolume bool
	unmuted   float64 // the volume to go back to from mute, 0 when not muted

	// DDC/CI, when the monitor answers it: hardware brightness, inputs and
	// volume (when the LAN remote has none), and picture controls.
	ddc           *display.DDC
	probingDDC    bool
	hardwareError error // why DDC/CI is unavailable, nil before the probe
	brightness    float64
	hasBrightness bool
	ddcInputs     []mitv.Source
	ddcVolume     bool
	controls      []control
	maxima        map[byte]int // the range of each VCP code

	volumeTarget atomic.Int64
	deviceVolume int // the monitor's volume as last known
	chasing      bool

	// Following the built-in display: its brightness last seen (-1 before
	// any), and the backlight being eased toward.
	lastBuiltin  float64
	followTarget float64
	easing       bool

	// The probes in flight, which the UI flags don't tell apart from those
	// merely queued, and a generation that changes when the display is
	// replugged so a stale probe's result is dropped.
	ddcStarted, lanStarted bool
	gen                    int
}

// store holds what the panel shows and the actions that change it. Its
// fields are read and written on the main thread only: network work runs in
// goroutines and hands its results back through post.
type store struct {
	prefs    *settings
	tracker  *usage.Tracker
	redraw   func()                                                          // asks the open panel for a new frame
	setGamma func(monitor display.Monitor, brightness, warmth float64) error // replaced in tests

	// The monitors, by identity, in the system's order.
	displays map[string]*displayState
	order    []string
	targetID string

	// The built-in display's settings while there is no external one, and
	// the version 1 fields they come from.
	builtinPrefs *monitorPrefs

	usage         []usage.Day
	launchAtLogin bool

	// Shortcuts: those bound, by id, and the one the panel records keys for.
	bindShortcuts bool // false in tests, which have no app to bind them
	registered    map[string]string
	recording     string
	held          string                 // the shortcut whose keys are held, repeating it
	keyHeld       func(keys string) bool // whether keys are still down, fixed in tests
	// onAdjust shows the HUD for a level a key just set.
	onAdjust func(hudKind)
	// The media keys: taken where the user asked and macOS allows.
	soundToDisplay  func() bool           // whether the sound goes to a display, fixed in tests
	soundOutput     func() (string, bool) // the default output's name, when it is a display
	mediaKeysDenied bool                  // asked for, without the Accessibility permission

	notice   string
	dirty    bool          // prefs changed since the last save
	hudState *displayState // the monitor the HUD last showed

	// The day and night start times as the time inputs edit them.
	dayStart, nightStart time.Time
	now                  func() time.Time // the clock, fixed in tests
	hasBuiltin           bool

	// Updates, for builds that can update themselves.
	canUpdate  bool
	update     *mygo.Update // a newer version, nil when up to date
	installing bool
	progress   int // percent downloaded

	// The display enumeration, fixed in tests that plug and unplug monitors.
	allDisplays       func() []display.Monitor
	targetDisplay     func() display.Monitor
	builtinBrightness func() (float64, bool)

	// The LAN scan's cache and last results: one scan at a time, shared by
	// every display's probe. devices is for the panel, on the main thread.
	scanMu    sync.Mutex
	scanAt    time.Time
	scanFound []mitv.Device
	devices   []mitv.Device
}

// control is a picture setting the monitor offers over DDC/CI.
type control struct {
	code  byte
	label func() string
	value float64
	max   int
}

// controlCatalog lists the settings shown when the monitor answers them.
var controlCatalog = []struct {
	code  byte
	label func() string
}{
	{display.VCPContrast, locales.ControlContrast}, {display.VCPSharpness, locales.ControlSharpness},
	{display.VCPRedGain, locales.ControlRedGain}, {display.VCPGreenGain, locales.ControlGreenGain},
	{display.VCPBlueGain, locales.ControlBlueGain},
}

func newStore() *store {
	s := &store{
		prefs:             loadSettings(),
		tracker:           usage.Open(filepath.Join(dataDir(), "usage.json")),
		redraw:            func() {},
		now:               time.Now,
		launchAtLogin:     mygo.App.OpenAtLogin(),
		canUpdate:         mygo.Updater.Enabled(),
		registered:        map[string]string{},
		soundToDisplay:    display.SoundToDisplay,
		soundOutput:       display.SoundOutputName,
		keyHeld:           keystate.Held,
		onAdjust:          func(hudKind) {},
		displays:          map[string]*displayState{},
		allDisplays:       display.All,
		targetDisplay:     display.Target,
		builtinBrightness: display.BuiltinBrightness,
	}
	s.dayStart, s.nightStart = parseClock(s.prefs.DayStart, 7*60), parseClock(s.prefs.NightStart, 19*60)
	// Before the first external display appears, the built-in one takes the
	// version 1 flat settings; the migration copies them when one does.
	if s.prefs.needsMigration() && s.prefs.Monitors["builtin"] == nil {
		p := s.prefs.legacyPrefs()
		s.prefs.Monitors["builtin"] = &p
	}
	s.builtinPrefs = s.prefs.Monitors["builtin"]
	s.refreshDisplays()
	s.setGamma = func(m display.Monitor, b, w float64) error { return m.SetGamma(b, w) }
	for _, st := range s.stateList() {
		s.applyGammaFor(st)
	}
	s.usage = s.tracker.Recent(7)
	return s
}

// post runs fn on the main thread, then redraws.
func (s *store) post(fn func()) {
	mygo.RunOnMain(func() {
		fn()
		s.redraw()
	})
}

// MARK: Displays and the target

// targetState is the monitor the panel and the shortcuts act on. It is never
// nil: with no external display it is the built-in one, and in a store
// without any display it is an empty one the tests edit.
func (s *store) targetState() *displayState {
	if st := s.displays[s.targetID]; st != nil {
		return st
	}
	if len(s.order) > 0 {
		if st := s.displays[s.order[0]]; st != nil {
			s.targetID = st.id
			return st
		}
	}
	st := &displayState{id: "", monitor: display.Monitor{Builtin: true}, prefs: defaultPrefsPointer(), lastBuiltin: -1}
	if s.displays == nil {
		s.displays = map[string]*displayState{}
	}
	s.displays[""], s.order, s.targetID = st, []string{""}, ""
	return st
}

// defaultPrefsPointer is a monitor record for a store with no monitor.
func defaultPrefsPointer() *monitorPrefs {
	p := defaultMonitorPrefs()
	return &p
}

// targetPrefs are the target's choices.
func (s *store) targetPrefs() *monitorPrefs { return s.targetState().prefs }

// stateList returns the monitors in the system's order.
func (s *store) stateList() []*displayState {
	out := make([]*displayState, 0, len(s.order))
	for _, id := range s.order {
		if st := s.displays[id]; st != nil {
			out = append(out, st)
		}
	}
	return out
}

// multi reports more than one managed display, when the switcher shows.
func (s *store) displayCount() int { return len(s.order) }

// selectTarget makes id the monitor the panel and shortcuts act on.
func (s *store) selectTarget(id string) {
	if s.displays[id] == nil || s.targetID == id {
		return
	}
	s.targetID = id
	if st := s.displays[id]; st != nil && !st.monitor.Builtin {
		// Save an identity, not the state's defensive #n suffix for displays
		// the platform couldn't tell apart.
		s.prefs.Selected, s.dirty = monitorKey(st.monitor), true
	}
	st := s.targetState()
	s.detectState(st)
	s.redraw()
}

// displayName names a monitor: "Name · HDMI".
func (st *displayState) displayName() string {
	name := cmp.Or(st.monitor.Name, locales.MonitorUnnamed())
	if st.monitor.Connection != "" {
		return name + " · " + st.monitor.Connection
	}
	return name
}

// switcherLabel tells same-named monitors apart when the system doesn't say
// which cable reaches each: they are numbered in the system's order, the
// best a name alone can do.
func (s *store) switcherLabel(id string) string {
	st := s.displays[id]
	if st == nil {
		return ""
	}
	label := st.displayName()
	shared := 0
	for _, other := range s.stateList() {
		if other.displayName() == label {
			shared++
		}
	}
	if shared > 1 {
		// The system doesn't say which cable reaches each: the port's own
		// key is the next best thing, and the order when there is none.
		if position := shortPosition(st.monitor.Position); position != "" {
			label += " · " + position
		} else {
			label += fmt.Sprintf(" · %d", slices.Index(s.order, id)+1)
		}
	}
	if st.detecting || st.probingDDC {
		label += " · " + locales.DetectingShort()
	}
	return label
}

// shortPosition trims a platform location key to something a menu item can
// show: the last part of a UUID or device path.
func shortPosition(position string) string {
	position = strings.TrimRight(position, "/")
	if i := strings.LastIndexAny(position, "/#"); i >= 0 {
		position = position[i+1:]
	}
	if len(position) > 8 {
		position = position[len(position)-8:]
	}
	return position
}

// MARK: Picture (software, through the gamma table)

func (s *store) setSoftBrightness(percent float64) {
	st := s.targetState()
	st.prefs.SoftBrightness = percent
	s.applyGammaFor(st)
}

func (s *store) setWarmth(percent float64) {
	st := s.targetState()
	st.prefs.Warmth = percent
	s.applyGammaFor(st)
}

// apply sets a scene's brightness, on the backlight when DDC/CI reaches it,
// and its warmth.
func (s *store) apply(sc scene) { s.applyTo(s.targetState(), sc) }

func (s *store) applyTo(st *displayState, sc scene) {
	if st.hasBrightness {
		s.setBrightnessFor(st, sc.brightness)
		st.prefs.SoftBrightness = 100 // a scene's brightness is all backlight
	} else {
		st.prefs.SoftBrightness = sc.brightness
	}
	st.prefs.Warmth = sc.warmth
	s.applyGammaFor(st)
}

func (s *store) isActive(sc scene) bool { return isActiveScene(s.targetState(), sc) }

func isActiveScene(st *displayState, sc scene) bool {
	return math.Abs(effectiveBrightness(st)-sc.brightness) < 1 && math.Abs(st.prefs.Warmth-sc.warmth) < 1 &&
		(!st.hasBrightness || st.prefs.SoftBrightness >= 99.5)
}

// Combined brightness, as BetterDisplay and MonitorControl have it (#9):
// one scale over the backlight and software dimming. Above the switchover
// the backlight goes from its minimum to its maximum; below it, the
// backlight stays at its minimum and the gamma table dims, down to softMin.
const (
	switchover = 50.0
	softMin    = 20.0 // the darkest software dimming, %
)

// combine places a backlight and a software dimming, in percent, on the
// combined scale.
func combine(hardware, soft float64) float64 {
	if hardware > 0 || soft >= 100 {
		return switchover + hardware*(100-switchover)/100
	}
	return (soft - softMin) / (100 - softMin) * switchover
}

// split is the backlight and the software dimming of a combined brightness.
func split(combined float64) (hardware, soft float64) {
	if combined >= switchover {
		return (combined - switchover) / (100 - switchover) * 100, 100
	}
	return 0, softMin + max(combined, 0)/switchover*(100-softMin)
}

func (s *store) combinedBrightness() float64 { return combinedBrightnessFor(s.targetState()) }

func combinedBrightnessFor(st *displayState) float64 {
	return combine(st.brightness, st.prefs.SoftBrightness)
}

// setCombined sets the backlight and the software dimming of a combined
// brightness, writing only what changed: DDC/CI is slow.
func (s *store) setCombined(combined float64) { s.setCombinedFor(s.targetState(), combined) }

func (s *store) setCombinedFor(st *displayState, combined float64) {
	hardware, soft := split(combined)
	if hardware != st.brightness {
		s.setBrightnessFor(st, hardware)
	}
	if soft != st.prefs.SoftBrightness {
		st.prefs.SoftBrightness = soft
		s.applyGammaFor(st)
	}
}

// stepBrightness moves the brightness by delta points, on the combined
// scale when DDC/CI reaches the backlight.
func (s *store) stepBrightness(delta float64) { s.stepBrightnessFor(s.targetState(), delta) }

func (s *store) stepBrightnessFor(st *displayState, delta float64) {
	if st.hasBrightness {
		s.setCombinedFor(st, min(max(combinedBrightnessFor(st)+delta, 0), 100))
		return
	}
	st.prefs.SoftBrightness = min(max(st.prefs.SoftBrightness+delta, softMin), 100)
	s.applyGammaFor(st)
}

// effectiveBrightness is the backlight's when DDC/CI reaches it, else the
// software dimming's.
func (s *store) effectiveBrightness() float64 { return effectiveBrightness(s.targetState()) }

func effectiveBrightness(st *displayState) float64 {
	if st.hasBrightness {
		return st.brightness
	}
	return st.prefs.SoftBrightness
}

func (s *store) resetSoftware() {
	st := s.targetState()
	st.prefs.SoftBrightness, st.prefs.Warmth = 100, 0
	s.applyGammaFor(st)
}

func (s *store) applyGamma() { s.applyGammaFor(s.targetState()) }

func (s *store) applyGammaFor(st *displayState) {
	if s.setGamma == nil || st.monitor.Name == "" && st.monitor.Identity == "" {
		return
	}
	s.dirty = true
	err := s.setGamma(st.monitor, st.prefs.SoftBrightness/100, st.prefs.Warmth/100)
	switch {
	case errors.Is(err, display.ErrGammaLimited):
		s.notice = locales.NoticeGammaLimited()
	case err != nil:
		s.notice = locales.NoticeGammaFailed()
	case s.notice == locales.NoticeGammaLimited() || s.notice == locales.NoticeGammaFailed():
		s.notice = ""
	}
}

// soundDisplay is the monitor the sound goes out of, when its audio device
// carries the monitor's name; nil when macOS doesn't say which one it is.
func (s *store) soundDisplay() *displayState {
	name, onDisplay := s.soundOutput()
	if !onDisplay || name == "" {
		return nil
	}
	var found *displayState
	for _, st := range s.stateList() {
		if st.monitor.Builtin || !sameDeviceName(st.monitor.Name, name) {
			continue
		}
		if found != nil {
			return nil // two same-named monitors: macOS doesn't say which one
		}
		found = st
	}
	return found
}

// sameDeviceName compares a monitor's name with an audio device's: the
// system often drops "显示器" or spacing from the one it reports.
func sameDeviceName(monitor, output string) bool {
	a, b := deviceNameKey(monitor), deviceNameKey(output)
	if len(a) < 3 || len(b) < 3 {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

func deviceNameKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	out = strings.ReplaceAll(out, "monitor", "")
	return strings.ReplaceAll(out, "显示器", "")
}

// MARK: Network (HyperOS)

// sources are the inputs to switch between: the LAN remote's, else DDC/CI's.
func (s *store) sources() []mitv.Source { return sourcesFor(s.targetState()) }

func sourcesFor(st *displayState) []mitv.Source {
	if st.link != nil {
		return st.inputs
	}
	return st.ddcInputs
}

// inputChannel names how inputs are switched, for the inputs card.
func (s *store) inputChannel() string { return inputChannelFor(s.targetState()) }

func inputChannelFor(st *displayState) string {
	if st.link != nil {
		return locales.InputsOverLan()
	}
	return "DDC/CI"
}

// connect finds the target's display on the LAN: the host found last time
// first, then a scan.
func (s *store) connect() { s.startLAN(s.targetState()) }

// startLAN looks for the monitor's LAN remote in the background.
func (s *store) startLAN(st *displayState) {
	if st.lanStarted || st.monitor.Builtin {
		if st.monitor.Builtin {
			st.detecting = false
		}
		return
	}
	st.lanStarted, st.detecting = true, true
	m, cached, gen := st.monitor, st.prefs.NetworkHost, st.gen
	taken := s.takenHosts(st.id)
	go func() {
		link, sources, volume, volumeErr, devices := s.connectDevice(m, cached, taken)
		s.post(func() {
			if devices != nil {
				s.devices = devices
			}
			if s.displays[st.id] != st || st.gen != gen {
				if link != nil {
					go link.Close()
				}
				return
			}
			st.lanStarted, st.detecting = false, false
			s.applyLink(st, link, sources, volume, volumeErr)
		})
	}()
}

// connectDevice validates the cached host, else scans and pairs the monitor.
func (s *store) connectDevice(m display.Monitor, cached string, taken map[string]bool) (link *mitv.Link, sources []mitv.Source, volume int, volumeErr error, devices []mitv.Device) {
	if cached != "" {
		l := mitv.New(cached)
		// The address may now belong to another device, or to none: a
		// cached pairing is not trusted without a monitor answering.
		if d, ok := l.Probe(); ok && len(d.Sources) > 0 {
			v, verr := l.Volume()
			return l, d.Sources, v, verr, nil
		}
	}
	found, err := s.lanScan()
	if err != nil {
		return nil, nil, 0, nil, nil
	}
	if d := mitv.Match(m.Name, found, taken); d != nil {
		l := mitv.New(d.Host)
		v, verr := l.Volume()
		return l, d.Sources, v, verr, found
	}
	return nil, nil, 0, nil, found
}

// applyLink takes the LAN probe's result on the main thread.
func (s *store) applyLink(st *displayState, link *mitv.Link, sources []mitv.Source, volume int, volumeErr error) {
	if link != nil {
		// Two probes can run at once; one device belongs to one display.
		for id, other := range s.displays {
			if id == st.id || other.prefs == nil {
				continue
			}
			if other.prefs.NetworkHost == link.Host {
				go link.Close()
				link = nil
				break
			}
		}
	}
	st.link, st.lanFailed = link, link == nil
	if link == nil {
		st.inputs = nil
		st.hasVolume = st.ddcVolume // DDC/CI may still have volume
		return
	}
	st.prefs.NetworkHost, s.dirty = link.Host, true
	if st.prefs == s.builtinPrefs {
		s.syncLegacy()
	}
	slices.SortFunc(sources, func(a, b mitv.Source) int { return a.ID - b.ID })
	st.inputs = sources
	st.hasVolume = st.ddcVolume // until the remote answers, DDC/CI is the fallback
	if volumeErr == nil {
		st.deviceVolume, st.volume, st.hasVolume = volume, float64(volume), true
	}
}

// lanScan scans once for every display probing at the same time.
func (s *store) lanScan() ([]mitv.Device, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if len(s.scanFound) > 0 && time.Since(s.scanAt) < 5*time.Second {
		return s.scanFound, nil
	}
	found, err := mitv.DiscoverAll()
	if err != nil {
		return nil, err
	}
	s.scanFound, s.scanAt = found, time.Now()
	return found, nil
}

// takenHosts are the addresses already paired with another display.
func (s *store) takenHosts(except string) map[string]bool {
	taken := map[string]bool{}
	for id, st := range s.displays {
		if id == except || st.prefs == nil {
			continue
		}
		if st.prefs.NetworkHost != "" {
			taken[st.prefs.NetworkHost] = true
		}
	}
	return taken
}

// freeDevices lists the LAN remotes no other display uses.
func (s *store) freeDevices() []mitv.Device {
	taken := s.takenHosts(s.targetID)
	var out []mitv.Device
	for _, d := range s.devices {
		if !taken[d.Host] {
			out = append(out, d)
		}
	}
	return out
}

// unpaired reports the target has no LAN remote chosen yet: a pairing card
// may help when the network has devices.
func (s *store) unpaired() bool {
	st := s.targetState()
	if st.link != nil || st.monitor.Builtin {
		return false
	}
	// A paired address that no longer answers is worth replacing, when the
	// network shows another device; a monitor simply unplugged has nothing
	// to offer and keeps its pairing.
	return st.prefs.NetworkHost == "" || st.lanFailed
}

// pair binds the target to a device chosen by the user. A device the last
// scan saw is taken as it is: the panel updates at once, and the volume is
// read when it opens.
func (s *store) pair(host string) {
	st := s.targetState()
	for _, d := range s.devices {
		if d.Host == host {
			st.lanStarted, st.detecting = false, false
			s.applyLink(st, mitv.New(host), d.Sources, 0, errVolumeUnread)
			return
		}
	}
	st.prefs.NetworkHost, s.dirty = host, true
	st.link, st.inputs, st.hasVolume = nil, nil, false
	st.lanStarted = false
	s.startLAN(st)
}

// errVolumeUnread says a pairing happened before the volume was read.
var errVolumeUnread = errors.New("store: the volume was not read yet")

func (s *store) selectInput(src mitv.Source) { s.selectInputFor(s.targetState(), src) }

func (s *store) selectInputFor(st *displayState, src mitv.Source) {
	st.input = src.ID
	link := st.link
	if link == nil {
		if st.ddc != nil {
			st.ddc.Set(display.VCPInput, src.ID)
		}
		return
	}
	go func() {
		if err := link.Select(src.ID); err != nil {
			s.post(func() { s.notice = locales.NoticeInputFailed(src.Name) })
		}
	}()
}

// stepVolume moves the volume by delta points, when it can be set.
func (s *store) stepVolume(delta float64) { s.stepVolumeFor(s.targetState(), delta) }

func (s *store) stepVolumeFor(st *displayState, delta float64) {
	if st.hasVolume {
		s.setVolumeFor(st, min(max(st.volume+delta, 0), 100))
	}
}

// toggleMute takes the volume to 0, or back to what it was: neither the LAN
// remote nor every DDC/CI display has a mute.
func (s *store) toggleMute() { s.toggleMuteFor(s.targetState()) }

func (s *store) toggleMuteFor(st *displayState) {
	if !st.hasVolume {
		return
	}
	if st.volume > 0 {
		st.unmuted = st.volume
		s.setVolumeFor(st, 0)
		return
	}
	s.setVolumeFor(st, cmp.Or(st.unmuted, defaultUnmuted))
}

const defaultUnmuted = 20 // the volume unmuting goes to, when it was muted elsewhere

func (s *store) setVolume(percent float64) { s.setVolumeFor(s.targetState(), percent) }

func (s *store) setVolumeFor(st *displayState, percent float64) {
	if percent > 0 {
		st.unmuted = 0
	}
	st.volume = percent
	if st.link == nil {
		s.setDDCFor(st, display.VCPVolume, percent)
		return
	}
	st.volumeTarget.Store(int64(math.Round(percent)))
	s.chaseVolume(st)
}

// chaseVolume steps toward the latest target: the API only has ±1 keys.
func (s *store) chaseVolume(st *displayState) {
	if st.link == nil || st.chasing {
		return
	}
	st.chasing = true
	link, actual := st.link, st.deviceVolume
	go func() {
		failed := false
		for target := int(st.volumeTarget.Load()); target != actual; target = int(st.volumeTarget.Load()) {
			key, step := "volumeup", 1
			if target < actual {
				key, step = "volumedown", -1
			}
			if link.Press(key) != nil {
				failed = true
				break
			}
			actual += step
		}
		s.post(func() {
			st.chasing = false
			st.deviceVolume = actual
			switch {
			case failed:
				s.notice = locales.NoticeVolumeFailed()
			case int(st.volumeTarget.Load()) != actual:
				s.chaseVolume(st) // the slider moved while the last step was sent
			default:
				s.readVolumeFor(st)
			}
		})
	}()
}

func (s *store) readVolume() { s.readVolumeFor(s.targetState()) }

func (s *store) readVolumeFor(st *displayState) {
	link := st.link
	if link == nil {
		return
	}
	go func() {
		v, err := link.Volume()
		if err != nil {
			return
		}
		s.post(func() {
			if s.displays[st.id] != st {
				return
			}
			st.deviceVolume, st.hasVolume = v, true
			if !st.chasing {
				st.volume = float64(v)
			}
		})
	}()
}

// MARK: DDC/CI

// probeDDC looks for DDC/CI on the target, in the background. A monitor
// that doesn't answer simply gets no DDC controls.
func (s *store) probeDDC() { s.startDDC(s.targetState()) }

func (s *store) startDDC(st *displayState) {
	if st.ddcStarted || st.monitor.Builtin {
		if st.monitor.Builtin {
			st.probingDDC = false
		}
		return
	}
	st.ddcStarted, st.probingDDC = true, true
	if old := st.ddc; old != nil {
		st.ddc = nil
		go old.Close()
	}
	m, gen := st.monitor, st.gen
	go func() {
		d, err := m.OpenDDC()
		var r ddcReading
		if err == nil {
			r = readDDC(d, m.Name, true)
		}
		s.post(func() {
			if s.displays[st.id] != st || st.gen != gen {
				if d != nil {
					go d.Close()
				}
				return
			}
			st.ddcStarted, st.probingDDC = false, false
			st.hardwareError = err
			st.ddc = d
			s.applyDDCFor(st, r)
			s.autoCheck()
		})
	}()
}

// probeJob is one non-target display's pending probe, taken on the main
// thread so the background goroutine doesn't read state it could change.
type probeJob struct {
	st      *displayState
	gen     int
	monitor display.Monitor
	cached  string
	taken   map[string]bool
	ddc     bool
	lan     bool
}

// probeStateSync looks for a non-target's channels on its own goroutine, one
// display after another: DDC/CI is slow, and the panel stays responsive.
func (s *store) probeStateSync(j probeJob) {
	st := j.st
	if j.ddc {
		d, err := j.monitor.OpenDDC()
		var r ddcReading
		if err == nil {
			r = readDDC(d, j.monitor.Name, true)
		}
		s.post(func() {
			if s.displays[st.id] != st || st.gen != j.gen {
				if d != nil {
					go d.Close()
				}
				return
			}
			st.ddcStarted, st.probingDDC = false, false
			st.hardwareError, st.ddc = err, d
			s.applyDDCFor(st, r)
			s.autoCheck()
		})
	}
	if j.lan {
		link, sources, volume, volumeErr, devices := s.connectDevice(j.monitor, j.cached, j.taken)
		s.post(func() {
			if devices != nil {
				s.devices = devices
			}
			if s.displays[st.id] != st || st.gen != j.gen {
				if link != nil {
					go link.Close()
				}
				return
			}
			st.lanStarted, st.detecting = false, false
			s.applyLink(st, link, sources, volume, volumeErr)
		})
	}
}

// ddcReading is what the monitor reported over DDC/CI.
type ddcReading struct {
	values    map[byte][2]int // value and maximum of each code that answered
	inputs    []mitv.Source
	refreshed bool // values only: inputs and controls were not read again
}

// readDDC reads the values the panel shows; with all, also the inputs the
// capability string lists and which picture controls answer. It blocks.
func readDDC(d *display.DDC, model string, all bool) ddcReading {
	r := ddcReading{values: map[byte][2]int{}, refreshed: !all}
	codes := []byte{display.VCPBrightness, display.VCPVolume, display.VCPInput}
	if all {
		for _, c := range controlCatalog {
			codes = append(codes, c.code)
		}
		for _, id := range display.ParseCapabilities(d.Capabilities())[display.VCPInput] {
			r.inputs = append(r.inputs, mitv.Source{ID: id, Name: display.InputNameFor(model, id)})
		}
	}
	for _, code := range codes {
		if v, max, ok := d.Get(code); ok && max > 0 {
			r.values[code] = [2]int{v, max}
		}
	}
	return r
}

func (s *store) applyDDC(r ddcReading) { s.applyDDCFor(s.targetState(), r) }

func (s *store) applyDDCFor(st *displayState, r ddcReading) {
	if st.ddc == nil {
		st.hasBrightness, st.ddcVolume, st.ddcInputs, st.controls = false, false, nil, nil
		return
	}
	if st.maxima == nil {
		st.maxima = map[byte]int{}
	}
	for code, v := range r.values {
		st.maxima[code] = v[1]
	}
	percent := func(code byte) (float64, bool) {
		v, ok := r.values[code]
		return float64(v[0]) * 100 / float64(v[1]), ok
	}
	if v, ok := percent(display.VCPBrightness); ok {
		st.brightness, st.hasBrightness = v, true
	}
	if v, ok := percent(display.VCPVolume); ok {
		st.ddcVolume = true
		if st.link == nil && !st.chasing {
			st.volume, st.hasVolume = v, true
		}
	}
	if r.refreshed {
		s.applyInputFor(st, r)
		for i := range st.controls {
			if v, ok := r.values[st.controls[i].code]; ok {
				st.controls[i].value = float64(v[0])
			}
		}
		return
	}
	st.ddcInputs = r.inputs
	s.applyInputFor(st, r)
	st.controls = nil
	for _, c := range controlCatalog {
		if v, ok := r.values[c.code]; ok {
			st.controls = append(st.controls, control{code: c.code, label: c.label, value: float64(v[0]), max: v[1]})
		}
	}
}

// applyInput highlights the input the monitor reports, when it is one of
// those it lists. Some report something else (issue #2: 8 where it lists
// 0x0F 0x10 0x11 0x12): the input chosen in the app stays highlighted then.
func (s *store) applyInput(r ddcReading) { s.applyInputFor(s.targetState(), r) }

func (s *store) applyInputFor(st *displayState, r ddcReading) {
	v, ok := r.values[display.VCPInput]
	if !ok || st.link != nil {
		return
	}
	code := v[0] & 0xFF // some monitors put more in the high byte
	for _, src := range st.ddcInputs {
		if src.ID == code {
			st.input = code
		}
	}
}

// refreshDDC rereads the values the monitor's own buttons may have changed.
func (s *store) refreshDDC() { s.refreshDDCFor(s.targetState()) }

func (s *store) refreshDDCFor(st *displayState) {
	d := st.ddc
	if d == nil || st.probingDDC {
		return
	}
	model := st.monitor.Name
	go func() {
		r := readDDC(d, model, false)
		s.post(func() {
			if s.displays[st.id] == st && st.ddc == d {
				s.applyDDCFor(st, r)
			}
		})
	}()
}

// setBrightness sets the backlight over DDC/CI.
func (s *store) setBrightness(percent float64) { s.setBrightnessFor(s.targetState(), percent) }

func (s *store) setBrightnessFor(st *displayState, percent float64) {
	st.brightness = percent
	s.setDDCFor(st, display.VCPBrightness, percent)
}

// setDDC writes a percentage scaled to the code's range.
func (s *store) setDDC(code byte, percent float64) { s.setDDCFor(s.targetState(), code, percent) }

func (s *store) setDDCFor(st *displayState, code byte, percent float64) {
	if st.ddc == nil {
		return
	}
	max := st.maxima[code]
	if max == 0 {
		max = 100
	}
	st.ddc.Set(code, int(math.Round(percent/100*float64(max))))
}

func (s *store) setControl(i int) { s.setControlFor(s.targetState(), i) }

func (s *store) setControlFor(st *displayState, i int) {
	if st.ddc != nil && i >= 0 && i < len(st.controls) {
		st.ddc.Set(st.controls[i].code, int(math.Round(st.controls[i].value)))
	}
}

// MARK: Day and night

// autoCheck applies the day or night scene to every monitor that switches by
// the time, when the period changed since its last one, so manual changes
// stay until the next switch.
func (s *store) autoCheck() {
	period, _ := s.autoTimes().At(s.now())
	for _, st := range s.stateList() {
		// Until DDC/CI has answered, a scene's brightness would be software
		// dimming instead of the backlight; the probe calls autoCheck again.
		if st.probingDDC || !st.prefs.AutoSwitch || string(period) == st.prefs.AutoPeriod {
			continue
		}
		id := st.prefs.DayScene
		if period == schedule.Night {
			id = st.prefs.NightScene
		}
		s.applyTo(st, sceneByID(id))
		st.prefs.AutoPeriod, s.dirty = string(period), true
	}
}

// autoChanged takes new settings: the current period's scenes apply now.
func (s *store) autoChanged() {
	s.prefs.DayStart, s.prefs.NightStart = s.dayStart.Format("15:04"), s.nightStart.Format("15:04")
	for _, st := range s.stateList() {
		st.prefs.AutoPeriod = ""
	}
	s.dirty = true
	s.autoCheck()
}

func (s *store) autoTimes() schedule.Times {
	return schedule.Times{
		DayStart:   s.dayStart.Hour()*60 + s.dayStart.Minute(),
		NightStart: s.nightStart.Hour()*60 + s.nightStart.Minute(),
	}
}

// autoStatus says what is on and what comes next for the target.
func (s *store) autoStatus() string {
	st := s.targetState()
	period, next := s.autoTimes().At(s.now())
	if period == schedule.Day {
		return locales.AutoNowDay(next, sceneByID(st.prefs.NightScene).label())
	}
	return locales.AutoNowNight(next, sceneByID(st.prefs.DayScene).label())
}

// parseClock reads "15:04" into a time of day, or the fallback minutes.
func parseClock(text string, fallback int) time.Time {
	t, err := time.Parse("15:04", text)
	if err != nil {
		t = time.Date(0, 1, 1, fallback/60, fallback%60, 0, 0, time.UTC)
	}
	return t
}

// MARK: Following the built-in display

// canFollow tells whether following the built-in display is possible: a
// MacBook's display to follow, and a backlight to set over DDC/CI.
func (s *store) canFollow() bool { return s.canFollowFor(s.targetState()) }

func (s *store) canFollowFor(st *displayState) bool {
	return s.hasBuiltin && st.hasBrightness && !st.monitor.Builtin
}

// following reports any monitor set to follow the built-in display.
func (s *store) following() bool {
	for _, st := range s.stateList() {
		if st.prefs.FollowBuiltin && s.canFollowFor(st) {
			return true
		}
	}
	return false
}

// followTick runs every second. A change of the built-in display's
// brightness sets a new target, which each backlight eases toward; between
// changes, a manual change of the backlight stays.
func (s *store) followTick(builtin func() (float64, bool)) {
	b, ok := builtin()
	if !ok {
		return
	}
	for _, st := range s.stateList() {
		if st.prefs.FollowBuiltin && s.canFollowFor(st) {
			s.followState(st, b)
		}
	}
}

func (s *store) followState(st *displayState, b float64) {
	if st.lastBuiltin < 0 || math.Abs(b-st.lastBuiltin) >= 0.005 {
		st.lastBuiltin = b
		st.followTarget, st.easing = followTarget(b, st.prefs.FollowMin, st.prefs.FollowMax), true
	}
	if !st.easing {
		return
	}
	if st.prefs.SoftBrightness < 100 { // following sets the backlight alone, with no software dimming
		st.prefs.SoftBrightness = 100
		s.applyGammaFor(st)
	}
	next := easeToward(st.brightness, st.followTarget)
	if math.Round(next) != math.Round(st.brightness) {
		s.setBrightnessFor(st, next)
	} else {
		st.brightness = next
	}
	if next == st.followTarget {
		st.easing = false
	}
}

// followChanged restarts following from the built-in display's brightness.
func (s *store) followChanged() {
	for _, st := range s.stateList() {
		st.lastBuiltin = -1
	}
	s.dirty = true
}

// followTarget maps the built-in display's brightness, 0...1, into the
// external backlight's range, in percent.
func followTarget(builtin, lo, hi float64) float64 {
	return lo + (hi-lo)*min(max(builtin, 0), 1)
}

// easeToward moves a third of the way to the target each second, at least
// one point, landing on it: a few seconds for any change, without jumps.
func easeToward(current, target float64) float64 {
	d := target - current
	if math.Abs(d) <= 1 {
		return target
	}
	step := d * 0.3
	if math.Abs(step) < 1 {
		step = math.Copysign(1, d)
	}
	return current + step
}

// MARK: Updates

// checkUpdate asks the releases for a newer version, in the background;
// manual checks also tell when there is none.
func (s *store) checkUpdate(manual bool) {
	if !s.canUpdate || s.installing {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		up, err := mygo.Updater.Check(ctx)
		s.post(func() {
			if err == nil {
				s.update = up
			}
			switch {
			case !manual:
			case err != nil:
				s.notice = locales.NoticeUpdateCheckFailed()
			case up == nil:
				s.notice = locales.NoticeUpToDate(mygo.App.Version())
			}
		})
	}()
}

// installUpdate downloads the update, checks its signature, replaces the
// app and starts the new version.
func (s *store) installUpdate() {
	up := s.update
	if up == nil || s.installing {
		return
	}
	s.installing, s.progress, s.notice = true, 0, ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		shown := -1
		err := up.Install(ctx, func(done, total int64) {
			if total <= 0 {
				return
			}
			if p := int(done * 100 / total); p != shown {
				shown = p
				s.post(func() { s.progress = p })
			}
		})
		s.post(func() {
			if err != nil {
				s.installing, s.notice = false, locales.NoticeUpdateFailed()
				return
			}
			s.save()
			mygo.App.Relaunch()
		})
	}()
}

// MARK: Display modes

func (s *store) applyMode(m display.Mode) {
	st := s.targetState()
	if err := st.monitor.Apply(m); err != nil {
		s.notice = locales.NoticeModeRejected()
	} else {
		s.notice = locales.NoticeModeSwitched(m.Title(), m.RefreshRate)
	}
	s.refreshState(st)
}

// refreshState rereads a monitor's modes and current mode.
func (s *store) refreshState(st *displayState) {
	st.modes = st.monitor.Modes()
	st.current, st.hasCurrent = st.monitor.CurrentMode()
}

// competitive reports the monitor in its 1080p 320Hz mode: only then does
// the system offer rates above the 160Hz of 4K.
func (s *store) competitive() bool {
	st := s.targetState()
	return slices.ContainsFunc(st.modes, func(m display.Mode) bool { return m.RefreshRate > 160 })
}

// setCompetitive switches the monitor between 4K 160Hz and 1080p 320Hz
// through the LAN remote. The system follows once the monitor offers its
// new EDID, some seconds later.
func (s *store) setCompetitive(on bool) {
	st := s.targetState()
	link := st.link
	if link == nil || st.switchingTo != "" || on == s.competitive() {
		return
	}
	st.switchingTo = "4K 160Hz"
	if on {
		st.switchingTo = "1080p 320Hz"
	}
	s.notice = ""
	go func() {
		err := link.SetCompetitive(on)
		s.post(func() {
			if err != nil {
				s.notice = locales.NoticeSwitchFailed()
			} else {
				s.notice = locales.NoticeSwitching(st.switchingTo)
			}
			st.switchingTo = ""
		})
	}()
}

func (s *store) sleepDisplay() { _ = display.SleepNow() }

// MARK: Lifecycle

// monitorKey is the identity a monitor's settings are kept under: its own,
// or the panel's when the platform gave none.
func monitorKey(m display.Monitor) string {
	if m.Identity != "" {
		return m.Identity
	}
	if m.Builtin {
		return "builtin"
	}
	return m.BaseIdentity()
}

// assignPrefs gives each online monitor the record it is: the exact identity,
// else the only saved record of the same panel (a display that was saved with
// a port key while another of its model was online). Displays without one get
// defaults. One saved record is never given to two online displays.
func (s *store) assignPrefs(monitors []display.Monitor) map[string]*monitorPrefs {
	out := map[string]*monitorPrefs{}
	used := map[string]bool{}
	for _, m := range monitors {
		key := monitorKey(m)
		if m.Builtin {
			if s.builtinPrefs == nil {
				if p := s.prefs.Monitors[key]; p != nil {
					s.builtinPrefs = p
				} else {
					p := s.prefs.monitor(key)
					s.builtinPrefs, s.dirty = p, true
				}
			}
			out[key] = s.builtinPrefs
			continue
		}
		if p := s.prefs.Monitors[key]; p != nil && !used[key] {
			out[key], used[key] = p, true
			continue
		}
		var saved []string
		for id := range s.prefs.Monitors {
			if !used[id] {
				saved = append(saved, id)
			}
		}
		if match := display.MatchIdentity(key, saved); match != "" {
			out[key] = s.prefs.Monitors[match]
			used[match] = true
			continue
		}
		p := s.prefs.monitor(key)
		out[key], used[key], s.dirty = p, true, true
	}
	return out
}

// refreshDisplays rebuilds the monitor list after a display was plugged,
// unplugged or woke: states stay for the monitors still online, and the
// target follows the user's choice when it is back.
func (s *store) refreshDisplays() {
	all, target, builtinBrightness := s.allDisplays, s.targetDisplay, s.builtinBrightness
	if all == nil {
		all = display.All
	}
	if target == nil {
		target = display.Target
	}
	if builtinBrightness == nil {
		builtinBrightness = display.BuiltinBrightness
	}
	_, s.hasBuiltin = builtinBrightness()
	monitors := all()
	external := len(monitors) > 0
	if !external {
		monitors = []display.Monitor{target()}
		s.syncLegacy()
	}
	if external && s.prefs.needsMigration() {
		s.syncLegacy()
		if s.prefs.adoptLegacy(monitors[0].Identity) {
			s.dirty = true
			s.builtinPrefs = s.prefs.Monitors["builtin"]
		}
	}
	assigned := s.assignPrefs(monitors)
	next := map[string]*displayState{}
	var order []string
	for _, m := range monitors {
		key := monitorKey(m)
		id := key
		if _, dup := next[id]; dup {
			// Two displays the platform could not tell apart: keep their
			// states apart, but they share the only settings record there is.
			id = fmt.Sprintf("%s#%d", id, len(order))
		}
		st := s.displays[id]
		if st == nil {
			st = &displayState{id: id, probingDDC: !m.Builtin, detecting: !m.Builtin, lastBuiltin: -1}
		}
		changed := !st.monitor.Same(m)
		st.id, st.monitor = id, m
		st.prefs = assigned[key]
		// Modes can change without the handle changing (a resolution changed
		// elsewhere); a refresh is rare enough to reread them.
		st.modes = m.Modes()
		st.current, st.hasCurrent = m.CurrentMode()
		if changed {
			st.gen++
			st.ddcStarted, st.lanStarted = false, false
			st.probingDDC, st.detecting = !m.Builtin, !m.Builtin
		}
		next[id], order = st, append(order, id)
	}
	for id, old := range s.displays {
		if next[id] == old {
			continue
		}
		if old.ddc != nil {
			go old.ddc.Close()
		}
	}
	s.displays, s.order = next, order
	s.targetID = chooseTarget(order, s.prefs.Selected, s.targetID)
}

// chooseTarget picks the monitor the panel opens on: the one the user last
// chose when it is online, else the current fallback when it still is, else
// the first in the system's order. The saved choice is not overwritten by a
// fallback: the monitor comes back selected.
func chooseTarget(order []string, selected, current string) string {
	for _, id := range order {
		if id == selected {
			return selected
		}
	}
	for _, id := range order {
		if id == current {
			return current
		}
	}
	if len(order) > 0 {
		return order[0]
	}
	return ""
}

// syncLegacy copies the built-in display's choices into the version 1 flat
// fields, which an older build still reads after a rollback.
func (s *store) syncLegacy() {
	if s.builtinPrefs == nil {
		return
	}
	p := s.builtinPrefs
	s.prefs.SoftBrightness, s.prefs.Warmth = p.SoftBrightness, p.Warmth
	s.prefs.NetworkHost = p.NetworkHost
	s.prefs.FollowBuiltin, s.prefs.FollowMin, s.prefs.FollowMax = p.FollowBuiltin, p.FollowMin, p.FollowMax
	s.prefs.AutoSwitch, s.prefs.DayScene, s.prefs.NightScene, s.prefs.AutoPeriod =
		p.AutoSwitch, p.DayScene, p.NightScene, p.AutoPeriod
}

// displaysChanged follows plugging, mode changes and wake: the gamma table
// was reset and AV services may be stale, so capture and bind them again.
func (s *store) displaysChanged() {
	for _, st := range s.stateList() {
		display.ResetGammaBase(st.monitor)
	}
	display.ResetDDCBinding()
	s.refreshDisplays()
	for _, st := range s.stateList() {
		s.applyGammaFor(st)
	}
	s.detect()
	// A display that comes back whose last period is the other one catches
	// up at once, not at the next timer tick.
	s.autoCheck()
}

func (s *store) panelOpened() {
	s.usage = s.tracker.Recent(7)
	st := s.targetState()
	// Retry here too: the launch attempt may have raced the network
	// permission prompt.
	if st.link == nil && !st.monitor.Builtin {
		s.startLAN(st)
	} else {
		s.readVolumeFor(st)
	}
	s.refreshDDCFor(st)
}

// detect looks for every control channel again: the target at once, the
// other displays serially in the background.
func (s *store) detect() {
	target := s.targetState()
	s.detectState(target)
	var jobs []probeJob
	for _, id := range s.order {
		if id == target.id {
			continue
		}
		st := s.displays[id]
		if st == nil || st.monitor.Builtin {
			continue
		}
		j := probeJob{st: st, gen: st.gen, monitor: st.monitor, cached: st.prefs.NetworkHost, taken: s.takenHosts(st.id)}
		if !st.ddcStarted {
			st.ddcStarted, st.probingDDC, j.ddc = true, true, true
		}
		if !st.lanStarted {
			st.lanStarted, st.detecting, j.lan = true, true, true
		}
		if j.ddc || j.lan {
			jobs = append(jobs, j)
		}
	}
	go func() {
		for _, j := range jobs {
			s.probeStateSync(j)
		}
	}()
}

// detectState looks for one display's channels, without blocking the panel.
func (s *store) detectState(st *displayState) {
	s.startLAN(st)
	s.startDDC(st)
}

func (s *store) recordUsage() {
	s.autoCheck()
	lit := false
	for _, st := range s.stateList() {
		if !st.monitor.Asleep() {
			lit = true
			break
		}
	}
	s.tracker.Record(lit, time.Duration(display.IdleSeconds()*float64(time.Second)))
	s.usage = s.tracker.Recent(7)
	s.save()
}

func (s *store) toggleLaunchAtLogin() {
	if err := mygo.App.SetOpenAtLogin(!s.launchAtLogin); err != nil {
		s.notice = locales.NoticeLoginFailed(err.Error())
	}
	s.launchAtLogin = mygo.App.OpenAtLogin()
}

func (s *store) save() {
	if s.prefs.needsMigration() {
		s.syncLegacy()
	}
	if s.dirty {
		s.prefs.save()
		s.dirty = false
	}
}
