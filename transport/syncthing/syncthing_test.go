package syncthing

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/store"
)

func TestInviteRoundtrip(t *testing.T) {
	inv := Invite{Device: "KS776L4-HIZCKQU-TMCZNZ2-TY3Q6H4-OIK6PPP-3JYMQ4F-4XLB6WR-LBGQEAR", Folder: "abcollab-abc234", Name: "claudwe", Host: "Ja"}
	code := inv.Code()
	if strings.ContainsAny(code, "-_=+/ ") {
		t.Fatalf("kod ma znaki, ktore psuja zaznaczanie: %s", code)
	}
	// wklejony z komunikatora: male litery, spacje, nowe linie
	pasted := "  " + strings.ToLower(code[:20]) + "\n" + code[20:] + " \n"
	got, err := ParseInvite(pasted)
	if err != nil || got != inv {
		t.Fatalf("got %+v, %v", got, err)
	}
	boxed := "│ " + code[:40] + " │\n│ " + code[40:] + "   │"
	if got, err := ParseInvite("`" + boxed + "`"); err != nil || got != inv {
		t.Fatalf("kod z ramki: %+v, %v", got, err)
	}
	for _, bad := range []string{"", "ABC1", "XYZ" + code[3:], code[:len(code)-5], "ABC1" + Invite{Device: "zle", Folder: "abcollab-x"}.Code()[4:]} {
		if _, err := ParseInvite(bad); err == nil {
			t.Errorf("przyjeto zly kod %q", bad)
		}
	}
	evil := Invite{Device: inv.Device, Folder: inv.Folder, Name: `..\..\Windows`, Host: "a\x1b[2Jb"}
	got, err = ParseInvite(evil.Code())
	if err != nil || strings.ContainsAny(got.Name, `\/`) || strings.Contains(got.Host, "\x1b") {
		t.Fatalf("nazwy z kodu nie oczyszczone: %+v %v", got, err)
	}
}

func TestVia(t *testing.T) {
	for _, c := range []struct {
		c    Connection
		want string
	}{
		{Connection{Type: "tcp-client", Address: "192.168.1.5:22000"}, "LAN"},
		{Connection{Type: "quic-server", Address: "37.30.40.196:22000"}, "internet"},
		{Connection{Type: "relay-client", Address: "1.2.3.4:443"}, "relay"},
		{Connection{Type: "tcp-server", Address: "[::1]:22000"}, "LAN"},
	} {
		if got := via(c.c); got != c.want {
			t.Errorf("%+v: %s, chce %s", c.c, got, c.want)
		}
	}
}

// Prawdziwe parowanie dwoch (i odrzucenie trzeciej) instancji Syncthinga na
// tej maszynie. Wymaga binarki: ABCOLLAB_SYNCTHING=sciezka/do/syncthing.
func TestPairing(t *testing.T) {
	exe := os.Getenv("ABCOLLAB_SYNCTHING")
	if exe == "" {
		t.Skip("ustaw ABCOLLAB_SYNCTHING, zeby testowac z prawdziwym Syncthingiem")
	}
	tmp := t.TempDir()
	start := func(name string) *Node {
		n, err := Start(Options{Home: filepath.Join(tmp, "st-"+name), Exe: exe, Name: name, LocalOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(n.Stop)
		return n
	}
	host, guest, stranger := start("Ja"), start("Kolega"), start("Obcy")

	// host: projekt z setem
	hostDir := filepath.Join(tmp, "host", "song Project")
	os.MkdirAll(hostDir, 0o755)
	set := tinySet(t)
	os.WriteFile(filepath.Join(hostDir, "song.als"), set, 0o644)
	hp, _, err := store.Init(hostDir, "song.als", store.Device{ID: "HOSTDEV", Name: "Ja", Peer: host.ID})
	if err != nil {
		t.Fatal(err)
	}
	hshare, err := host.Share(hostDir, FolderID(hp.Meta.ID), "song")
	if err != nil {
		t.Fatal(err)
	}
	code := hshare.Invite("Ja")

	// gosc wkleja kod
	inv, err := ParseInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	guestDir := filepath.Join(tmp, "guest", inv.Name)
	gshare, err := guest.Join(inv, guestDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.Join(inv, guestDir); err == nil {
		t.Error("drugie dolaczenie do tego samego projektu przeszlo")
	}
	if _, err := host.Join(inv, filepath.Join(tmp, "x")); err == nil {
		t.Error("dolaczenie wlasnym kodem przeszlo")
	}
	full := filepath.Join(tmp, "pelny")
	os.MkdirAll(full, 0o755)
	os.WriteFile(filepath.Join(full, "prywatne.txt"), []byte("x"), 0o644)
	if _, err := stranger.Join(inv, full); err == nil {
		t.Error("dolaczenie do niepustego folderu przeszlo")
	}
	if _, err := stranger.Join(inv, filepath.Join(tmp, "stranger", "song")); err != nil {
		t.Fatal(err)
	}
	// bez discovery: adres hosta podajemy wprost
	for _, n := range []*Node{guest, stranger} {
		if err := n.PatchDevice(host.ID, map[string]any{"addresses": []string{host.ListenAddr()}}); err != nil {
			t.Fatal(err)
		}
	}

	// host widzi dwie prosby z nazwami; Kolege przyjmuje, Obcego odrzuca
	var l *Lobby
	waitFor(t, "prosby u hosta", func() bool {
		l, err = hshare.Lobby()
		return err == nil && len(l.Requests) == 2
	})
	byName := map[string]Request{}
	for _, r := range l.Requests {
		byName[r.Name] = r
	}
	if byName["Kolega"].ID != guest.ID || byName["Obcy"].ID != stranger.ID {
		t.Fatalf("prosby: %+v", l.Requests)
	}
	if err := hshare.Reject(byName["Obcy"]); err != nil {
		t.Fatal(err)
	}
	if err := hshare.Accept(byName["Kolega"]); err != nil {
		t.Fatal(err)
	}

	// gosc dostaje projekt i robi checkout
	var gp *store.Project
	waitFor(t, "projekt u goscia", func() bool {
		gp, err = store.Open(guestDir, store.Device{ID: "GUESTDEV", Name: "Kolega", Peer: guest.ID})
		if err != nil {
			return false
		}
		r, err := gp.Pull(nil)
		return err == nil && len(r.FastForwarded) == 1
	})
	got, _ := os.ReadFile(gp.WorkingPath())
	if !bytes.Equal(got, set) {
		t.Error("set u goscia rozni sie od seta hosta")
	}

	// lobby po obu stronach: polaczeni przez LAN; Obcy nie wraca jako prosba
	waitFor(t, "lobby po akceptacji", func() bool {
		hl, err1 := hshare.Lobby()
		gl, err2 := gshare.Lobby()
		return err1 == nil && err2 == nil && len(hl.Members) == 1 && hl.Members[0].Connected && hl.Members[0].Via == "LAN" &&
			hl.Members[0].Name == "Kolega" && len(hl.Requests) == 0 &&
			len(gl.Members) == 1 && gl.Members[0].Connected && gl.Members[0].ID == host.ID
	})
	time.Sleep(3 * time.Second) // Obcy probuje sie polaczyc co 2 s
	if hl, _ := hshare.Lobby(); len(hl.Requests) != 0 {
		t.Errorf("odrzucony wrocil: %+v", hl.Requests)
	}
	// .als nie jest synchronizowany - kazdy ma wlasny plik roboczy
	os.WriteFile(filepath.Join(guestDir, "song.als"), []byte("lokalna zmiana"), 0o644)
	time.Sleep(3 * time.Second)
	if now, _ := os.ReadFile(filepath.Join(hostDir, "song.als")); !bytes.Equal(now, set) {
		t.Error("plik roboczy goscia nadpisal set hosta")
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func tinySet(t *testing.T) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\r\n<Ableton MajorVersion=\"5\">\r\n\t<LiveSet>\r\n\t\t<Tracks />\r\n\t</LiveSet>\r\n</Ableton>\r\n"))
	w.Close()
	return buf.Bytes()
}
