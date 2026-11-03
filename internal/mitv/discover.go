package mitv

import (
	"errors"
	"net"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound means no HyperOS monitor answered on the local network.
var ErrNotFound = errors.New("mitv: no monitor found")

// IsMonitor tells a monitor from the other devices with the same remote
// API, such as TVs: MonitorBar must never switch a TV in another room. A
// monitor's name says so, unless its owner renamed it (#5); then its DP or
// USB-C input does, which TVs don't have.
func (l *Link) IsMonitor() bool {
	_, _, ok := l.monitor()
	return ok
}

// monitor reads the device's name and inputs, and reports whether it is a
// monitor; the scan keeps both so it doesn't ask twice.
func (l *Link) monitor() (string, []Source, bool) {
	name, err := l.DeviceName()
	if err != nil {
		return "", nil, false
	}
	sources, sourcesErr := l.Sources()
	if namedMonitor(name) {
		return name, sources, true
	}
	return name, sources, sourcesErr == nil && hasMonitorInput(sources)
}

// Probe asks one address what it is, with its name and inputs: a cached
// pairing is still a monitor only if this answers yes.
func (l *Link) Probe() (Device, bool) {
	name, sources, ok := l.monitor()
	if !ok {
		return Device{}, false
	}
	return Device{Host: l.Host, Name: name, Sources: sources}, true
}

func namedMonitor(name string) bool {
	return strings.Contains(name, "显示器") || strings.Contains(strings.ToLower(name), "monitor")
}

func hasMonitorInput(sources []Source) bool {
	return slices.ContainsFunc(sources, func(s Source) bool { return s.ID == SourceDP || s.ID == SourceUSBC })
}

// Device is a HyperOS remote found by a scan.
type Device struct {
	Host    string
	Name    string
	Sources []Source
}

// DiscoverAll returns every HyperOS monitor on the local network, sorted by
// address. MonitorBar manages their displays, so all of them are needed,
// not just the first to answer.
func DiscoverAll() ([]Device, error) {
	var (
		mu      sync.Mutex
		devices []Device
		wg      sync.WaitGroup
		slots   = make(chan struct{}, 64) // the probes' concurrency: a /24 doesn't burst
	)
	for _, host := range candidates() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			d, ok := probeDevice(host)
			if !ok {
				return
			}
			mu.Lock()
			devices = append(devices, d)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(devices) == 0 {
		return nil, ErrNotFound
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Host < devices[j].Host })
	return devices, nil
}

// probeDevice asks one address whether it is a monitor.
func probeDevice(host string) (Device, bool) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "6095"), 500*time.Millisecond)
	if err != nil {
		return Device{}, false
	}
	conn.Close()
	name, sources, ok := New(host).monitor()
	if !ok {
		return Device{}, false
	}
	return Device{Host: host, Name: name, Sources: sources}, true
}

// Discover returns the first HyperOS monitor's address, for callers that
// manage one display.
func Discover() (string, error) {
	devices, err := DiscoverAll()
	if err != nil {
		return "", err
	}
	return devices[0].Host, nil
}

// candidates lists the other addresses of each private IPv4 /24 we are on.
func candidates() []string {
	var hosts []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip == nil || ip.IsLoopback() || !ip.IsPrivate() {
			continue
		}
		for last := 1; last < 255; last++ {
			if byte(last) != ip[3] {
				hosts = append(hosts, net.IPv4(ip[0], ip[1], ip[2], byte(last)).String())
			}
		}
	}
	return hosts
}
