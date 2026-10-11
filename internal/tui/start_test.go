package tui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeBackend: "dolaczenie" kopiuje historie hosta do folderu goscia, jak
// zrobilby to Syncthing po akceptacji.
type fakeBackend struct {
	t        *testing.T
	host     *store.Project
	folders  []syncthing.FolderConfig
	joined   string
	created  string
	renamed  string
	failWith error
}

func (f *fakeBackend) DeviceID() string                           { return idGuest }
func (f *fakeBackend) Folders() ([]syncthing.FolderConfig, error) { return f.folders, nil }
func (f *fakeBackend) Share(dir, id, label string) (Network, error) {
	return &fakeNet{}, nil
}
func (f *fakeBackend) Rename(name string) error { f.renamed = name; return nil }
func (f *fakeBackend) Create(set string) (string, error) {
	if f.failWith != nil {
		return "", f.failWith
	}
	f.created = set
	return filepath.Dir(set), nil
}
func (f *fakeBackend) Join(inv syncthing.Invite, dir string) (Network, error) {
	if ents, _ := os.ReadDir(dir); len(ents) > 0 {
		return nil, errors.New("folder nie jest pusty")
	}
	f.joined = dir
	copyDir(f.t, filepath.Join(f.host.Dir, store.Dir), filepath.Join(dir, store.Dir))
	return &fakeNet{lobby: syncthing.Lobby{Members: []syncthing.Member{{ID: inv.Device, Connected: true}}}}, nil
}

// press: klawisz albo wklejony tekst; wykonuje wynikajace komendy do konca.
func press(t *testing.T, s Start, k string) Start {
	t.Helper()
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+u":
		msg = tea.KeyMsg{Type: tea.KeyCtrlU}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k), Paste: len(k) > 1}
	}
	next, cmd := s.Update(msg)
	return drain(t, next.(Start), cmd)
}

func drain(t *testing.T, s Start, cmd tea.Cmd) Start {
	for i := 0; cmd != nil && i < 20; i++ {
		m := cmd()
		if _, ok := m.(tea.QuitMsg); ok {
			break
		}
		next, c := s.Update(m)
		s, cmd = next.(Start), c
	}
	return s
}

func started(t *testing.T, be Backend, o StartOptions) Start {
	s := NewStart(be, o)
	s.pollWait = 0
	next, _ := s.Update(s.loadProjects()())
	return next.(Start)
}

func TestMenuFirstRunAndJoin(t *testing.T) {
	host := project(t, "Ja", tinySet())
	base := t.TempDir()
	be := &fakeBackend{t: t, host: host}
	s := started(t, be, StartOptions{Me: store.NewDevice("DESKTOP-X1"), AskName: true, BaseDir: base})
	if v := s.View(); !strings.Contains(v, "Jak maja cie widziec") || !strings.Contains(v, "DESKTOP-X1") {
		t.Fatalf("pierwsze uruchomienie:\n%s", v)
	}
	s = press(t, s, "ctrl+u")
	s = press(t, s, "Kolega")
	s = press(t, s, "enter")
	if be.renamed != "Kolega" || s.stage != stMenu || !strings.Contains(s.View(), "Kolega") {
		t.Fatalf("nazwa: %q, etap %d", be.renamed, s.stage)
	}
	v := s.View()
	dump(t, v)
	if !strings.Contains(v, "Nowy projekt") || !strings.Contains(v, "Dolacz do projektu") {
		t.Fatalf("menu:\n%s", v)
	}

	s = press(t, s, "d")
	s = press(t, s, "smieci")
	s = press(t, s, "enter")
	if s.err == nil || s.stage != stCode {
		t.Fatal("zly kod przyjety")
	}
	code := syncthing.Invite{Device: idHost, Folder: "abcollab-abc", Name: "song", Host: "Ja"}.Code()
	s = press(t, s, "ctrl+u")
	s = press(t, s, code[:30]+"\n"+code[30:])
	s = press(t, s, "enter")
	want := filepath.Join(base, "song Project")
	if s.stage != stDir || s.input != want {
		t.Fatalf("etap %d, folder %q", s.stage, s.input)
	}
	s = press(t, s, "enter")
	if s.Dir() != want || be.joined != want || s.Created() {
		t.Fatalf("Dir()=%q joined=%q etap=%d err=%v\n%s", s.Dir(), be.joined, s.stage, s.err, s.View())
	}
	got, _ := os.ReadFile(filepath.Join(want, "song.als"))
	if !bytes.Equal(got, tinySet()) {
		t.Error("set nie zostal pobrany")
	}
}

func TestJoinNonEmptyDir(t *testing.T) {
	base := t.TempDir()
	os.WriteFile(filepath.Join(base, "prywatne.txt"), []byte("x"), 0o644)
	code := syncthing.Invite{Device: idHost, Folder: "abcollab-abc", Name: "song", Host: "Ja"}.Code()
	s := NewStart(&fakeBackend{t: t}, StartOptions{Me: store.NewDevice("Kolega"), BaseDir: base, Code: code})
	s = drain(t, s, s.Init())
	if s.stage != stDir {
		t.Fatalf("kod z `join` nie przeszedl dalej: etap %d, %v", s.stage, s.err)
	}
	s = press(t, s, "ctrl+u")
	s = press(t, s, `"`+base+`"`) // przeciagniety folder - w cudzyslowach
	s = press(t, s, "enter")
	if s.Dir() != "" || s.stage != stDir || s.err == nil || !strings.Contains(s.View(), "nie jest pusty") {
		t.Fatalf("dolaczono do niepustego folderu:\n%s", s.View())
	}
}

// liveProject: folder projektu Live z setem (jak zapisuje go Live).
func liveProject(t *testing.T, root, name string, mod time.Time) string {
	dir := filepath.Join(root, name+" Project")
	os.MkdirAll(filepath.Join(dir, "Ableton Project Info"), 0o755)
	set := filepath.Join(dir, name+".als")
	os.WriteFile(set, tinySet(), 0o644)
	os.Chtimes(set, mod, mod)
	return set
}

func TestFindSets(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old := liveProject(t, filepath.Join(root, "saves"), "stary", now.Add(-time.Hour))
	fresh := liveProject(t, filepath.Join(root, "saves"), "nowy", now)
	// juz projekt abcollab - pomijany
	shared := liveProject(t, root, "wspolny", now)
	os.MkdirAll(filepath.Join(filepath.Dir(shared), store.Dir), 0o755)
	// luzny set bez folderu projektu i kopie zapasowe - pomijane
	os.WriteFile(filepath.Join(root, "luzny.als"), tinySet(), 0o644)
	bak := filepath.Join(filepath.Dir(fresh), "Backup")
	os.MkdirAll(bak, 0o755)
	os.WriteFile(filepath.Join(bak, "nowy [2026-10-11].als"), tinySet(), 0o644)

	got := FindSets([]string{root, filepath.Join(root, "nie-ma")}, 10)
	if len(got) != 2 || got[0] != fresh || got[1] != old {
		t.Fatalf("FindSets: %v", got)
	}
}

func TestMenuNewProject(t *testing.T) {
	root := t.TempDir()
	set := liveProject(t, root, "bit", time.Now())
	be := &fakeBackend{t: t, folders: []syncthing.FolderConfig{{ID: "abcollab-x", Label: "stary", Path: root}}}
	s := started(t, be, StartOptions{Me: store.NewDevice("Ja"), Roots: []string{root}})
	if !strings.Contains(s.View(), "Twoje projekty") || !strings.Contains(s.View(), "stary") {
		t.Fatalf("brak listy projektow:\n%s", s.View())
	}
	s = press(t, s, "n")
	v := s.View()
	dump(t, v)
	if s.stage != stNew || !strings.Contains(v, "bit.als") {
		t.Fatalf("wybor setu:\n%s", v)
	}
	be.failWith = errors.New("nie lezy w folderze projektu Live")
	s = press(t, s, "enter")
	if s.err == nil || s.Dir() != "" {
		t.Fatal("blad zakladania nie pokazany")
	}
	be.failWith = nil
	s = press(t, s, "enter")
	if be.created != set || s.Dir() != filepath.Dir(set) || !s.Created() {
		t.Fatalf("created=%q dir=%q", be.created, s.Dir())
	}
}

func TestMenuNewProjectTypedPath(t *testing.T) {
	be := &fakeBackend{t: t}
	s := started(t, be, StartOptions{Me: store.NewDevice("Ja")})
	s = press(t, s, "n")
	s = drain(t, s, func() tea.Msg { return setsMsg(nil) })
	s = press(t, s, `& 'C:\Muzyka\x Project\x.als'`) // przeciagniety plik w PowerShellu
	s = press(t, s, "enter")
	if be.created != `C:\Muzyka\x Project\x.als` {
		t.Fatalf("sciezka: %q", be.created)
	}
}

func TestShowInvite(t *testing.T) {
	noClipboard(t)
	p := project(t, "Ja", tinySet())
	m := New(p, &fakeNet{}).ShowInvite()
	if m.tab != tabLobby || !strings.Contains(m.View(), "ABC1KODJA") {
		t.Fatalf("nowy projekt bez zaproszenia:\n%s", m.View())
	}
}
