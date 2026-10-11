package live

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	kernel32                   = syscall.NewLazyDLL("kernel32.dll")
	procGetForegroundWindow    = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcess = user32.NewProc("GetWindowThreadProcessId")
	procSendInput              = user32.NewProc("SendInput")
	procOpenProcess            = kernel32.NewProc("OpenProcess")
	procQueryImageName         = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle            = kernel32.NewProc("CloseHandle")
)

// keyInput: INPUT z KEYBDINPUT (uklad 64-bitowy, 40 bajtow).
type keyInput struct {
	typ   uint32
	_     uint32
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	_     uint32
	extra uintptr
	_     [8]byte
}

// liveInFront: czy okno na wierzchu nalezy do Ableton Live.
func liveInFront() bool {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return false
	}
	var pid uint32
	procGetWindowThreadProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	const queryLimited = 0x1000
	h, _, _ := procOpenProcess.Call(queryLimited, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := procQueryImageName.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:n])))
	return strings.HasPrefix(name, "ableton live")
}

// SaveInLive wysyla Ctrl+S, jesli Live jest na wierzchu (nie kradniemy fokusu).
func SaveInLive() (bool, error) {
	if !liveInFront() {
		return false, nil
	}
	const vkControl, vkS, keyUp = 0x11, 0x53, 0x2
	in := []keyInput{
		{typ: 1, vk: vkControl},
		{typ: 1, vk: vkS},
		{typ: 1, vk: vkS, flags: keyUp},
		{typ: 1, vk: vkControl, flags: keyUp},
	}
	r, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if r != uintptr(len(in)) {
		return false, err
	}
	return true, nil
}

// AutoSaveDefault: na Windows auto-zapis dziala bez dodatkowych uprawnien.
const AutoSaveDefault = true
