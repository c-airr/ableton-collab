package als

import (
	"fmt"
	"strings"
	"testing"
)

// Minimalny LiveSet do testow jednostkowych. Struktura odwzorowuje to, co
// widac w prawdziwych plikach (testdata): Tracks > MidiTrack Id > Name,
// DeviceChain/MainSequencer/ClipSlotList/ClipSlot Id/ClipSlot/Value/MidiClip,
// Notes/KeyTracks/KeyTrack/Notes/MidiNoteEvent + MidiKey, Mixer/Volume/Manual.

type tNote struct {
	id, key int
	time    string
	vel     int
}

type tClip struct {
	name  string
	notes []tNote
}

type tTrack struct {
	id      int
	name    string
	volume  string
	slots   []*tClip // nil = pusty slot
	devices string   // surowy XML wnetrza <Devices>
	extra   string   // surowy XML dopisany na koncu sciezki
	at      int      // Id AutomationTarget glosnosci (0 = id*10)
}

type tSet struct {
	next   int // NextPointeeId
	tracks []tTrack
	noise  string // np. CurrentTime
}

func n(id, key int, time string, vel int) tNote { return tNote{id, key, time, vel} }

func (s tSet) xml() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?><Ableton MajorVersion="5"><LiveSet>`)
	fmt.Fprintf(&b, `<NextPointeeId Value="%d" /><Tracks>`, s.next)
	for _, t := range s.tracks {
		fmt.Fprintf(&b, `<MidiTrack Id="%d"><Name><EffectiveName Value="%s" /><UserName Value="" /></Name>`, t.id, t.name)
		at := t.at
		if at == 0 {
			at = t.id * 10
		}
		vol := t.volume
		if vol == "" {
			vol = "1"
		}
		fmt.Fprintf(&b, `<DeviceChain><Mixer><Volume><Manual Value="%s" /><AutomationTarget Id="%d" /></Volume></Mixer>`, vol, at)
		b.WriteString(`<MainSequencer><ClipSlotList>`)
		for i, c := range t.slots {
			fmt.Fprintf(&b, `<ClipSlot Id="%d"><ClipSlot><Value>`, i)
			if c != nil {
				b.WriteString(c.xml())
			}
			b.WriteString(`</Value></ClipSlot></ClipSlot>`)
		}
		b.WriteString(`</ClipSlotList></MainSequencer>`)
		fmt.Fprintf(&b, `<DeviceChain><Devices>%s</Devices></DeviceChain></DeviceChain>%s</MidiTrack>`, t.devices, t.extra)
	}
	fmt.Fprintf(&b, `</Tracks><Transport><CurrentTime Value="0" /></Transport>%s</LiveSet></Ableton>`, s.noise)
	return b.String()
}

func (c *tClip) xml() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<MidiClip Id="0" Time="0"><Name Value="%s" /><Notes><KeyTracks>`, c.name)
	keys := []int{}
	byKey := map[int][]tNote{}
	for _, nt := range c.notes {
		if _, ok := byKey[nt.key]; !ok {
			keys = append(keys, nt.key)
		}
		byKey[nt.key] = append(byKey[nt.key], nt)
	}
	maxID := 0
	for i, k := range keys {
		fmt.Fprintf(&b, `<KeyTrack Id="%d"><Notes>`, i)
		for _, nt := range byKey[k] {
			fmt.Fprintf(&b, `<MidiNoteEvent Time="%s" Duration="0.25" Velocity="%d" OffVelocity="64" NoteId="%d" />`, nt.time, nt.vel, nt.id)
			maxID = max(maxID, nt.id)
		}
		fmt.Fprintf(&b, `</Notes><MidiKey Value="%d" /></KeyTrack>`, k)
	}
	fmt.Fprintf(&b, `</KeyTracks><NoteIdGenerator><NextId Value="%d" /></NoteIdGenerator></Notes></MidiClip>`, maxID+1)
	return b.String()
}

func (s tSet) doc(t *testing.T) *Doc {
	t.Helper()
	d, err := ParseXML(strings.NewReader(s.xml()))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, s.xml())
	}
	return d
}

// copy robi gleboka kopie, zeby warianty ours/theirs nie dzielily slicow.
func (s tSet) copy() tSet {
	c := s
	c.tracks = make([]tTrack, len(s.tracks))
	for i, t := range s.tracks {
		t.slots = make([]*tClip, len(s.tracks[i].slots))
		for j, cl := range s.tracks[i].slots {
			if cl != nil {
				cc := *cl
				cc.notes = append([]tNote(nil), cl.notes...)
				t.slots[j] = &cc
			}
		}
		c.tracks[i] = t
	}
	return c
}

// baseSet: dwie sciezki (Melodia, Hats), kazda z jednym klipem w slocie 0
// i dwoma wolnymi slotami.
func baseSet() tSet {
	return tSet{next: 1000, tracks: []tTrack{
		{id: 10, name: "Melodia", slots: []*tClip{{name: "mel", notes: []tNote{n(1, 60, "0", 100), n(2, 62, "1", 100), n(3, 64, "2", 100)}}, nil, nil}},
		{id: 11, name: "Hats", slots: []*tClip{{name: "hh", notes: []tNote{n(1, 42, "0", 90), n(2, 42, "0.5", 70)}}, nil, nil}},
	}}
}

// notesOf zwraca nuty klipu w slocie jako "id:key@time v=vel".
func notesOf(t *testing.T, d *Doc, track string, slot int) []string {
	t.Helper()
	for _, tr := range tracks(d) {
		if trackName(tr) != track {
			continue
		}
		list := tr.Find("DeviceChain", "MainSequencer", "ClipSlotList")
		v := list.Children[slot].Find("ClipSlot", "Value")
		if len(v.Children) == 0 {
			return nil
		}
		var out []string
		for _, kt := range v.Children[0].Find("Notes", "KeyTracks").Children {
			for _, ev := range kt.Child("Notes").Children {
				id, _ := ev.Attr("NoteId")
				tm, _ := ev.Attr("Time")
				vel, _ := ev.Attr("Velocity")
				out = append(out, fmt.Sprintf("%s:%s@%s v=%s", id, kt.Value("MidiKey"), tm, vel))
			}
		}
		return out
	}
	t.Fatalf("brak sciezki %q", track)
	return nil
}

func eqStrings(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }
