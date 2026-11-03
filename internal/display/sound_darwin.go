//go:build darwin

package display

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

type audioPropertyAddress struct{ selector, scope, element uint32 }

var (
	audioObjectGetPropertyData func(object uint32, address *audioPropertyAddress, qualifierSize uint32, qualifier uintptr, size *uint32, data unsafe.Pointer) int32
	audioOnce                  sync.Once
)

// fourCC packs a Core Audio four-character code.
func fourCC(s string) uint32 {
	return uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
}

func loadAudio() {
	audioOnce.Do(func() {
		lib, err := purego.Dlopen("/System/Library/Frameworks/CoreAudio.framework/CoreAudio", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			return
		}
		purego.RegisterLibFunc(&audioObjectGetPropertyData, lib, "AudioObjectGetPropertyData")
	})
}

// SoundToDisplay says whether the system's sound goes to a display, over
// HDMI or DisplayPort, whose volume macOS can't set.
func SoundToDisplay() bool {
	_, onDisplay := SoundOutputName()
	return onDisplay
}

// SoundOutputName names the default output device when it goes to a display
// over HDMI or DisplayPort: the name often carries the monitor's, which lets
// the app tell which display the volume keys should set.
func SoundOutputName() (string, bool) {
	loadAudio()
	if audioObjectGetPropertyData == nil {
		return "", false
	}
	const systemObject = 1
	get := func(object uint32, selector string) (uint32, bool) {
		address := audioPropertyAddress{fourCC(selector), fourCC("glob"), 0}
		var value uint32
		size := uint32(unsafe.Sizeof(value))
		return value, audioObjectGetPropertyData(object, &address, 0, 0, &size, unsafe.Pointer(&value)) == 0
	}
	device, ok := get(systemObject, "dOut") // the default output device
	if !ok || device == 0 {
		return "", false
	}
	transport, ok := get(device, "tran")
	if !ok || (transport != fourCC("hdmi") && transport != fourCC("dprt")) {
		return "", false
	}
	address := audioPropertyAddress{fourCC("lnam"), fourCC("glob"), 0} // kAudioDevicePropertyDeviceNameCFString
	var name uintptr
	size := uint32(unsafe.Sizeof(name))
	if audioObjectGetPropertyData(device, &address, 0, 0, &size, unsafe.Pointer(&name)) != 0 || name == 0 {
		return "", true
	}
	defer cfRelease(name)
	return goCFString(name), true
}
