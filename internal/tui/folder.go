package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/store"
)

// FolderInfo: co jest w folderze, ktory ma byc wspoldzielony w calosci.
type FolderInfo struct {
	Dir     string
	Sets    []string // sety (.als) wzgledem Dir ("/"), bez kopii z Backup/
	Files   int
	Bytes   int64
	More    bool // przerwano liczenie (bardzo duzy folder)
	Project bool // juz jest sesja abcollab
}

// Big: tyle danych, ze warto zapytac, zanim wszystko poleci do innych.
func (f FolderInfo) Big() bool { return f.More || f.Files > 5000 || f.Bytes > 2<<30 }

// forbidden: foldery, ktorych nie wolno wspoldzielic w calosci - pomylka
// wyslalaby innym cale Dokumenty albo biblioteke Live'a.
func forbidden(dir string) string {
	if filepath.Dir(dir) == dir {
		return "it's a whole drive"
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, sub := range []string{"", "Documents", "Desktop", "Downloads", "Music", "Pictures", "Videos",
			"OneDrive", filepath.Join("OneDrive", "Documents"), filepath.Join("Documents", "Ableton"), filepath.Join("Music", "Ableton")} {
			if samePath(dir, filepath.Join(home, sub)) {
				return "it's your whole " + filepath.Join(filepath.Base(home), sub) + " folder"
			}
		}
		if rel, err := filepath.Rel(dir, home); err == nil && !strings.HasPrefix(rel, "..") {
			return "your home folder is inside it"
		}
	}
	for d := dir; ; d = filepath.Dir(d) {
		if filepath.Base(d) == "User Library" || filepath.Base(d) == "Factory Packs" {
			return "it's Live's library"
		}
		if d != dir {
			if _, err := os.Stat(filepath.Join(d, store.Dir, "project.json")); err == nil {
				return "it's inside another session (" + d + ")"
			}
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return ""
}

// InspectFolder sprawdza folder sesji: czy wolno go udostepnic, ile wazy i
// jakie sety w nim leza.
func InspectFolder(dir string) (FolderInfo, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return FolderInfo{}, err
	}
	info := FolderInfo{Dir: dir}
	if why := forbidden(dir); why != "" {
		return info, fmt.Errorf("you can't share this folder: %s. Pick or create a separate folder for the session", why)
	}
	if _, err := os.Stat(filepath.Join(dir, store.Dir, "project.json")); err == nil {
		info.Project = true
	}
	const maxFiles = 50000
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // nieczytelne pomijamy
		}
		if d.IsDir() {
			if p != dir && (d.Name() == store.Dir || d.Name() == "Backup" || strings.HasPrefix(d.Name(), ".st")) {
				return filepath.SkipDir
			}
			return nil
		}
		if hidden(d.Name()) {
			return nil
		}
		info.Files++
		if fi, err := d.Info(); err == nil {
			info.Bytes += fi.Size()
		}
		if strings.EqualFold(filepath.Ext(p), ".als") {
			if rel, err := filepath.Rel(dir, p); err == nil {
				info.Sets = append(info.Sets, filepath.ToSlash(rel))
			}
		}
		if info.Files >= maxFiles {
			info.More = true
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(info.Sets)
	return info, err
}

// setReady: set da sie w calosci odczytac (Live skonczyl zapis).
func setReady(dir, set string) bool {
	_, err := als.ReadFile(filepath.Join(dir, filepath.FromSlash(set)))
	return err == nil
}

func humanCount(f FolderInfo) string {
	s := fmt.Sprintf("%d files, %s", f.Files, humanBytes(f.Bytes))
	if f.More {
		s = "over " + s
	}
	return s
}

func drives() []string {
	var out []string
	for c := 'A'; c <= 'Z'; c++ {
		d := string(c) + `:\`
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// cleanPath: sciezka wpisana albo przeciagnieta do terminala (bywa w cudzyslowach).
func cleanPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "& ") // PowerShell po przeciagnieciu pliku
	return strings.Trim(s, `"'`)
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || os.PathSeparator == '\\' && strings.EqualFold(a, b)
}

// clipboard: podmieniany w testach, zeby nie ruszac schowka uzytkownika.
var clipboard = copyToClipboard

// copyToClipboard: schowek systemowy przez narzedzia systemu (bez cgo).
func copyToClipboard(s string) error {
	var cmds [][]string
	switch runtime.GOOS {
	case "windows":
		cmds = [][]string{{"clip"}}
	case "darwin":
		cmds = [][]string{{"pbcopy"}}
	default:
		cmds = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	for _, c := range cmds {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(s)
		if cmd.Run() == nil {
			return nil
		}
	}
	return errors.New("no clipboard")
}
