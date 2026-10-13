package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/internal/live"
	"github.com/c-airr/ableton-collab/store"
)

// fakeLive: silnik pracy na zywo w tescie - notatki podajemy recznie.
type fakeLiveSync struct {
	notes    chan live.Note
	st       live.Status
	resolved map[string]bool
	rechecks int
	caught   [][]live.Catch
}

// Kanal notatek jest zamkniety: komenda czekajaca na niego konczy sie od razu
// (pomocnik drive wykonuje komendy po kolei), notatki podaje liveNote.
func newFakeLiveSync() *fakeLiveSync {
	ch := make(chan live.Note)
	close(ch)
	return &fakeLiveSync{notes: ch, st: live.Status{Connected: true, Matched: true}, resolved: map[string]bool{}}
}

func (f *fakeLiveSync) Notes() <-chan live.Note       { return f.notes }
func (f *fakeLiveSync) Status() live.Status           { return f.st }
func (f *fakeLiveSync) Resolve(key string, mine bool) { f.resolved[key] = mine }
func (f *fakeLiveSync) Recheck()                      { f.rechecks++ }
func (f *fakeLiveSync) Catchup(items []live.Catch)    { f.caught = append(f.caught, items) }
func (f *fakeLiveSync) Close()                        {}

// liveNote: notatka od silnika przechodzi przez Update jak w programie.
func liveNote(t *testing.T, a App, n live.Note) App {
	next, cmd := a.Update(liveMsg{sid: a.sid, note: n, ok: true})
	a = next.(App)
	_ = cmd // kolejne czekanie na kanal - pomijamy
	return a
}

func liveApp(t *testing.T, set Settings) (App, *fakeBackend, *fakeLiveSync, string) {
	me := store.NewDevice("Ja")
	dir := sessionDir(t, tinySet())
	if _, _, err := store.Init(dir, "beat Project/beat.als", me); err != nil {
		t.Fatal(err)
	}
	ls := newFakeLiveSync()
	be := &fakeBackend{t: t, me: me, net: &fakeNet{}, live: ls, settings: set}
	a := newApp(t, be, Options{Last: dir})
	a = liveNote(t, a, live.Note{Kind: "status"})
	return a, be, ls, dir
}

func clipObj(keys ...float64) live.Obj {
	var ns []any
	for i, k := range keys {
		ns = append(ns, []any{k, float64(i), 1.0, 100.0, false, 1.0, 0.0, 64.0})
	}
	return live.Obj{"track": "Hats", "name": "hh", "midi": true, "loop": []any{0.0, 4.0}, "notes": ns}
}

func TestLiveStatusAndLog(t *testing.T) {
	a, _, _, _ := liveApp(t, Settings{AutoSync: true})
	dump(t, a)
	must(t, a, "◉ Live: live sync on", "auto")
	mustLog(t, a, "Live connected")
	a = liveNote(t, a, live.Note{Kind: "log", Text: `Kolega changed Hats slot 1 clip "hh"`})
	a = liveNote(t, a, live.Note{Kind: "local", Text: "Bass mixer"})
	mustLog(t, a, `Kolega changed Hats slot 1 clip "hh"`, "↑ Bass mixer")
}

func TestLiveConflictDialog(t *testing.T) {
	a, _, ls, _ := liveApp(t, Settings{})
	a = liveNote(t, a, live.Note{Kind: "conflict", Key: "clip/0/s0", Label: `Hats slot 1 clip "hh"`,
		Mine: clipObj(60, 62), Theirs: clipObj(60, 67), From: "Kolega"})
	dump(t, a)
	must(t, a, "Live conflict with Kolega", "YOURS", "KOLEGA'S", "Keep mine", "Keep Kolega's", "C3")
	a = press(t, a, "down", "enter")
	if mine, ok := ls.resolved["clip/0/s0"]; !ok || mine {
		t.Fatalf("resolved %v", ls.resolved)
	}
	if a.dlg != nil {
		t.Fatal("dialog still open")
	}
}

func TestAutoSaveInLive(t *testing.T) {
	a, be, _, _ := liveApp(t, Settings{AutoSave: true})
	clock := time.Now()
	a.now = func() time.Time { return clock }
	a.ses.now = a.now
	a = liveNote(t, a, live.Note{Kind: "local", Text: "Hats clip"})
	a = tickApp(t, a)
	if be.saves != 0 {
		t.Fatal("saved while still editing")
	}
	clock = clock.Add(5 * time.Second)
	a = tickApp(t, a)
	if be.saves != 1 {
		t.Fatalf("auto-save not sent (%d)", be.saves)
	}
	mustLog(t, a, "Saved in Live (Ctrl+S)")
	clock = clock.Add(5 * time.Second)
	a = tickApp(t, a)
	if be.saves != 1 {
		t.Fatal("saved again without new changes")
	}
}

func TestAutoSyncWhenLiveSaves(t *testing.T) {
	a, _, ls, dir := liveApp(t, Settings{AutoSync: true})
	// Live zapisuje set z nowa trescia (inny set testowy)
	path := a.ses.p.WorkingPath()
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, testdata(t, "base.als"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	os.Chtimes(path, future, future)
	a = tickApp(t, a)
	mustLog(t, a, "Live saved the set - sent as version")
	if ls.rechecks == 0 {
		t.Error("no recheck after save")
	}
	_ = dir
}

func TestLiveSyncable(t *testing.T) {
	c := func(path ...string) als.Change { return als.Change{Kind: "modified", Path: path} }
	yes := []als.Change{
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "MainSequencer", "ClipSlotList", "ClipSlot#0", "ClipSlot", "Value", "MidiClip#0", "Notes", "KeyTracks", "60", "Notes", "6"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "Mixer", "Pan", "Manual"),
		c("Ableton", "LiveSet", "MainTrack", "DeviceChain", "Mixer", "Tempo", "Manual"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "MainSequencer", "ClipSlotList", "ClipSlot#1", "ClipSlot", "Value", "MidiClip#0"),
	}
	no := []als.Change{
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "DeviceChain", "Devices", "PluginDevice#3"),
		c("Ableton", "LiveSet", "Tracks", "AudioTrack#2", "DeviceChain", "MainSequencer", "ClipSlotList", "ClipSlot#0", "ClipSlot", "Value", "AudioClip#0", "WarpMarkers"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#9"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "Mixer", "Volume", "Manual"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "MainSequencer", "ClipSlotList", "ClipSlot#0", "ClipSlot", "Value", "MidiClip#0", "Color"),
	}
	for _, x := range yes {
		if !liveSyncable(x) {
			t.Errorf("should be live: %s", strings.Join(x.Path, "/"))
		}
	}
	for _, x := range no {
		if liveSyncable(x) {
			t.Errorf("shouldn't be live: %s", strings.Join(x.Path, "/"))
		}
	}
}

func TestSettingsToggles(t *testing.T) {
	be := &fakeBackend{t: t, me: store.NewDevice("Ja"), net: &fakeNet{}, settings: Settings{AutoSync: true}}
	a := newApp(t, be, Options{})
	a = press(t, a, "o")
	must(t, a, "Automatic sync: ON", "Auto-save in Live: OFF")
	a = press(t, a, "down", "down", "enter") // auto-save
	if !be.settings.AutoSave || !a.settings.AutoSave {
		t.Fatal("auto-save not switched on")
	}
	must(t, a, "Auto-save in Live: ON")
}

// Live ma inny set: wyrazny komunikat i przycisk Open in Live.
func TestOpenInLive(t *testing.T) {
	a, be, ls, _ := liveApp(t, Settings{AutoSync: true})
	ls.st = live.Status{Connected: true, Detail: "Live has a different set open - open this session's set"}
	a = liveNote(t, a, live.Note{Kind: "status"})
	must(t, a, "Live has a different set open", "Open in Live")
	a = press(t, a, "e")
	if be.opened != a.ses.p.WorkingPath() {
		t.Fatalf("opened %q", be.opened)
	}
	if be.opened == "" {
		t.Fatalf("opened %q", be.opened)
	}
	mustLog(t, a, "Opening")
	// set otwarty - przycisk znika
	ls.st = live.Status{Connected: true, Matched: true}
	a = liveNote(t, a, live.Note{Kind: "status"})
	if strings.Contains(view(a), "Open in Live") {
		t.Fatal("Open in Live still shown with the right set open")
	}
}

// Open in Live najpierw pobiera nowsza wersje kolegi - inaczej Live otworzylby
// stary plik, a zmiany sprzed polaczenia nie przyszlyby juz na zywo.
func TestOpenInLiveGetsLatest(t *testing.T) {
	a, be, ls, dir := liveApp(t, Settings{AutoSync: true})
	// kolega w swojej kopii zapisuje nowa wersje
	other := t.TempDir()
	copyDir(t, filepath.Join(dir, store.Dir), filepath.Join(other, store.Dir))
	kp, err := store.Open(other, store.NewDevice("Kolega"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kp.Pull(nil); err != nil {
		t.Fatal(err)
	}
	newer := testdata(t, "base.als")
	os.WriteFile(kp.WorkingPath(), newer, 0o644)
	if v, err := kp.Commit(); err != nil || v == nil {
		t.Fatalf("%v %v", v, err)
	}
	copyDir(t, filepath.Join(other, store.Dir, "devices", kp.Me.ID), filepath.Join(dir, store.Dir, "devices", kp.Me.ID))

	ls.st = live.Status{Connected: true, Detail: "Live has a different set open"}
	a = liveNote(t, a, live.Note{Kind: "status"})
	a = press(t, a, "e")
	if be.opened == "" {
		t.Fatal("not opened")
	}
	if got, _ := os.ReadFile(a.ses.p.WorkingPath()); !store.SameSet(got, newer) {
		t.Fatal("opened an old set - the newer version wasn't pulled first")
	}
	mustLog(t, a, "Got the latest version before opening")
}

// Nowa wersja kolegi przychodzi sama (bez Get changes). Plik seta jest
// podmieniany tylko wtedy, gdy Live ma otwarty inny set.
func TestAutoPullModes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		st        live.Status
		fileTaken bool // plik seta podmieniony
	}{
		{"live on - changes come live", live.Status{Connected: true, Matched: true}, false},
		{"other set open - file updated", live.Status{Connected: true, Detail: "different set"}, true},
		{"Live not connected - file untouched", live.Status{Detail: "not connected"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, ls, dir := liveApp(t, Settings{AutoSync: true})
			ls.st = tc.st
			a = liveNote(t, a, live.Note{Kind: "status"})
			other := t.TempDir()
			copyDir(t, filepath.Join(dir, store.Dir), filepath.Join(other, store.Dir))
			kp, _ := store.Open(other, store.NewDevice("Kolega"))
			kp.Pull(nil)
			newer := testdata(t, "base.als")
			os.WriteFile(kp.WorkingPath(), newer, 0o644)
			if v, err := kp.Commit(); err != nil || v == nil {
				t.Fatalf("%v %v", v, err)
			}
			copyDir(t, filepath.Join(other, store.Dir, "devices", kp.Me.ID), filepath.Join(dir, store.Dir, "devices", kp.Me.ID))
			before, _ := os.ReadFile(a.ses.p.WorkingPath())
			a = tickApp(t, a) // odswiezenie widzi nowa wersje
			a = tickApp(t, a) // auto pobranie
			mustLog(t, a, "New version from Kolega - getting it")
			if a.ses.st == nil || a.ses.st.Peers[0].Relation == store.RelAhead {
				t.Fatal("not pulled automatically")
			}
			now, _ := os.ReadFile(a.ses.p.WorkingPath())
			if taken := !bytes.Equal(now, before); taken != tc.fileTaken {
				t.Fatalf("set file replaced=%v, want %v", taken, tc.fileTaken)
			}
			if !tc.fileTaken && !tc.st.Matched {
				must(t, a, "new change(s) from others are in")
			}
			// Live ma ten set: zmiany z wersji ida do niego od razu, raz
			a = tickApp(t, a)
			a = tickApp(t, a)
			if tc.st.Matched && (len(ls.caught) != 1 || len(ls.caught[0]) == 0) {
				t.Fatalf("changes from the version not brought into Live: %v", ls.caught)
			}
			if !tc.st.Matched && len(ls.caught) != 0 {
				t.Fatal("brought changes into a Live without this set")
			}
		})
	}
}
