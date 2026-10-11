package tui

import (
	"os"
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
	must(t, a, "♪ Live: live sync on", "auto")
	mustLog(t, a, "Live connected")
	a = liveNote(t, a, live.Note{Kind: "log", Text: `Kolega changed Hats slot 1 clip "hh"`})
	a = liveNote(t, a, live.Note{Kind: "local", Text: "Bass mixer"})
	mustLog(t, a, `Kolega changed Hats slot 1 clip "hh"`, "⇡ Bass mixer")
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
	mustLog(t, a, "Live saved the set - shared as version")
	if ls.rechecks == 0 {
		t.Error("no recheck after save")
	}
	_ = dir
}

func TestLiveSyncable(t *testing.T) {
	c := func(path ...string) als.Change { return als.Change{Kind: "modified", Path: path} }
	yes := []als.Change{
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "MainSequencer", "ClipSlotList", "ClipSlot#0", "ClipSlot", "Value", "MidiClip#0", "Notes", "KeyTracks", "60", "Notes", "6"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "Mixer", "Volume", "Manual"),
		c("Ableton", "LiveSet", "MainTrack", "DeviceChain", "Mixer", "Tempo", "Manual"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "MainSequencer", "ClipSlotList", "ClipSlot#1", "ClipSlot", "Value", "MidiClip#0"),
	}
	no := []als.Change{
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#16", "DeviceChain", "DeviceChain", "Devices", "PluginDevice#3"),
		c("Ableton", "LiveSet", "Tracks", "AudioTrack#2", "DeviceChain", "MainSequencer", "ClipSlotList", "ClipSlot#0", "ClipSlot", "Value", "AudioClip#0", "WarpMarkers"),
		c("Ableton", "LiveSet", "Tracks", "MidiTrack#9"),
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
