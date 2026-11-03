package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/binaryify/MonitorBar/internal/display"
	"github.com/binaryify/MonitorBar/internal/mitv"
)

// probe prints what the app can see and control, without UI (--probe).
func probe() {
	monitors := display.All()
	builtin := false
	if len(monitors) == 0 {
		monitors = []display.Monitor{display.Target()}
		builtin = true
	}
	settings := loadSettings()
	taken := map[string]bool{}
	for _, p := range settings.Monitors {
		if p.NetworkHost != "" {
			taken[p.NetworkHost] = true
		}
	}
	var devices []mitv.Device
	scanned := false
	for i, m := range monitors {
		if i > 0 {
			fmt.Println()
		}
		heading := fmt.Sprintf("显示器 %d/%d: %s", i+1, len(monitors), m.Name)
		if builtin {
			heading = "显示器: " + or(m.Name, "内屏")
		}
		fmt.Printf("%s  接口: %s  身份: %s%s\n", heading, or(m.Connection, "未知"), m.Identity, positionNote(m.Identity))
		probeModes(m)
		probeDDC(m)
		if !builtin {
			if !scanned {
				devices, scanned = probeDevices(), true
			}
			paired := pairedHost(settings, m.Identity)
			if paired == "" && len(monitors) == 1 && settings.needsMigration() {
				paired = settings.NetworkHost // a version 1 file's one display
			}
			probeLAN(m, devices, paired, taken)
		}
	}
}

// probeDevices scans the LAN once for every display.
func probeDevices() []mitv.Device {
	start := time.Now()
	devices, err := mitv.DiscoverAll()
	if err != nil {
		return nil
	}
	if len(devices) > 0 {
		fmt.Printf("局域网: 发现 %d 台澎湃 OS 显示器（%.2fs）\n", len(devices), time.Since(start).Seconds())
	}
	return devices
}

func probeModes(m display.Monitor) {
	if mode, ok := m.CurrentMode(); ok {
		hidpi := ""
		if mode.HiDPI() {
			hidpi = " HiDPI"
		}
		fmt.Printf("当前模式: %s%s %dHz\n", mode.Title(), hidpi, mode.RefreshRate)
	}
	modes := m.Modes()
	current, _ := m.CurrentMode()
	w, h := m.NativeSize(modes, current)
	fmt.Printf("原生分辨率: %d × %d（%s）\n", w, h, display.SizeLabel(w, h))
	fmt.Printf("可用模式: %d 个  亮屏: %v  空闲: %.0f 秒\n", len(modes), !m.Asleep(), display.IdleSeconds())
}

func probeDDC(m display.Monitor) {
	d, err := m.OpenDDC()
	if err != nil {
		switch {
		case errors.Is(err, display.ErrNoHardwareControl):
			fmt.Printf("DDC/CI: 无硬件通道（%v）\n", err)
		default:
			fmt.Println("DDC/CI: 显示器未响应")
		}
		return
	}
	defer d.Close()
	for _, v := range []struct {
		code byte
		name string
	}{{display.VCPBrightness, "亮度"}, {display.VCPContrast, "对比度"}, {display.VCPVolume, "音量"}, {display.VCPInput, "输入源"}} {
		if cur, max, ok := d.Get(v.code); ok {
			fmt.Printf("DDC/CI %s: %d / %d\n", v.name, cur, max)
		}
	}
	caps := d.Capabilities()
	fmt.Println("DDC/CI 能力:", or(caps, "（未提供）"))
	if ids := display.ParseCapabilities(caps)[display.VCPInput]; len(ids) > 0 {
		fmt.Print("DDC/CI 输入源列表:")
		for _, id := range ids {
			fmt.Printf(" %s=0x%02X", display.InputNameFor(m.Name, id), id)
		}
		fmt.Println()
	}
}

// probeLAN prints the device the display is paired with, or the one its name
// matches, and says when same-named devices make the pairing ambiguous.
func probeLAN(m display.Monitor, devices []mitv.Device, paired string, taken map[string]bool) {
	if paired != "" {
		for _, d := range devices {
			if d.Host == paired {
				fmt.Printf("局域网: 已配对 %s @ %s\n", or(d.Name, "未命名设备"), paired)
				probeSources(d)
				return
			}
		}
		fmt.Printf("局域网: 已配对 %s（未在线）\n", paired)
		return
	}
	if d := mitv.Match(m.Name, devices, taken); d != nil {
		fmt.Printf("局域网: %s @ %s（按名称配对）\n", or(d.Name, "未命名设备"), d.Host)
		probeSources(*d)
		return
	}
	var sameName []mitv.Device
	want := mitv.NormalizeName(m.Name)
	for _, d := range devices {
		if want != "" && mitv.NormalizeName(d.Name) == want {
			sameName = append(sameName, d)
		}
	}
	if len(sameName) > 1 {
		fmt.Printf("局域网: 同名设备 %d 台，需在面板中手动配对:", len(sameName))
		for _, d := range sameName {
			fmt.Printf(" %s", d.Host)
		}
		fmt.Println()
		return
	}
	fmt.Println("局域网: 未发现匹配的设备")
}

// probeSources prints a device's inputs and volume.
func probeSources(d mitv.Device) {
	if len(d.Sources) > 0 {
		fmt.Print("输入源:")
		for _, s := range d.Sources {
			fmt.Printf(" %s=%d", s.Name, s.ID)
		}
		fmt.Println()
	}
	if v, err := mitv.New(d.Host).Volume(); err == nil {
		fmt.Printf("音量: %d\n", v)
	}
}

// pairedHost is the address saved for a display, by its exact identity or the
// only record of the same panel.
func pairedHost(s *settings, identity string) string {
	if p := s.Monitors[identity]; p != nil {
		return p.NetworkHost
	}
	saved := make([]string, 0, len(s.Monitors))
	for id := range s.Monitors {
		saved = append(saved, id)
	}
	if id := display.MatchIdentity(identity, saved); id != "" {
		return s.Monitors[id].NetworkHost
	}
	return ""
}

// positionNote says whether the identity needed the port's location.
func positionNote(identity string) string {
	if identity == "builtin" {
		return "（内屏）"
	}
	if strings.Contains(identity, "@") {
		return "（位置键）"
	}
	return "（EDID）"
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
