package display

import (
	"fmt"
	"sort"
	"strings"
)

// parseEDID reads the manufacturer, product and serial numbers from the
// first 16 bytes of an EDID block.
func parseEDID(b []byte) (vendor, model, serial uint32, ok bool) {
	if len(b) < 16 || b[0] != 0 {
		return 0, 0, 0, false
	}
	vendor = uint32(b[8])<<8 | uint32(b[9])
	model = uint32(b[11])<<8 | uint32(b[10])
	serial = uint32(b[12]) | uint32(b[13])<<8 | uint32(b[14])<<16 | uint32(b[15])<<24
	return vendor, model, serial, true
}

// assignIdentities puts the monitors in the system's order and gives each
// its identity, appending the port's key when panels share an EDID.
func assignIdentities(monitors []Monitor) {
	orderMonitors(monitors)
	keys := identities(edids(monitors))
	for i := range monitors {
		monitors[i].Identity = keys[i]
	}
}

// edids are the monitors' identity fields, in their order.
func edids(monitors []Monitor) []Identity {
	out := make([]Identity, len(monitors))
	for i, m := range monitors {
		out[i] = m.edid()
	}
	return out
}

// orderMonitors puts the main display first, the others from left to right,
// as the system's display settings list them.
func orderMonitors(monitors []Monitor) {
	sort.SliceStable(monitors, func(i, j int) bool {
		if monitors[i].Primary != monitors[j].Primary {
			return monitors[i].Primary
		}
		return monitors[i].x < monitors[j].x
	})
}

// Identity is how a monitor is recognised across launches and replugs: the
// panel's own EDID, and the port's location only when two online panels
// report the same EDID. Positions change when a cable moves; EDIDs don't.
type Identity struct {
	Vendor   uint32 // EDID manufacturer id, such as 0x10ac
	Model    uint32 // EDID product code
	Serial   uint32 // EDID serial number, often 0
	Position string // platform location key, "" when the EDID is enough
}

// Base is the identity of the panel alone, without the port: vendor, model
// and serial, in hex.
func (id Identity) Base() string {
	return fmt.Sprintf("%04x:%04x:%08x", id.Vendor, id.Model, id.Serial)
}

// String is the identity as kept in the settings: the base, and the
// position after "@" when panels share their EDID.
func (id Identity) String() string {
	if id.Position == "" {
		return id.Base()
	}
	return id.Base() + "@" + id.Position
}

// baseOf returns the panel part of an identity string.
func baseOf(identity string) string {
	if i := strings.IndexByte(identity, '@'); i >= 0 {
		return identity[:i]
	}
	return identity
}

// identities gives every online monitor its identity. A panel whose EDID
// another online panel shares gets its position appended, so the two keep
// separate settings: an EDID-only key would make them one monitor.
func identities(ids []Identity) []string {
	shared := map[string]int{}
	for _, id := range ids {
		shared[id.Base()]++
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		if shared[id.Base()] > 1 && id.Position != "" {
			out[i] = id.String()
		} else {
			out[i] = id.Base()
		}
	}
	return out
}

// MatchIdentity finds the saved identity an online monitor is: the exact
// one, else the only saved one of the same panel. Ambiguity (two panels of
// the same EDID, neither matched by position) is no match: the monitor
// starts with defaults instead of taking another one's settings.
func MatchIdentity(online string, saved []string) string {
	base := baseOf(online)
	found, matches := "", 0
	for _, id := range saved {
		if id == online {
			return id
		}
		if baseOf(id) == base {
			found, matches = id, matches+1
		}
	}
	if matches == 1 {
		return found
	}
	return ""
}
