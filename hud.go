package main

import (
	"fmt"
	"math"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/binaryify/MonitorBar/locales"
)

// hudKind is the level the HUD shows.
type hudKind string

const (
	hudBrightness hudKind = "brightness"
	hudVolume     hudKind = "volume"
)

const (
	hudWidth, hudHeight = 260, 64
	hudDuration         = 1500 * time.Millisecond
	hudBar              = 150 // the level bar's length
)

// adjusted tells that a key just set a level, for the HUD to show it.
func (s *store) adjusted(kind hudKind) { s.adjustedFor(kind, s.targetState()) }

// adjustedFor shows a level of st: a media key may set the sound's display,
// which is not always the target.
func (s *store) adjustedFor(kind hudKind, st *displayState) {
	if kind == "" || !s.prefs.HUD || kind == hudVolume && !st.hasVolume {
		return
	}
	s.hudState = st
	s.onAdjust(kind)
}

// hudDisplay is the monitor the HUD talks about: the one a key just set,
// else the target.
func (s *store) hudDisplay() *displayState {
	if st := s.hudState; st != nil {
		if s.displays[st.id] == st {
			return st
		}
	}
	return s.targetState()
}

// hudLevel is what the HUD shows for kind: its icon, title and percent.
func (s *store) hudLevel(kind hudKind) (iconName, title string, percent float64) {
	st := s.hudDisplay()
	title = locales.Brightness()
	if kind == hudVolume {
		iconName, title = "volume", locales.Volume()
		if st.volume == 0 {
			iconName = "volumeOff"
		}
		if s.displayCount() > 1 {
			title = st.displayName() + " · " + title
		}
		return iconName, title, st.volume
	}
	if st.hasBrightness {
		percent = combinedBrightnessFor(st)
	} else {
		percent = st.prefs.SoftBrightness
	}
	if s.displayCount() > 1 {
		title = st.displayName() + " · " + title
	}
	return "sun", title, percent
}

// showHUD shows the level a key set under the menu bar icon (by the
// notification area on Windows), while the panel, which shows it already,
// is closed. It goes after a moment without keys; it never takes the
// keyboard or the pointer from the app in use.
func (a *app) showHUD(kind hudKind) {
	if a.flyout != nil {
		return
	}
	a.hudKind, a.hudUntil = kind, time.Now().Add(hudDuration)
	time.AfterFunc(hudDuration, func() { mygo.RunOnMain(a.hideHUD) })
	if a.hud != nil {
		a.hud.Invalidate()
		return
	}
	a.hud = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:      a.tray,
		Placement: mygo.PlacementBottom,
		Gap:       4,
		Width:     hudWidth,
		Height:    hudHeight,
		Shadow:    true,
		Content:   ui.View(a.hudView),
	})
	a.hud.SetIgnoreMouseEvents(true)
	a.hud.OnClosed(func() { a.hud = nil })
	if corner, ok := a.corner(hudWidth, hudHeight); ok {
		a.hud.SetBounds(corner)
	}
}

// hideHUD closes the HUD once no key has shown it for a while.
func (a *app) hideHUD() {
	if a.hud != nil && !time.Now().Before(a.hudUntil) {
		a.hud.Close()
	}
}

func (a *app) hudView(c *ui.Context) {
	name, title, percent := a.s.hudLevel(a.hudKind)
	c.SetTheme(darkTheme)
	if runtime.GOOS == "windows" {
		c.Root().Background(panelBottom) // opaque for ClearType, as the panel's
	} else {
		c.Root().Background(ui.Transparent)
	}
	box := ui.Row(c).Size(hudWidth, hudHeight).Padding(0, 18).Gap(14).AlignItems(ui.Center).
		TextColor(ink).Gradient(panelTop, panelBottom, 135)
	if runtime.GOOS == "darwin" {
		box.Radius(16).Border(0.7, ui.RGBA(255, 255, 255, 0.12)).Clip()
	}
	box.Children(func() {
		icon(c, name).FontSize(20).TextColor(accent)
		ui.Column(c).Gap(8).Grow(1).Children(func() {
			ui.Text(c, title).FontSize(11).FontWeight(600).TextColor(muted).SingleLine()
			ui.Box(c).Size(hudBar, 4).Radius(2).Background(ui.RGBA(255, 255, 255, 0.12)).Children(func() {
				ui.Box(c).Size(float32(hudBar*min(max(percent, 0), 100)/100), 4).Radius(2).Background(accent)
			})
		})
		ui.Text(c, fmt.Sprint(int(math.Round(percent)))).FontSize(22).FontWeight(600).FontFeatures("tnum")
	})
}
