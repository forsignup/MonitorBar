//go:build windows

package display

// SoundToDisplay has no Windows counterpart yet: the media keys are
// macOS's alone.
func SoundToDisplay() bool { return false }

// SoundOutputName has no Windows counterpart yet.
func SoundOutputName() (string, bool) { return "", false }
