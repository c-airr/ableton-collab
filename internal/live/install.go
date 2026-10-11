// Package live: wspolpraca na zywo z otwartym Live'em przez wlasny Remote
// Script (AbletonCollab). Skrypt podsluchuje zmiany w secie i nanosi zmiany
// innych; ten pakiet go instaluje, rozmawia z nim przez TCP i wymienia
// zmiany z innymi uczestnikami przez folder sesji (Syncthing).
package live

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed script/AbletonCollab/__init__.py
var scriptFS embed.FS

const ScriptName = "AbletonCollab"

// UserLibraries: foldery "User Library" Live'a na tym komputerze (Live na
// Windows/macOS i Live pod Wine na Linuksie). ABCOLLAB_USER_LIBRARY nadpisuje.
func UserLibraries() []string {
	if p := os.Getenv("ABCOLLAB_USER_LIBRARY"); p != "" {
		return []string{p}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var cands []string
	switch runtime.GOOS {
	case "darwin":
		cands = []string{filepath.Join(home, "Music", "Ableton", "User Library")}
	case "windows":
		cands = []string{
			filepath.Join(home, "Documents", "Ableton", "User Library"),
			filepath.Join(home, "OneDrive", "Documents", "Ableton", "User Library"),
		}
	default:
		for _, pat := range []string{
			".wine*/drive_c/users/*/Documents/Ableton/User Library",
			".local/share/wineprefixes/*/drive_c/users/*/Documents/Ableton/User Library",
			"Games/*/drive_c/users/*/Documents/Ableton/User Library",
			"*/drive_c/users/*/Documents/Ableton/User Library",
		} {
			m, _ := filepath.Glob(filepath.Join(home, pat))
			cands = append(cands, m...)
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// Install kopiuje skrypt do Remote Scripts w kazdej znalezionej User Library
// (nadpisuje starsza wersje) i zapisuje token. Zwraca foldery skryptu i
// informacje, czy cos sie zmienilo (Live trzeba wtedy uruchomic ponownie).
func Install(token string) (dirs []string, changed bool, err error) {
	src, _ := fs.ReadFile(scriptFS, "script/AbletonCollab/__init__.py")
	for _, lib := range UserLibraries() {
		dir := filepath.Join(lib, "Remote Scripts", ScriptName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return dirs, changed, err
		}
		for name, data := range map[string][]byte{"__init__.py": src, "token.txt": []byte(token + "\n")} {
			path := filepath.Join(dir, name)
			if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
				continue
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return dirs, changed, err
			}
			changed = true
		}
		dirs = append(dirs, dir)
	}
	return dirs, changed, nil
}

// Token: sekret do skryptu, trzymany w pliku (staly miedzy uruchomieniami,
// zeby nie przeinstalowywac skryptu za kazdym razem).
func Token(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) >= 16 {
		return string(bytes.TrimSpace(data)), nil
	}
	b := make([]byte, 16)
	rand.Read(b)
	t := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return t, os.WriteFile(path, []byte(t+"\n"), 0o600)
}
