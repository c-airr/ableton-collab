package tui

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	idHost  = "AAAAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
	idGuest = "KS776L4-HIZCKQU-TMCZNZ2-TY3Q6H4-OIK6PPP-3JYMQ4F-4XLB6WR-LBGQEAR"
	idOther = "ZZZZZZZ-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH"
)

type fakeNet struct {
	lobby              syncthing.Lobby
	accepted, rejected []string
}

func (f *fakeNet) Lobby() (*syncthing.Lobby, error) { l := f.lobby; return &l, nil }
func (f *fakeNet) Invite(name string) string        { return "ABC1KOD" + strings.ToUpper(name) }
func (f *fakeNet) Accept(r syncthing.Request) error {
	f.accepted = append(f.accepted, r.Name)
	f.drop(r.ID)
	f.lobby.Members = append(f.lobby.Members, syncthing.Member{ID: r.ID, Name: r.Name})
	return nil
}
func (f *fakeNet) Reject(r syncthing.Request) error {
	f.rejected = append(f.rejected, r.Name)
	f.drop(r.ID)
	return nil
}
func (f *fakeNet) drop(id string) {
	var keep []syncthing.Request
	for _, r := range f.lobby.Requests {
		if r.ID != id {
			keep = append(keep, r)
		}
	}
	f.lobby.Requests = keep
}

func tinySet() []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<Ableton MajorVersion=\"5\">\r\n\t<LiveSet>\r\n\t\t<Tracks />\r\n\t</LiveSet>\r\n</Ableton>\r\n"))
	w.Close()
	return buf.Bytes()
}

func noClipboard(t *testing.T) {
	old := clipboard
	clipboard = func(string) error { return errors.New("test") }
	t.Cleanup(func() { clipboard = old })
}

func TestLobby(t *testing.T) {
	noClipboard(t)
	p := project(t, "Ja", tinySet())
	net := &fakeNet{lobby: syncthing.Lobby{
		Me:      idHost,
		Members: []syncthing.Member{{ID: idOther, Name: "Basia", Connected: true, Via: "relay"}},
		Requests: []syncthing.Request{
			{ID: idGuest, Name: "Kolega", Time: time.Now()},
			{ID: idOther[:8] + "XXXXXXX" + idOther[15:], Name: "Obcy", Time: time.Now()},
		},
		Sync: &syncthing.FolderStatus{State: "idle"},
	}}
	m := New(p, net)
	m = run(t, m, m.refreshCmd(true))
	if v := m.View(); !strings.Contains(v, "2 prosb(y) o dolaczenie") {
		t.Fatalf("status bez prosb/obecnosci:\n%s", v)
	}
	m = key(t, m, "2")
	v := m.View()
	dump(t, v)
	for _, want := range []string{"Lobby (2/10)", "AAAAAAA", "Basia", "przez relay", "Kolega", "KS776L4", "Obcy", "przyjmij", "wszystko przeslane"} {
		if !strings.Contains(v, want) {
			t.Fatalf("lobby bez %q:\n%s", want, v)
		}
	}
	m = key(t, m, "j") // na Obcego
	m = key(t, m, "n")
	m = key(t, m, "a") // kursor wraca na jedyna prosbe - Kolega
	if fmt.Sprint(net.rejected) != "[Obcy]" || fmt.Sprint(net.accepted) != "[Kolega]" {
		t.Fatalf("odrzucone %v, przyjete %v", net.rejected, net.accepted)
	}
	v = m.View()
	if !strings.Contains(v, "Lobby (3/10)") || strings.Contains(v, "Prosby o dolaczenie") || !strings.Contains(v, "Kolega przyjety") {
		t.Fatalf("po akceptacji:\n%s", v)
	}

	m = key(t, m, "i")
	v = m.View()
	dump(t, v)
	if !strings.Contains(v, "ABC1KODJA") || !strings.Contains(v, "abcollab join") {
		t.Fatalf("brak kodu zaproszenia:\n%s", v)
	}
	if m = key(t, m, "esc"); strings.Contains(m.View(), "ABC1KODJA") {
		t.Fatal("esc nie zamknal zaproszenia")
	}
}

func TestLobbyFull(t *testing.T) {
	p := project(t, "Ja", tinySet())
	net := &fakeNet{lobby: syncthing.Lobby{Me: idHost, Requests: []syncthing.Request{{ID: idGuest, Name: "Dziesiaty"}}}}
	for i := 0; i < syncthing.MaxMembers-1; i++ {
		net.lobby.Members = append(net.lobby.Members, syncthing.Member{ID: fmt.Sprint(i), Name: fmt.Sprint("osoba", i)})
	}
	m := New(p, net)
	m = run(t, m, m.refreshCmd(true))
	m = key(t, m, "2")
	m = key(t, m, "a")
	if len(net.accepted) != 0 || !strings.Contains(m.View(), "Lobby pelne") {
		t.Fatalf("przyjeto ponad limit:\n%s", m.View())
	}
}

func TestNoNetwork(t *testing.T) {
	p := project(t, "Ja", tinySet())
	m := New(p, nil)
	m = run(t, m, m.refreshCmd(true))
	m = key(t, m, "i")
	m = key(t, m, "2")
	if v := m.View(); !strings.Contains(v, "bez sieci") || strings.Contains(v, "zapros") {
		t.Fatalf("offline:\n%s", v)
	}
}

// fakeJoiner: "dolaczenie" kopiuje historie hosta do folderu goscia, jak
// zrobilby to Syncthing po akceptacji.
type fakeJoiner struct {
	t      *testing.T
	host   *store.Project
	joined string
}

func (f *fakeJoiner) DeviceID() string                           { return idGuest }
func (f *fakeJoiner) Folders() ([]syncthing.FolderConfig, error) { return nil, nil }
func (f *fakeJoiner) Share(dir, id, label string) (Network, error) {
	return &fakeNet{}, nil
}
func (f *fakeJoiner) Join(inv syncthing.Invite, dir string) (Network, error) {
	if ents, _ := os.ReadDir(dir); len(ents) > 0 {
		return nil, errors.New("folder nie jest pusty")
	}
	f.joined = dir
	copyDir(f.t, filepath.Join(f.host.Dir, store.Dir), filepath.Join(dir, store.Dir))
	return &fakeNet{lobby: syncthing.Lobby{Members: []syncthing.Member{{ID: inv.Device, Connected: true}}}}, nil
}

func startKey(t *testing.T, s Start, k string) Start {
	t.Helper()
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "ctrl+u":
		msg = tea.KeyMsg{Type: tea.KeyCtrlU}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k), Paste: len(k) > 1}
	}
	next, cmd := s.Update(msg)
	s = next.(Start)
	for i := 0; cmd != nil && i < 20; i++ {
		m := cmd()
		if _, ok := m.(tea.QuitMsg); ok {
			break
		}
		next, cmd = s.Update(m)
		s = next.(Start)
	}
	return s
}

func TestJoinFlow(t *testing.T) {
	host := project(t, "Ja", tinySet())
	base := t.TempDir()
	j := &fakeJoiner{t: t, host: host}
	s := NewStart(j, store.NewDevice("Kolega"), base, "")
	s.pollWait = 0
	next, _ := s.Update(s.Init()())
	s = next.(Start)
	if s.stage != stCode {
		t.Fatalf("bez projektow powinno od razu pytac o kod, jest %d", s.stage)
	}
	s = startKey(t, s, "smieci")
	s = startKey(t, s, "enter")
	if s.err == nil || s.stage != stCode {
		t.Fatal("zly kod przyjety")
	}
	code := syncthing.Invite{Device: idHost, Folder: "abcollab-abc", Name: "song", Host: "Ja"}.Code()
	s = startKey(t, s, "ctrl+u")
	s = startKey(t, s, code[:30]+"\n"+code[30:])
	s = startKey(t, s, "enter")
	want := filepath.Join(base, "song Project")
	if s.stage != stDir || s.input != want {
		t.Fatalf("etap %d, folder %q", s.stage, s.input)
	}
	dump(t, s.View())
	s = startKey(t, s, "enter")
	dump(t, s.View())
	if s.Dir() != want || j.joined != want {
		t.Fatalf("Dir()=%q joined=%q etap=%d err=%v\n%s", s.Dir(), j.joined, s.stage, s.err, s.View())
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
	s := NewStart(&fakeJoiner{}, store.NewDevice("Kolega"), base, code)
	s = startKey(t, s, "enter") // kod -> folder
	s = startKey(t, s, "ctrl+u")
	s = startKey(t, s, base)
	s = startKey(t, s, "enter")
	if s.Dir() != "" || s.stage != stDir || s.err == nil || !strings.Contains(s.View(), "nie jest pusty") {
		t.Fatalf("dolaczono do niepustego folderu:\n%s", s.View())
	}
}
