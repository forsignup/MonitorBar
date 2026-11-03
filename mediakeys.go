package main

import (
	"github.com/egoist/mygo"

	"github.com/binaryify/MonitorBar/internal/mediakeys"
)

// mediaKey is what a brightness or volume key does while MonitorBar takes
// the keys: step changes the external display, nil to leave the key to
// macOS, and pass lets macOS have the key as well.
func (s *store) mediaKey(k mediakeys.Key) (step func(), pass bool) {
	target := s.targetState()
	// The volume keys follow the sound, wherever it plays: a media key may
	// set a display that isn't the target.
	volume := s.soundDisplay()
	if volume == nil {
		volume = target
	}
	switch k {
	case mediakeys.BrightnessUp, mediakeys.BrightnessDown:
		if target.monitor.Builtin || target.prefs.FollowBuiltin && s.canFollowFor(target) {
			return nil, false // no external display, or it follows the built-in one already
		}
		delta := float64(shortcutStep)
		if k == mediakeys.BrightnessDown {
			delta = -delta
		}
		// A MacBook's own display keeps its keys: both change together.
		return func() { s.stepBrightnessFor(target, delta); s.adjustedFor(hudBrightness, target) }, s.hasBuiltin
	case mediakeys.VolumeUp, mediakeys.VolumeDown:
		if !volume.hasVolume || !s.soundToDisplay() {
			return nil, false // the sound plays elsewhere, at macOS's volume
		}
		delta := float64(shortcutStep)
		if k == mediakeys.VolumeDown {
			delta = -delta
		}
		return func() { s.stepVolumeFor(volume, delta); s.adjustedFor(hudVolume, volume) }, false
	case mediakeys.Mute:
		if !volume.hasVolume || !s.soundToDisplay() {
			return nil, false
		}
		return func() { s.toggleMuteFor(volume); s.adjustedFor(hudVolume, volume) }, false
	}
	return nil, false
}

// onMediaKey is the event tap's handler.
func (s *store) onMediaKey(k mediakeys.Key, down bool) (take bool) {
	step, pass := s.mediaKey(k)
	if step == nil {
		return false
	}
	if down { // after the tap returns, as macOS holds the keys until then
		go s.post(step)
	}
	return !pass
}

// syncMediaKeys takes the keys or gives them back, as asked and allowed. It
// runs every second, as the user grants the permission in System Settings
// (or loses it to an update signed anew).
func (s *store) syncMediaKeys() {
	want := s.prefs.MediaKeys && s.bindShortcuts && mediakeys.Supported
	switch {
	case !want:
		mediakeys.Stop()
		s.mediaKeysDenied = false
	case !mediakeys.Trusted():
		mediakeys.Stop()
		s.mediaKeysDenied = true
	case !mediakeys.Running():
		s.mediaKeysDenied = mediakeys.Start(s.onMediaKey) != nil
	default:
		s.mediaKeysDenied = false
	}
}

// setMediaKeys turns taking the keys on or off, asking macOS for the
// permission the first time.
func (s *store) setMediaKeys(on bool) {
	s.prefs.MediaKeys, s.dirty = on, true
	if on && s.bindShortcuts && !mediakeys.Trusted() {
		mediakeys.RequestAccess()
	}
	s.syncMediaKeys()
}

// openAccessibility opens the Accessibility list in System Settings.
func openAccessibility() { _ = mygo.Shell.OpenExternal(mediakeys.SettingsURL) }
