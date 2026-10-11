package tui

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/transport/syncthing"
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
	if !strings.Contains(v, "ABC1KODJA") || !strings.Contains(v, "Dolacz do projektu") {
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
