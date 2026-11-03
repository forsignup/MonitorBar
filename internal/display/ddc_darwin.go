//go:build darwin

package display

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// errNoAVService means this Mac has no AV service (Intel, or the private
// symbols are gone): the framebuffer path may still reach a display.
var errNoAVService = errors.New("display: no AV services")

// Apple Silicon reaches the display's I2C bus through these IOKit calls,
// which Apple doesn't document; DDC/CI talks to address 0x37 on it.
var (
	ioavServiceCreateWithService func(alloc uintptr, service uint32) uintptr
	ioavServiceReadI2C           func(service uintptr, chip, offset uint32, buf *byte, size uint32) int32
	ioavServiceWriteI2C          func(service uintptr, chip, offset uint32, buf *byte, size uint32) int32
	ioavServiceCopyEDID          func(service uintptr, edid *uintptr) int32
	coreDisplayInfoDict          func(id uint32) uintptr
	ioavOnce                     sync.Once
	ioavErr                      error
)

func loadIOAV() error {
	ioavOnce.Do(func() {
		load()
		io, err := purego.Dlopen("/System/Library/Frameworks/IOKit.framework/IOKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			ioavErr = fmt.Errorf("%w: %v", errNoAVService, err)
			return
		}
		for fn, name := range map[any]string{
			&ioavServiceCreateWithService: "IOAVServiceCreateWithService",
			&ioavServiceReadI2C:           "IOAVServiceReadI2C",
			&ioavServiceWriteI2C:          "IOAVServiceWriteI2C",
		} {
			if _, err := purego.Dlsym(io, name); err != nil { // Intel Macs lack them
				ioavErr = fmt.Errorf("%w: %v", errNoAVService, err)
				return
			}
			purego.RegisterLibFunc(fn, io, name)
		}
		// Newer systems only: without it, one external display can still use
		// its AV service; with several, none is bound.
		if _, err := purego.Dlsym(io, "IOAVServiceCopyEDID"); err == nil {
			purego.RegisterLibFunc(&ioavServiceCopyEDID, io, "IOAVServiceCopyEDID")
		}
		if lib, err := purego.Dlopen("/System/Library/PrivateFrameworks/CoreDisplay.framework/CoreDisplay", purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			if _, err := purego.Dlsym(lib, "CoreDisplay_DisplayCreateInfoDictionary"); err == nil {
				purego.RegisterLibFunc(&coreDisplayInfoDict, lib, "CoreDisplay_DisplayCreateInfoDictionary")
			}
		}
	})
	return ioavErr
}

type ioavDDC struct{ service uintptr }

// ddcTransport opens the monitor's DDC/CI channel: the AV service where the
// Mac has one (Apple Silicon), else the framebuffer's own I2C bus (Intel).
func (m Monitor) ddcTransport() (ddcTransport, error) {
	t, err := m.avDDCTransport()
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, errNoAVService) {
		return nil, err
	}
	// No DCP AV services (Intel, or the private symbols changed): the
	// framebuffer path may still reach this display.
	if fb, fbErr := m.framebufferDDC(); fbErr == nil {
		return fb, nil
	} else {
		return nil, fmt.Errorf("%w: %v; %v", ErrNoHardwareControl, err, fbErr)
	}
}

// avDDCTransport opens the AV service bound to this monitor. Binding never
// guesses: an unbound monitor gets ErrNoHardwareControl, so its I2C bus is
// not written by mistake.
func (m Monitor) avDDCTransport() (ddcTransport, error) {
	if err := loadIOAV(); err != nil {
		return nil, err
	}
	service, err := avServiceFor(m.handle)
	if err != nil {
		return nil, err
	}
	cfRetain(service)
	d := &ioavDDC{service: service}
	if _, _, ok := d.read(VCPBrightness); !ok { // a stale service answers nothing
		d.close()
		forgetAVService(m.handle)
		return nil, fmt.Errorf("%w: the AV service bound to display %d stopped answering", ErrNoHardwareControl, m.handle)
	}
	return d, nil
}

// MARK: Binding displays to AV services

// avCandidate is an external AV service and the EDID it reports.
type avCandidate struct {
	service      uintptr
	edid         Identity
	hasEDID      bool
	registryPath string
}

var avCache struct {
	sync.Mutex
	built     bool
	byDisplay map[uint32]uintptr // retained AV services
}

// ResetDDCBinding forgets the display-to-service binding, after the display
// configuration changed or the Mac woke: AV services can go stale.
func ResetDDCBinding() {
	avCache.Lock()
	defer avCache.Unlock()
	for _, service := range avCache.byDisplay {
		cfRelease(service)
	}
	avCache.byDisplay, avCache.built = nil, false
}

// forgetAVService drops one stale binding after it stopped answering.
func forgetAVService(id uint32) {
	avCache.Lock()
	defer avCache.Unlock()
	if service, ok := avCache.byDisplay[id]; ok {
		cfRelease(service)
		delete(avCache.byDisplay, id)
	}
}

// avServiceFor returns the display's AV service, building the binding once
// per display configuration.
func avServiceFor(id uint32) (uintptr, error) {
	avCache.Lock()
	defer avCache.Unlock()
	if !avCache.built {
		avCache.byDisplay = bindAVServices()
		avCache.built = true
	}
	if service := avCache.byDisplay[id]; service != 0 {
		return service, nil
	}
	return 0, fmt.Errorf("%w: no AV service matches display %d's EDID", ErrNoHardwareControl, id)
}

// bindAVServices maps each external display to its own AV service. The AV
// service's EDID is the panel's, so it can be compared with the display's;
// panels that share an EDID are told apart by the registry path the display
// reports. Anything ambiguous stays unbound.
func bindAVServices() map[uint32]uintptr {
	var iter uint32
	if ioServiceGetMatchingServices(0, ioServiceMatching("DCPAVServiceProxy"), &iter) != 0 {
		return nil
	}
	defer ioObjectRelease(iter)
	key := cfStringCreateWithCString(0, "Location", utf8Encoding)
	defer cfRelease(key)
	var candidates []avCandidate
	for entry := ioIteratorNext(iter); entry != 0; entry = ioIteratorNext(iter) {
		location := ioRegistryEntryCreateCFProperty(entry, key, 0, 0)
		external := goCFString(location) == "External"
		if location != 0 {
			cfRelease(location)
		}
		if !external {
			ioObjectRelease(entry)
			continue
		}
		service := ioavServiceCreateWithService(0, entry)
		if service == 0 {
			ioObjectRelease(entry)
			continue
		}
		c := avCandidate{service: service, registryPath: registryPath(entry)}
		if ioavServiceCopyEDID != nil {
			var data uintptr
			if ioavServiceCopyEDID(service, &data) == 0 && data != 0 {
				if b := cfDataBytes(data); len(b) > 0 {
					vendor, model, serial, ok := parseEDID(b)
					c.edid, c.hasEDID = Identity{Vendor: vendor, Model: model, Serial: serial}, ok
				}
				cfRelease(data)
			}
		}
		candidates = append(candidates, c)
		ioObjectRelease(entry)
	}
	displays := All()
	bound := map[uint32]uintptr{}
	for _, d := range displays {
		service := bindAVService(d, candidates)
		if service != 0 {
			bound[d.handle] = service
			continue
		}
		// One external display and one external AV service: the old rule,
		// kept so a single-monitor Mac behaves as before even where the
		// private EDID call is gone.
		if len(displays) == 1 && len(candidates) == 1 && !candidates[0].hasEDID && ioavServiceCopyEDID == nil {
			bound[d.handle] = candidates[0].service
		}
	}
	for _, c := range candidates { // release the ones no display took
		if !hasService(bound, c.service) {
			cfRelease(c.service)
		}
	}
	return bound
}

// bindAVService is the binding rule: the exact EDID triple, else the
// display's own registry location, else nothing.
func bindAVService(d Monitor, candidates []avCandidate) uintptr {
	var matched []avCandidate
	for _, c := range candidates {
		if c.hasEDID && c.edid.Vendor == d.Vendor && c.edid.Model == d.Model && c.edid.Serial == d.Serial {
			matched = append(matched, c)
		}
	}
	if len(matched) == 1 {
		return matched[0].service
	}
	location := displayLocation(d.handle)
	if location == "" {
		return 0
	}
	var found uintptr
	for _, c := range candidates {
		if c.registryPath == "" {
			continue
		}
		if c.registryPath == location || strings.HasPrefix(c.registryPath, location+"/") {
			if found != 0 {
				return 0 // ambiguous: never guess
			}
			found = c.service
		}
	}
	return found
}

// displayLocation is the IORegistry location macOS reports for a display.
func displayLocation(id uint32) string {
	if coreDisplayInfoDict == nil {
		return ""
	}
	info := coreDisplayInfoDict(id)
	if info == 0 {
		return ""
	}
	defer cfRelease(info)
	key := cfStringCreateWithCString(0, "IODisplayLocation", utf8Encoding)
	defer cfRelease(key)
	return goCFString(cfDictionaryGetValue(info, key))
}

// registryPath is an IOService entry's path, for matching a display's
// location.
func registryPath(entry uint32) string {
	buf := make([]byte, 4096)
	if ioRegistryEntryGetPath(entry, "IOService", &buf[0], uint32(len(buf))) != 0 {
		return ""
	}
	return cString(&buf[0])
}

// cfDataBytes copies a CFDataRef's bytes for parsing.
func cfDataBytes(data uintptr) []byte {
	n := cfDataGetLength(data)
	if n <= 0 {
		return nil
	}
	ptr := cfDataGetBytePtr(data)
	if ptr == nil {
		return nil
	}
	return unsafe.Slice(ptr, n)
}

func hasService(m map[uint32]uintptr, v uintptr) bool {
	for _, x := range m {
		if x == v {
			return true
		}
	}
	return false
}

func (d *ioavDDC) read(code byte) (value, maximum int, ok bool) {
	for range 3 {
		reply := make([]byte, 11)
		if d.send([]byte{0x01, code}) && d.receive(reply) && reply[2] == 0x02 && reply[4] == code {
			if reply[3] != 0 { // the monitor answered "unsupported code"
				return 0, 0, false
			}
			return int(reply[8])<<8 | int(reply[9]), int(reply[6])<<8 | int(reply[7]), true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, 0, false
}

func (d *ioavDDC) write(code byte, value int) bool {
	return d.send([]byte{0x03, code, byte(value >> 8), byte(value)})
}

func (d *ioavDDC) capabilities() string {
	var caps []byte
	for len(caps) < 4096 {
		reply := make([]byte, 38)
		offset := len(caps)
		if !d.send([]byte{0xF3, byte(offset >> 8), byte(offset)}) || !d.receive(reply) || reply[2] != 0xE3 {
			break
		}
		end := 2 + int(reply[1]&0x7F)
		if end < 5 {
			break
		}
		chunk := reply[5:end]
		caps = append(caps, chunk...)
		if len(chunk) == 0 || bytes.IndexByte(chunk, 0) >= 0 {
			break
		}
	}
	if i := bytes.IndexByte(caps, 0); i >= 0 {
		caps = caps[:i]
	}
	return string(caps)
}

func (d *ioavDDC) close() { cfRelease(d.service) }

// send writes a DDC/CI packet: length, payload, checksum. It goes out twice,
// as some monitors drop the first one after idling.
func (d *ioavDDC) send(payload []byte) bool {
	packet := append([]byte{0x80 | byte(len(payload))}, payload...)
	sum := byte(0x6E ^ 0x51)
	for _, b := range packet {
		sum ^= b
	}
	packet = append(packet, sum)
	sent := false
	for range 2 {
		time.Sleep(10 * time.Millisecond)
		if ioavServiceWriteI2C(d.service, 0x37, 0x51, &packet[0], uint32(len(packet))) == 0 {
			sent = true
		}
	}
	return sent
}

// receive reads a reply and checks its length and checksum.
func (d *ioavDDC) receive(reply []byte) bool {
	time.Sleep(50 * time.Millisecond)
	if ioavServiceReadI2C(d.service, 0x37, 0x51, &reply[0], uint32(len(reply))) != 0 {
		return false
	}
	length := int(reply[1] & 0x7F)
	if length < 1 || length+2 >= len(reply) {
		return false
	}
	sum := byte(0x50)
	for _, b := range reply[:2+length] {
		sum ^= b
	}
	return sum == reply[2+length]
}
