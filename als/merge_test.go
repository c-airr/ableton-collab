package als

import (
	"strings"
	"testing"
)

func mustMerge(t *testing.T, b, o, th tSet, res map[string]Resolution) *Result {
	t.Helper()
	r, err := MergeWith(b.doc(t), o.doc(t), th.doc(t), res)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if r.Doc != nil {
		if errs := Validate(r.Doc); len(errs) > 0 {
			t.Fatalf("wynik niespojny: %v", errs)
		}
		// wynik musi sie dac zapisac i wczytac bez zmian
		if _, err := ParseXML(strings.NewReader(string(r.Doc.XMLBytes()))); err != nil {
			t.Fatalf("wynik nie parsuje sie: %v", err)
		}
	}
	return r
}

// Ty zmieniasz melodie, kolega hihaty - scala sie bez pytan.
func TestMergeDifferentClips(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[0].slots[0].notes[1].vel = 50 // nuta #2 melodii
	th := b.copy()
	th.tracks[1].slots[0].notes = append(th.tracks[1].slots[0].notes, n(3, 42, "1", 80))

	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 {
		t.Fatalf("nieoczekiwany konflikt: %v", r.Conflicts)
	}
	if got := notesOf(t, r.Doc, "Melodia", 0); !eqStrings(got, []string{"1:60@0 v=100", "2:62@1 v=50", "3:64@2 v=100"}) {
		t.Errorf("melodia: %v", got)
	}
	if got := notesOf(t, r.Doc, "Hats", 0); len(got) != 3 || got[2] != "3:42@1 v=80" {
		t.Errorf("hats: %v", got)
	}
}

// Zmiana mikseru i zmiana klipu tej samej sciezki to rozne jednostki.
func TestMergeMixerVsClipSameTrack(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[0].volume = "0.5"
	th := b.copy()
	th.tracks[0].slots[0].notes[0].vel = 10
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 {
		t.Fatalf("konflikt: %v", r.Conflicts)
	}
	if v := r.Doc.Root.Find("LiveSet", "Tracks").Children[0].Value("DeviceChain", "Mixer", "Volume", "Manual"); v != "0.5" {
		t.Errorf("volume = %s", v)
	}
	if got := notesOf(t, r.Doc, "Melodia", 0); got[0] != "1:60@0 v=10" {
		t.Errorf("nuty: %v", got)
	}
}

func sameClipConflict() (b, o, th tSet) {
	b = baseSet()
	o = b.copy()
	o.tracks[1].slots[0].notes[0].vel = 127 // nuta #1
	th = b.copy()
	th.tracks[1].slots[0].notes[1].vel = 1 // inna nuta (#2) w TYM SAMYM klipie
	return
}

// Obie strony zmieniaja ten sam klip (nawet rozne nuty) = konflikt, bez wyniku.
func TestConflictSameClip(t *testing.T) {
	b, o, th := sameClipConflict()
	r := mustMerge(t, b, o, th, nil)
	if r.Doc != nil {
		t.Fatal("przy konflikcie nie powinno byc wyniku")
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("konflikty: %d %v", len(r.Conflicts), r.Conflicts)
	}
	c := r.Conflicts[0]
	if !c.CanBoth || !strings.Contains(c.UnitLabel, `MidiTrack "Hats"`) || !strings.Contains(c.UnitLabel, "MidiClip") {
		t.Errorf("konflikt: %+v", c)
	}
	if len(c.Ours) != 1 || len(c.Theirs) != 1 {
		t.Errorf("zmiany w konflikcie: ours=%v theirs=%v", c.Ours, c.Theirs)
	}
}

func TestResolveOursTheirs(t *testing.T) {
	b, o, th := sameClipConflict()
	id := mustMerge(t, b, o, th, nil).Conflicts[0].ID

	r := mustMerge(t, b, o, th, map[string]Resolution{id: Ours})
	if got := notesOf(t, r.Doc, "Hats", 0); !eqStrings(got, []string{"1:42@0 v=127", "2:42@0.5 v=70"}) {
		t.Errorf("ours: %v", got)
	}
	r = mustMerge(t, b, o, th, map[string]Resolution{id: Theirs})
	if got := notesOf(t, r.Doc, "Hats", 0); !eqStrings(got, []string{"1:42@0 v=90", "2:42@0.5 v=1"}) {
		t.Errorf("theirs: %v", got)
	}
}

// "Oba": klip ours zostaje w slocie, klip theirs laduje w pierwszym wolnym.
func TestResolveBoth(t *testing.T) {
	b, o, th := sameClipConflict()
	id := mustMerge(t, b, o, th, nil).Conflicts[0].ID
	r := mustMerge(t, b, o, th, map[string]Resolution{id: Both})
	if got := notesOf(t, r.Doc, "Hats", 0); got[0] != "1:42@0 v=127" {
		t.Errorf("slot 0 (ours): %v", got)
	}
	if got := notesOf(t, r.Doc, "Hats", 1); !eqStrings(got, []string{"1:42@0 v=90", "2:42@0.5 v=1"}) {
		t.Errorf("slot 1 (theirs): %v", got)
	}
}

func TestResolveBothNoFreeSlot(t *testing.T) {
	b, o, th := sameClipConflict()
	for _, s := range []*tSet{&b, &o, &th} {
		s.tracks[1].slots = s.tracks[1].slots[:1]
	}
	id := mustMerge(t, b, o, th, nil).Conflicts[0].ID
	if _, err := MergeWith(b.doc(t), o.doc(t), th.doc(t), map[string]Resolution{id: Both}); err == nil ||
		!strings.Contains(err.Error(), "no free slot") {
		t.Fatalf("oczekiwany blad braku slotu, jest: %v", err)
	}
}

// Kolega usuwa sciezke, ty zmieniasz klip na niej = konflikt na calej sciezce.
func TestConflictDeleteVsEdit(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[1].slots[0].notes[0].vel = 1
	th := b.copy()
	th.tracks = th.tracks[:1]
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) != 1 {
		t.Fatalf("konflikty: %v", r.Conflicts)
	}
	c := r.Conflicts[0]
	if c.CanBoth || !strings.HasSuffix(c.UnitLabel, `MidiTrack "Hats"`) {
		t.Errorf("konflikt: %+v", c)
	}
	// "ich" = sciezka znika; "moje" = zostaje ze zmiana
	r = mustMerge(t, b, o, th, map[string]Resolution{c.ID: Theirs})
	if len(tracks(r.Doc)) != 1 {
		t.Errorf("theirs: %d sciezek", len(tracks(r.Doc)))
	}
	r = mustMerge(t, b, o, th, map[string]Resolution{c.ID: Ours})
	if got := notesOf(t, r.Doc, "Hats", 0); got[0] != "1:42@0 v=1" {
		t.Errorf("ours: %v", got)
	}
}

// Usuniecie nieruszanej przez druga strone sciezki to zwykla zmiana.
func TestDeleteUntouchedTrack(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[0].slots[0].notes[0].vel = 1
	th := b.copy()
	th.tracks = th.tracks[:1]
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 || len(tracks(r.Doc)) != 1 {
		t.Fatalf("konflikty=%v sciezki=%d", r.Conflicts, len(tracks(r.Doc)))
	}
}

// Ta sama zmiana po obu stronach nie jest konfliktem.
func TestSameChangeBothSides(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[1].slots[0].notes[0].vel = 5
	th := o.copy()
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 {
		t.Fatalf("konflikt: %v", r.Conflicts)
	}
}

// Obie strony dodaja sciezke z TYM SAMYM Id i nachodzacymi Id pointee
// (jak gitary w testdata: obie alokuja od NextPointeeId bazy).
func TestBothAddTrackSameIDs(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.next = 1010
	o.tracks = append(o.tracks, tTrack{id: 12, name: "Gitara", at: 1001, extra: `<Pointee Id="1000" /><Pointee Id="1009" />`})
	th := b.copy()
	th.next = 1005
	th.tracks = append(th.tracks, tTrack{id: 12, name: "Bas", at: 1001, extra: `<Pointee Id="1000" /><Pointee Id="1004" />`})

	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 {
		t.Fatalf("konflikt: %v", r.Conflicts)
	}
	var names []string
	ids := map[string]bool{}
	for _, tr := range tracks(r.Doc) {
		names = append(names, trackName(tr))
		id, _ := tr.Attr("Id")
		ids[id] = true
	}
	if !eqStrings(names, []string{"Melodia", "Hats", "Gitara", "Bas"}) || len(ids) != 4 {
		t.Errorf("sciezki: %v ids: %v", names, ids)
	}
	if np := nextPointee(r.Doc); np != 1015 {
		t.Errorf("NextPointeeId = %d, oczekiwane 1010+5", np)
	}
}

// Duplikat: obie strony dodaly sciezke o tej samej nazwie i instrumencie.
func TestDuplicateTrackWarning(t *testing.T) {
	b := baseSet()
	dev := `<OriginalSimpler Id="0"><UserName Value="" /></OriginalSimpler>`
	o := b.copy()
	o.tracks = append(o.tracks, tTrack{id: 12, name: "Gitara", devices: dev})
	th := b.copy()
	th.tracks = append(th.tracks, tTrack{id: 13, name: "Gitara", devices: dev})
	r := mustMerge(t, b, o, th, nil)
	if len(tracks(r.Doc)) != 4 || len(r.Warnings) != 1 || r.Warnings[0].Kind != "duplicate-track" {
		t.Fatalf("sciezki=%d ostrzezenia=%v", len(tracks(r.Doc)), r.Warnings)
	}
}

// Same zmiany szumu (pozycja odtwarzania, zaznaczenie) nie sa zmianami.
func TestNoiseOnly(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.noise = `<HighlightedTrackIndex Value="3" />`
	b.noise = `<HighlightedTrackIndex Value="0" />`
	if ch := Diff(b.doc(t), o.doc(t)); len(ch) > 0 {
		t.Fatalf("szum widoczny w diffie: %v", ch)
	}
	th := b.copy()
	th.noise = `<HighlightedTrackIndex Value="1" />`
	th.tracks[0].slots[0].notes[0].vel = 3
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 {
		t.Fatalf("konflikt na szumie: %v", r.Conflicts)
	}
	if got := notesOf(t, r.Doc, "Melodia", 0); got[0] != "1:60@0 v=3" {
		t.Errorf("zmiana theirs zgubiona: %v", got)
	}
}

// Dryf NoteId: u nas nuta #2 hihatow ma po API Live'a nowe ID (#7), ale te
// sama tresc. Kolega zmienia velocity tej nuty. Bez normalizacji wygladaloby
// to jak "usuniete w ours, zmienione w theirs".
func TestNoteIDDrift(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[1].slots[0].notes[1].id = 7
	o.tracks[0].slots[0].notes[0].vel = 1 // inna, prawdziwa zmiana
	th := b.copy()
	th.tracks[1].slots[0].notes[1].vel = 33

	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 {
		t.Fatalf("falszywy konflikt: %v", r.Conflicts)
	}
	if r.NoteIDsNormalized != 1 {
		t.Errorf("NoteIDsNormalized = %d", r.NoteIDsNormalized)
	}
	if got := notesOf(t, r.Doc, "Hats", 0); !eqStrings(got, []string{"1:42@0 v=90", "2:42@0.5 v=33"}) {
		t.Errorf("hats: %v", got)
	}
}

// Prawdziwie nowa nuta (inna tresc) nie moze zostac "dopasowana" do usunietej.
func TestNoteIDDriftOnlyIdentical(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[1].slots[0].notes[1] = n(7, 42, "0.75", 70) // usunieta #2, dodana inna
	od := o.doc(t)
	if NormalizeNoteIDs(b.doc(t), od) != 0 {
		t.Fatal("dopasowano rozne nuty")
	}
	if ch := Diff(b.doc(t), od); len(ch) != 2 {
		t.Errorf("oczekiwane removed+added, jest %v", ch)
	}
}

// Stan wtyczki to blob: zmiana po obu stronach = konflikt na urzadzeniu,
// zmiana po jednej - przechodzi w calosci.
func TestPluginBlob(t *testing.T) {
	plug := func(state string) string {
		return `<PluginDevice Id="0"><UserName Value="" /><PluginDesc><Vst3PluginInfo Id="0"><Name Value="Synth" />` +
			`<Preset><Vst3Preset Id="0"><ProcessorState>` + state + `</ProcessorState></Vst3Preset></Preset>` +
			`</Vst3PluginInfo></PluginDesc></PluginDevice>`
	}
	b := baseSet()
	b.tracks[0].devices = plug("AAAA")
	o := b.copy()
	o.tracks[0].devices = plug("BBBB")
	th := b.copy()
	th.tracks[0].devices = plug("CCCC")
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) != 1 || !strings.Contains(r.Conflicts[0].UnitLabel, `PluginDevice "Synth"`) || r.Conflicts[0].CanBoth {
		t.Fatalf("konflikty: %+v", r.Conflicts)
	}
	th = b.copy()
	th.tracks[1].slots[0].notes[0].vel = 2
	r = mustMerge(t, b, o, th, nil)
	if !strings.Contains(string(r.Doc.XMLBytes()), "BBBB") {
		t.Error("blob ours zgubiony")
	}
}

// Kolega zmienia kolejnosc sciezek, my edytujemy klip - kolejnosc theirs
// i nasza zmiana.
func TestReorderAndEdit(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[0].slots[0].notes[0].vel = 4
	th := b.copy()
	th.tracks[0], th.tracks[1] = th.tracks[1], th.tracks[0]
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) > 0 {
		t.Fatalf("konflikt: %v", r.Conflicts)
	}
	if trs := tracks(r.Doc); trackName(trs[0]) != "Hats" {
		t.Error("kolejnosc theirs zgubiona")
	}
	if got := notesOf(t, r.Doc, "Melodia", 0); got[0] != "1:60@0 v=4" {
		t.Errorf("zmiana ours zgubiona: %v", got)
	}
}

// Nowe elementy laduja obok swoich poprzednikow, nie na koncu.
func TestInsertPositions(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks = []tTrack{o.tracks[0], {id: 20, name: "Nowa ours"}, o.tracks[1]}
	th := b.copy()
	th.tracks = append(th.tracks, tTrack{id: 21, name: "Nowa theirs"})
	r := mustMerge(t, b, o, th, nil)
	var names []string
	for _, tr := range tracks(r.Doc) {
		names = append(names, trackName(tr))
	}
	if !eqStrings(names, []string{"Melodia", "Nowa ours", "Hats", "Nowa theirs"}) {
		t.Errorf("kolejnosc: %v", names)
	}
}

// Obie strony wstawiaja klip do tego samego pustego slotu.
func TestConflictBothAddClipSameSlot(t *testing.T) {
	b := baseSet()
	o := b.copy()
	o.tracks[0].slots[1] = &tClip{name: "A", notes: []tNote{n(1, 60, "0", 1)}}
	th := b.copy()
	th.tracks[0].slots[1] = &tClip{name: "B", notes: []tNote{n(1, 61, "0", 1)}}
	r := mustMerge(t, b, o, th, nil)
	if len(r.Conflicts) != 1 || !r.Conflicts[0].CanBoth {
		t.Fatalf("konflikty: %+v", r.Conflicts)
	}
	r = mustMerge(t, b, o, th, map[string]Resolution{r.Conflicts[0].ID: Both})
	if a, c := notesOf(t, r.Doc, "Melodia", 1), notesOf(t, r.Doc, "Melodia", 2); a[0] != "1:60@0 v=1" || c[0] != "1:61@0 v=1" {
		t.Errorf("slot1=%v slot2=%v", a, c)
	}
}

func TestParseResolution(t *testing.T) {
	for in, want := range map[string]Resolution{"o": Ours, "moje": Ours, "theirs": Theirs, "ich": Theirs, "B": Both, " oba ": Both} {
		if got, err := ParseResolution(in); err != nil || got != want {
			t.Errorf("%q -> %v %v", in, got, err)
		}
	}
	if _, err := ParseResolution("x"); err == nil {
		t.Error("oczekiwany blad")
	}
}

func TestAssetWarnings(t *testing.T) {
	ref := func(path, rel, pack string) string {
		return `<SampleRef><FileRef><RelativePath Value="` + rel + `" /><Path Value="` + path +
			`" /><LivePackName Value="` + pack + `" /></FileRef></SampleRef>`
	}
	b := baseSet()
	b.tracks[0].devices = `<OriginalSimpler Id="0">` +
		ref("C:/Muzyka/Projekt/Samples/Imported/kick.wav", "Samples/Imported/kick.wav", "") +
		ref("D:/Sample/snare.wav", "", "") +
		ref("C:/ProgramData/Ableton/Core Library/hat.wav", "x", "Core Library") +
		ref("C:/Packs/Drum Booth/tom.wav", "y", "Drum Booth") +
		`</OriginalSimpler>`
	d := b.doc(t)
	where := map[string]string{}
	for _, s := range Samples(d, `C:\Muzyka\Projekt`) {
		where[s.Path] = s.Where
	}
	if where["C:/Muzyka/Projekt/Samples/Imported/kick.wav"] != "in-project" || where["D:/Sample/snare.wav"] != "external" ||
		where["C:/Packs/Drum Booth/tom.wav"] != "pack" {
		t.Errorf("klasyfikacja: %v", where)
	}
	ws := AssetWarnings(d, `C:\Muzyka\Projekt`)
	kinds := map[string]int{}
	for _, w := range ws {
		kinds[w.Kind]++
	}
	if kinds["external-sample"] != 1 || kinds["pack-sample"] != 1 {
		t.Errorf("ostrzezenia: %v", ws)
	}
}

// Live pod Wine zapisuje sciezki Z:\home\..., a folder projektu jest linuksowy.
func TestSamplesUnderWine(t *testing.T) {
	b := baseSet()
	b.tracks[0].devices = `<OriginalSimpler Id="0"><SampleRef><FileRef><RelativePath Value="Samples/a.wav" />` +
		`<Path Value="Z:\home\bartek\beat\Samples\a.wav" /><LivePackName Value="" /></FileRef></SampleRef></OriginalSimpler>`
	ss := Samples(b.doc(t), "/home/bartek/beat")
	if len(ss) != 1 || ss[0].Where != "in-project" {
		t.Fatalf("%+v", ss)
	}
}
