package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/store"
)

// --- przegladarka folderow ------------------------------------------------------------

// browser: lista podfolderow do chodzenia strzalkami. Pusta sciezka na
// Windowsie = lista dyskow.
type browser struct {
	path  string
	dirs  []string
	files int // plikow w biezacym folderze (bez podfolderow)
	cur   int
	err   error
}

func newBrowser(path string) browser {
	b := browser{path: path}
	b.load()
	return b
}

func (b *browser) load() {
	b.dirs, b.files, b.cur, b.err = nil, 0, 0, nil
	if b.path == "" {
		b.dirs = drives()
		return
	}
	ents, err := os.ReadDir(b.path)
	if err != nil {
		b.err = err
		return
	}
	for _, e := range ents {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$"):
		case e.IsDir():
			b.dirs = append(b.dirs, name)
		default:
			b.files++
		}
	}
	sort.Slice(b.dirs, func(i, j int) bool { return strings.ToLower(b.dirs[i]) < strings.ToLower(b.dirs[j]) })
}

// up: folder wyzej; zwraca nazwe folderu, z ktorego wyszlismy (kursor na nim).
func (b *browser) up() {
	if b.path == "" {
		return
	}
	parent := filepath.Dir(b.path)
	from := filepath.Base(b.path)
	if parent == b.path { // korzen dysku
		if runtime.GOOS != "windows" {
			return
		}
		parent, from = "", b.path
	}
	b.path = parent
	b.load()
	for i, d := range b.dirs {
		if strings.EqualFold(d, from) {
			b.cur = i
		}
	}
}

func (b *browser) enter() {
	if len(b.dirs) == 0 {
		return
	}
	if b.path == "" {
		b.path = b.dirs[b.cur]
	} else {
		b.path = filepath.Join(b.path, b.dirs[b.cur])
	}
	b.load()
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

// --- sprawdzenie folderu sesji -----------------------------------------------------------

// FolderInfo: co jest w folderze, ktory ma byc wspoldzielony w calosci.
type FolderInfo struct {
	Dir     string
	Sets    []string // sety (.als) wzgledem Dir, bez kopii z Backup/
	Files   int
	Bytes   int64
	More    bool // przerwano liczenie (bardzo duzy folder)
	Project bool // juz jest projektem abcollab
}

// Big: tyle danych, ze warto zapytac, zanim wszystko poleci do innych.
func (f FolderInfo) Big() bool { return f.More || f.Files > 5000 || f.Bytes > 2<<30 }

// forbidden: foldery, ktorych nie wolno wspoldzielic w calosci - pomylka
// wyslalaby innym cale Dokumenty albo biblioteke Live'a.
func forbidden(dir string) string {
	if filepath.Dir(dir) == dir {
		return "to caly dysk"
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		for _, sub := range []string{"", "Documents", "Desktop", "Downloads", "Music", "Pictures", "Videos",
			"OneDrive", filepath.Join("OneDrive", "Documents"), filepath.Join("Documents", "Ableton"), filepath.Join("Music", "Ableton")} {
			if samePath(dir, filepath.Join(home, sub)) {
				return "to caly folder " + filepath.Join(filepath.Base(home), sub)
			}
		}
		if rel, err := filepath.Rel(dir, home); err == nil && !strings.HasPrefix(rel, "..") {
			return "w nim jest twoj folder domowy"
		}
	}
	for d := dir; ; d = filepath.Dir(d) {
		if filepath.Base(d) == "User Library" || filepath.Base(d) == "Factory Packs" {
			return "to biblioteka Live'a"
		}
		if d != dir {
			if _, err := os.Stat(filepath.Join(d, store.Dir, "project.json")); err == nil {
				return "jest w srodku innego projektu abcollab (" + d + ")"
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
		return info, fmt.Errorf("tego folderu nie mozna udostepnic: %s. Wybierz albo zaloz (+) osobny folder na sesje", why)
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
	s := fmt.Sprintf("%d plikow, %s", f.Files, humanBytes(f.Bytes))
	if f.More {
		s = "ponad " + s
	}
	return s
}
