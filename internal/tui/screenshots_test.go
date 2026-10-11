package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/internal/live"
	"github.com/c-airr/ableton-collab/store"
	"github.com/c-airr/ableton-collab/transport/syncthing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Zrzuty ekranu do README: ABCOLLAB_SCREENSHOTS=<folder> go test -run Screenshots
// zapisuje ekrany (ANSI) na przykladowych danych; PNG robi z nich freeze.
func TestScreenshots(t *testing.T) {
	out := os.Getenv("ABCOLLAB_SCREENSHOTS")
	if out == "" {
		t.Skip("set ABCOLLAB_SCREENSHOTS to write screenshots")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	clock := time.Date(2026, 10, 11, 21, 14, 0, 0, time.Local)
	save := func(name string, a App) {
		if err := os.WriteFile(filepath.Join(out, name+".ansi"), []byte(a.View()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// pusty start
	be := &fakeBackend{t: t, me: store.NewDevice("alex"), net: &fakeNet{}, settings: Settings{AutoSync: true}}
	a := newApp(t, be, Options{})
	a.w, a.h = 118, 32
	save("empty", a)

	// sesja w trakcie pracy
	me := store.NewDevice("alex")
	me.Peer = idHost
	dir := filepath.Join(t.TempDir(), "night drive")
	for _, f := range []string{"night drive Project/night drive.als", "Samples/hihat 808.wav", "Samples/kick punchy.wav",
		"Samples/vox take 2.wav", "Recordings/guitar riff.wav", "Recordings/pad.wav"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)
		data := []byte("x")
		if filepath.Ext(f) == ".als" {
			data = tinySet()
		}
		os.WriteFile(filepath.Join(dir, f), data, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "Samples", "vox take 2.wav"), make([]byte, 2<<20), 0o644)
	if _, _, err := store.Init(dir, "night drive Project/night drive.als", me); err != nil {
		t.Fatal(err)
	}
	net := &fakeNet{lobby: syncthing.Lobby{Me: idHost,
		Members:  []syncthing.Member{{ID: idOther, Name: "sam", Connected: true, Via: "LAN"}, {ID: idGuest, Name: "jo", Connected: true, Via: "relay"}},
		Requests: []syncthing.Request{{ID: "MIKAAAA-BBBBBBB-CCCCCCC-DDDDDDD-EEEEEEE-FFFFFFF-GGGGGGG-HHHHHHH", Name: "mika", Time: clock}},
		Sync:     &syncthing.FolderStatus{State: "idle"}}}
	ls := newFakeLiveSync()
	be = &fakeBackend{t: t, me: me, net: net, live: ls, settings: Settings{AutoSync: true, AutoSave: true}}
	a = newApp(t, be, Options{Last: dir})
	a.w, a.h = 118, 32
	a.now = func() time.Time { return clock }
	a = liveNote(t, a, live.Note{Kind: "status"})
	a.ses.log = nil
	at := func(m int) time.Time { return clock.Add(time.Duration(m-14) * time.Minute) }
	a.ses.log = []logLine{
		{at(2), lInfo, "Opened session night drive"},
		{at(3), lOK, "Live connected - changes now go live"},
		{at(3), lOK, "sam connected"},
		{at(5), lOK, `sam changed Drums slot 1 clip "hats"`},
		{at(6), lFile, "↓ Samples/kick punchy.wav received"},
		{at(9), lFile, "↑ Bass mixer"},
		{at(11), lOK, "jo changed tempo (128 BPM)"},
		{at(12), lFile, "↑ Samples/vox take 2.wav → sending to others"},
		{at(13), lOK, "Live saved the set - shared as version 3f9c2a1b07"},
		{at(14), lWarn, "mika (MIKAAAA) wants to join"},
	}
	a.ses.tree.open["Samples"] = true
	a.ses.tree.sel = "Samples/vox take 2.wav"
	a.focus = fFiles
	save("session", a)

	// konflikt na zywo
	hats := func(extra ...float64) live.Obj {
		var ns []any
		for i := 0; i < 16; i++ {
			v := 100.0
			if i%2 == 1 {
				v = 60
			}
			ns = append(ns, []any{42.0, float64(i) * 0.25, 0.2, v, false, 1.0, 0.0, 64.0})
		}
		ns = append(ns, []any{36.0, 0.0, 0.5, 110.0, false, 1.0, 0.0, 64.0}, []any{36.0, 2.0, 0.5, 110.0, false, 1.0, 0.0, 64.0})
		for _, x := range extra {
			ns = append(ns, []any{46.0, x, 0.25, 90.0, false, 1.0, 0.0, 64.0})
		}
		return live.Obj{"track": "Drums", "name": "hats", "midi": true, "loop": []any{0.0, 4.0}, "notes": ns}
	}
	a = liveNote(t, a, live.Note{Kind: "conflict", Key: "clip/0/s0", Label: `Drums slot 1 clip "hats"`,
		Mine: hats(1.75, 3.75), Theirs: hats(0.75, 1.5, 2.75, 3.5), From: "sam"})
	save("conflict", a)

	// dolaczanie z wlasna kopia projektu
	be = &fakeBackend{t: t, me: store.NewDevice("sam"), net: &fakeNet{}, settings: Settings{AutoSync: true}}
	a = newApp(t, be, Options{})
	a.w, a.h = 118, 32
	jd := newJoinDialog(&a)
	jd.inv = syncthing.Invite{Device: idHost, Folder: "abcollab-abc", Name: "night drive", Host: "alex"}
	jd.step = jAction
	jd.info = FolderInfo{Dir: `C:\Users\sam\Music\night drive`, Sets: []string{"night drive Project/night drive.als"}, Files: 41, Bytes: 380 << 20}
	a.dlg = jd
	save("join", a)
}
