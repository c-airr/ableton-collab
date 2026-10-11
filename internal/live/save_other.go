//go:build !windows

package live

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// SaveInLive wysyla Ctrl+S (Cmd+S na macOS), jesli Live jest na wierzchu.
// Linux (Live pod Wine): xdotool. macOS: osascript - wymaga zgody na
// "Dostepnosc" dla terminala, dlatego tam auto-zapis jest domyslnie wylaczony.
func SaveInLive() (bool, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("osascript", "-e",
			`tell application "System Events" to get name of first application process whose frontmost is true`).Output()
		if err != nil {
			return false, err
		}
		if !strings.Contains(string(out), "Live") {
			return false, nil
		}
		if out, err := exec.Command("osascript", "-e",
			`tell application "System Events" to keystroke "s" using command down`).CombinedOutput(); err != nil {
			return false, errors.New("macOS blocked the keystroke - allow your terminal in System Settings > Privacy & Security > Accessibility: " + strings.TrimSpace(string(out)))
		}
		return true, nil
	}
	if _, err := exec.LookPath("xdotool"); err != nil {
		return false, errors.New("auto-save needs xdotool (sudo apt install xdotool)")
	}
	out, err := exec.Command("xdotool", "getactivewindow", "getwindowname").Output()
	if err != nil || !strings.Contains(string(out), "Ableton Live") {
		return false, nil
	}
	if err := exec.Command("xdotool", "key", "--clearmodifiers", "ctrl+s").Run(); err != nil {
		return false, err
	}
	return true, nil
}

// AutoSaveDefault: na macOS wylaczony (uprawnienia), na Linuksie wlaczony.
const AutoSaveDefault = runtime.GOOS != "darwin"
