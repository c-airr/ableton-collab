package live

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/c-airr/ableton-collab/als"
)

// --- stan z pliku seta -------------------------------------------------------------------
//
// Zmiany innych przychodza na dwa sposoby: na zywo (operacje od ich skryptu)
// albo w zapisanej wersji (np. gdy ich Live nie byl polaczony). Te drugie
// nanosimy w otwartym Live'ie tak samo jak operacje - stan kluczy czytamy
// wtedy z pliku seta, w ksztalcie jak ze skryptu. Tylko pola, ktore da sie
// odczytac wiernie: kolor klipu (plik ma indeks palety, skrypt RGB) i
// glosnosc/sendy (plik ma wzmocnienie, API wartosc suwaka) zostaja na Reload.

// SetItems: klucze pracy na zywo w secie - klipy MIDI (sloty i aranzacja),
// panorama i wyciszenie sciezek, tempo.
func SetItems(d *als.Doc) map[string]Obj {
	out := map[string]Obj{}
	ls := d.Root.Child("LiveSet")
	if ls == nil {
		return out
	}
	main := ls.Child("MainTrack")
	if main == nil {
		main = ls.Child("MasterTrack")
	}
	if main != nil {
		if v, ok := num(main.Value("DeviceChain", "Mixer", "Tempo", "Manual")); ok {
			out["tempo"] = norm(Obj{"bpm": v})
		}
	}
	tracks := ls.Child("Tracks")
	if tracks == nil {
		return out
	}
	ti := 0
	for _, t := range tracks.Children {
		if t.Name == "ReturnTrack" {
			continue
		}
		name := t.Value("Name", "EffectiveName")
		if mx := t.Find("DeviceChain", "Mixer"); mx != nil {
			o := Obj{"track": name}
			if v, ok := num(mx.Value("Pan", "Manual")); ok {
				o["pan"] = v
			}
			if v := mx.Value("Speaker", "Manual"); v != "" {
				o["mute"] = v != "true"
			}
			out[fmt.Sprintf("mixer/%d", ti)] = norm(o)
		}
		seq := t.Find("DeviceChain", "MainSequencer")
		if seq != nil && t.Name == "MidiTrack" {
			if slots := seq.Child("ClipSlotList"); slots != nil {
				for _, cs := range slots.Children {
					id, _ := cs.Attr("Id")
					if c := cs.Find("ClipSlot", "Value", "MidiClip"); c != nil {
						out[fmt.Sprintf("clip/%d/s%s", ti, id)] = clipObj(name, c)
					}
				}
			}
			if evs := seq.Find("ClipTimeable", "ArrangerAutomation", "Events"); evs != nil {
				for _, c := range evs.Children {
					if c.Name != "MidiClip" {
						continue
					}
					at, _ := c.Attr("Time")
					if v, ok := num(at); ok {
						out[fmt.Sprintf("clip/%d/a%s", ti, pyFloat(v))] = clipObj(name, c)
					}
				}
			}
		}
		ti++
	}
	return out
}

func clipObj(track string, c *als.Node) Obj {
	o := Obj{"track": track, "name": c.Value("Name"), "midi": true}
	ls, _ := num(c.Value("Loop", "LoopStart"))
	le, _ := num(c.Value("Loop", "LoopEnd"))
	looping := c.Value("Loop", "LoopOn") == "true"
	o["loop"] = []any{ls, le}
	o["looping"] = looping
	o["len"] = round6(le - ls)
	if !looping {
		cs, _ := num(c.Value("CurrentStart"))
		ce, _ := num(c.Value("CurrentEnd"))
		o["len"] = round6(ce - cs)
	}
	notes := []any{}
	if kts := c.Find("Notes", "KeyTracks"); kts != nil {
		for _, kt := range kts.Children {
			pitch, ok := num(kt.Value("MidiKey"))
			if !ok {
				continue
			}
			ns := kt.Child("Notes")
			if ns == nil {
				continue
			}
			for _, n := range ns.Children {
				if n.Name != "MidiNoteEvent" {
					continue
				}
				enabled, ok := n.Attr("IsEnabled")
				notes = append(notes, []any{pitch, attr(n, "Time", 0), attr(n, "Duration", 0),
					attr(n, "Velocity", 100), ok && enabled == "false",
					attr(n, "Probability", 1), attr(n, "VelocityDeviation", 0), attr(n, "OffVelocity", 64)})
			}
		}
	}
	o["notes"] = notes
	return norm(o)
}

// --- liczby jak w skrypcie (round(x, 6), repr floata w Pythonie) -----------------------

func num(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return round6(v), true
}

func attr(n *als.Node, name string, def float64) float64 {
	if v, ok := n.Attr(name); ok {
		if f, ok := num(v); ok {
			return f
		}
	}
	return def
}

// round6: jak round(x, 6) w Pythonie (zaokraglenie zapisu dziesietnego).
func round6(x float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 6, 64), 64)
	return v
}

// pyFloat: repr(float) z Pythona - klucz klipu w aranzacji ("4.0", "0.25").
func pyFloat(x float64) string {
	if x == math.Trunc(x) && math.Abs(x) < 1e16 {
		return strconv.FormatFloat(x, 'f', 1, 64)
	}
	if a := math.Abs(x); a < 1e-4 || a >= 1e16 {
		return strconv.FormatFloat(x, 'e', -1, 64)
	}
	return strconv.FormatFloat(x, 'f', -1, 64)
}

// norm: ten sam ksztalt co obiekt odczytany z JSON-a (liczby float64, listy []any).
func norm(o Obj) Obj {
	data, _ := json.Marshal(o)
	var out Obj
	json.Unmarshal(data, &out)
	return out
}

// --- scalanie stanu z zapisanej wersji z tym, co jest w Live -------------------------------

// Catch: klucz zmieniony w nowszej wersji wzgledem tej, ktora Live ma
// wczytana. Base/Head - stan w obu wersjach (nil - klucza nie bylo).
type Catch struct {
	Key        string
	Base, Head Obj
}

// CatchupItems: zmiany kluczy pracy na zywo miedzy dwiema wersjami seta.
func CatchupItems(loaded, head *als.Doc) []Catch {
	a, b := SetItems(loaded), SetItems(head)
	var out []Catch
	for k, h := range b {
		if Hash(a[k]) != Hash(h) {
			out = append(out, Catch{Key: k, Base: a[k], Head: h})
		}
	}
	for k, o := range a {
		if _, ok := b[k]; !ok {
			out = append(out, Catch{Key: k, Base: o})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// mergeItem: co nanosic w Live, zeby mial zmiane z wersji, nie tracac
// wlasnych niezapisanych zmian (cur). nil - nic do zrobienia (juz to ma albo
// zmiana sie wyklucza z lokalna, np. klip usuniety tu, a zmieniony tam).
func mergeItem(key string, base, head, cur Obj) Obj {
	inLive := cur != nil && cur["empty"] != true
	clip := strings.HasPrefix(key, "clip/")
	switch {
	case head == nil: // usuniety w wersji
		if !clip || !inLive || base == nil || !strings.Contains(key, "/s") {
			return nil
		}
		if !sameFields(base, cur) || !sameNotes(base["notes"], cur["notes"]) {
			return nil // zmieniony tutaj - zostaje
		}
		return Obj{"track": cur["track"], "empty": true}
	case !inLive: // dodany w wersji
		if !clip || base != nil || !strings.Contains(key, "/s") {
			return nil
		}
		return copyObj(head)
	}
	if base == nil {
		base = Obj{}
	}
	out := copyObj(cur)
	changed := false
	for f, hv := range head {
		if f == "track" || f == "notes" || f == "len" || f == "midi" {
			continue
		}
		if Hash(Obj{"v": base[f]}) != Hash(Obj{"v": hv}) && Hash(Obj{"v": out[f]}) != Hash(Obj{"v": hv}) {
			out[f], changed = hv, true
		}
	}
	if hn, ok := head["notes"].([]any); ok {
		cn, _ := cur["notes"].([]any)
		bn, _ := base["notes"].([]any)
		merged := mergeNotes(bn, hn, cn)
		if !sameNotes(merged, cn) {
			out["notes"], changed = merged, true
		}
	}
	if !changed {
		return nil
	}
	return out
}

func copyObj(o Obj) Obj { return norm(o) }

// sameFields: te same wlasciwosci klipu (bez nut i pol, ktorych plik nie ma).
func sameFields(file, live Obj) bool {
	for f, v := range file {
		if f == "notes" || f == "len" || f == "midi" {
			continue
		}
		if Hash(Obj{"v": v}) != Hash(Obj{"v": live[f]}) {
			return false
		}
	}
	return true
}

func noteKey(n any) string {
	data, _ := json.Marshal(n)
	return string(data)
}

// mergeNotes: trojstronne scalenie nut, nuta po nucie (liczba takich samych
// nut): zmienione tylko w wersji - jak w wersji, tylko w Live - jak w Live.
func mergeNotes(base, head, cur []any) []any {
	b, h, c := map[string]int{}, map[string]int{}, map[string]int{}
	note := map[string]any{}
	for _, x := range []struct {
		ns []any
		m  map[string]int
	}{{base, b}, {head, h}, {cur, c}} {
		for _, n := range x.ns {
			k := noteKey(n)
			x.m[k]++
			note[k] = n
		}
	}
	out := []any{}
	for k, n := range note {
		want := c[k]
		switch {
		case c[k] == b[k]:
			want = h[k]
		case h[k] == b[k]:
		default:
			want = max(c[k], h[k]) // zmienione po obu stronach
		}
		for i := 0; i < want; i++ {
			out = append(out, n)
		}
	}
	sortNotes(out)
	return out
}

func sameNotes(a, b any) bool {
	x, _ := a.([]any)
	y, _ := b.([]any)
	if len(x) != len(y) {
		return false
	}
	m := map[string]int{}
	for _, n := range x {
		m[noteKey(n)]++
	}
	for _, n := range y {
		k := noteKey(n)
		if m[k] == 0 {
			return false
		}
		m[k]--
	}
	return true
}

// sortNotes: kolejnosc jak sorted() w skrypcie (listy porownywane po kolei).
func sortNotes(ns []any) {
	val := func(v any) float64 {
		switch x := v.(type) {
		case float64:
			return x
		case bool:
			if x {
				return 1
			}
		}
		return 0
	}
	sort.SliceStable(ns, func(i, j int) bool {
		a, _ := ns[i].([]any)
		b, _ := ns[j].([]any)
		for k := 0; k < len(a) && k < len(b); k++ {
			if va, vb := val(a[k]), val(b[k]); va != vb {
				return va < vb
			}
		}
		return len(a) < len(b)
	})
}
