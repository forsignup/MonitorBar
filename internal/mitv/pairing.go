package mitv

import (
	"strings"
)

// NormalizeName compares display and device names: case, spacing and the
// full-width forms some firmware reports don't tell them apart.
func NormalizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= '！' && r <= '～': // full-width ASCII
			r = r - '！' + '!'
		case r == '\u3000': // ideographic space
			r = ' '
		}
		if r == ' ' || r == '\t' || r == '\u00a0' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Match finds the device a monitor automatically pairs with: the only free
// device whose name is the monitor's. Same-named devices, or one already
// paired with another monitor, are left for the user to choose; a monitor
// with no name never pairs by itself.
func Match(monitorName string, devices []Device, taken map[string]bool) *Device {
	name := NormalizeName(monitorName)
	if name == "" {
		return nil
	}
	var found *Device
	for i := range devices {
		d := &devices[i]
		if taken[d.Host] || NormalizeName(d.Name) != name {
			continue
		}
		if found != nil {
			return nil // ambiguous: same model, same name
		}
		found = d
	}
	return found
}
