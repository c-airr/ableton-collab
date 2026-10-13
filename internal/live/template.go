package live

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// DefaultSet: szablon nowego seta - ten sam, od ktorego Live zaczyna
// File > New: wlasny (User Library/Templates/DefaultLiveSet.als), inaczej
// wbudowany w Live'a (najnowsza zainstalowana wersja). "" - nie znaleziono.
// ABCOLLAB_DEFAULT_SET nadpisuje.
func DefaultSet() string {
	if p := os.Getenv("ABCOLLAB_DEFAULT_SET"); p != "" {
		return p
	}
	libs := UserLibraries()
	for _, ul := range libs {
		if p := filepath.Join(ul, "Templates", "DefaultLiveSet.als"); isFile(p) {
			return p
		}
	}
	builtin := filepath.Join("Resources", "Builtin", "Templates", "DefaultLiveSet.als")
	var pats []string
	switch runtime.GOOS {
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		pats = []string{filepath.Join(pd, "Ableton", "Live *", builtin)}
	case "darwin":
		pats = []string{"/Applications/Ableton Live *.app/Contents/App-Resources/Builtin/Templates/DefaultLiveSet.als"}
	default:
		// Live pod Wine: ProgramData w tym samym prefiksie co User Library
		for _, ul := range libs {
			if i := strings.Index(ul, "drive_c"); i >= 0 {
				pats = append(pats, filepath.Join(ul[:i+len("drive_c")], "ProgramData", "Ableton", "Live *", builtin))
			}
		}
	}
	for _, pat := range pats {
		m, _ := filepath.Glob(pat)
		sort.Strings(m)
		for i := len(m) - 1; i >= 0; i-- {
			if isFile(m[i]) {
				return m[i]
			}
		}
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
