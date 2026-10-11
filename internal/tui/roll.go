package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/c-airr/ableton-collab/als"
	"github.com/c-airr/ableton-collab/internal/live"

	"github.com/charmbracelet/lipgloss"
)

// preview: podglad jednej strony konfliktu - dla klipu MIDI mini piano roll,
// dla reszty lista zmian.
func preview(n *als.Node, changes []als.Change, w, h int) []string {
	if n == nil {
		return []string{sErr.Render("(deleted)")}
	}
	if n.Name == "MidiClip" {
		if roll := pianoRoll(n, w, h); roll != nil {
			return roll
		}
	}
	var out []string
	if n.Name == "AudioClip" {
		if s := n.Value("Name"); s != "" {
			out = append(out, sBold.Render(trunc(s, w)))
		}
	}
	for i, c := range changes {
		if i == h-len(out) {
			out = append(out, sDim.Render(fmt.Sprintf("… %d more", len(changes)-i)))
			break
		}
		out = append(out, trunc(shortDetail(c), w))
	}
	if len(out) == 0 {
		out = append(out, sDim.Render("(changed)"))
	}
	return out
}

func shortDetail(c als.Change) string {
	s := c.Kind
	if c.Detail != "" {
		s += ": " + c.Detail
	} else {
		s += " " + c.Label
	}
	return s
}

type midiNote struct {
	key           int
	start, length float64
	vel           float64
}

func clipNotes(n *als.Node) []midiNote {
	var out []midiNote
	n.Walk(func(kt *als.Node) {
		if kt.Name != "KeyTrack" {
			return
		}
		key, err := strconv.Atoi(kt.Value("MidiKey"))
		if err != nil {
			return
		}
		kt.Walk(func(ev *als.Node) {
			if ev.Name != "MidiNoteEvent" {
				return
			}
			t, _ := ev.Attr("Time")
			d, _ := ev.Attr("Duration")
			v, _ := ev.Attr("Velocity")
			nt := midiNote{key: key}
			nt.start, _ = strconv.ParseFloat(t, 64)
			nt.length, _ = strconv.ParseFloat(d, 64)
			nt.vel, _ = strconv.ParseFloat(v, 64)
			out = append(out, nt)
		})
	})
	return out
}

// clipRange: zakres petli klipu w beatach (do skali podgladu).
func clipRange(n *als.Node, notes []midiNote) (float64, float64) {
	num := func(path ...string) (float64, bool) {
		v, err := strconv.ParseFloat(n.Value(path...), 64)
		return v, err == nil
	}
	start, ok1 := num("Loop", "LoopStart")
	end, ok2 := num("Loop", "LoopEnd")
	if !ok1 || !ok2 || end <= start {
		start, end = 0, 0
		for _, nt := range notes {
			end = max(end, nt.start+nt.length)
		}
	}
	if end <= start {
		end = start + 4
	}
	return start, end
}

var noteNames = []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// noteName: jak w Live (60 = C3).
func noteName(k int) string { return noteNames[k%12] + strconv.Itoa(k/12-2) }

// pianoRoll: podglad klipu z pliku .als.
func pianoRoll(n *als.Node, w, h int) []string {
	notes := clipNotes(n)
	start, end := clipRange(n, notes)
	return rollLines(notes, start, end, w, h)
}

// livePreview: podglad stanu ze skryptu Live (klip, mikser albo tempo).
func livePreview(o live.Obj, w, h int) []string {
	if o == nil {
		return []string{sErr.Render("(deleted)")}
	}
	if empty, _ := o["empty"].(bool); empty {
		return []string{sErr.Render("(no clip)")}
	}
	num := func(k string) float64 { v, _ := o[k].(float64); return v }
	if raw, ok := o["notes"].([]any); ok {
		var notes []midiNote
		for _, x := range raw {
			n, ok := x.([]any)
			if !ok || len(n) < 4 {
				continue
			}
			f := func(i int) float64 { v, _ := n[i].(float64); return v }
			notes = append(notes, midiNote{key: int(f(0)), start: f(1), length: f(2), vel: f(3)})
		}
		start, end := 0.0, 0.0
		if lp, ok := o["loop"].([]any); ok && len(lp) == 2 {
			start, _ = lp[0].(float64)
			end, _ = lp[1].(float64)
		}
		if end <= start {
			for _, nt := range notes {
				end = max(end, nt.start+nt.length)
			}
		}
		if end <= start {
			end = start + 4
		}
		return rollLines(notes, start, end, w, h)
	}
	if _, ok := o["bpm"]; ok {
		return []string{fmt.Sprintf("%g BPM", num("bpm"))}
	}
	if _, ok := o["vol"]; ok {
		mute := "off"
		if m, _ := o["mute"].(bool); m {
			mute = "on"
		}
		return []string{fmt.Sprintf("volume %.2f", num("vol")), fmt.Sprintf("pan %+.2f", num("pan")), "mute " + mute}
	}
	return []string{sDim.Render("(changed)")}
}

// rollLines: wiersze = wysokosci dzwieku (wysokie u gory), kolumny = czas.
func rollLines(notes []midiNote, start, end float64, w, h int) []string {
	if len(notes) == 0 {
		return []string{sDim.Render("(empty clip)")}
	}
	keys := map[int]int{}
	for _, nt := range notes {
		keys[nt.key]++
	}
	var ks []int
	for k := range keys {
		ks = append(ks, k)
	}
	// za duzo wysokosci - zostaja najczesciej uzywane
	rows := h - 1
	if len(ks) > rows {
		sort.Slice(ks, func(i, j int) bool { return keys[ks[i]] > keys[ks[j]] })
		ks = ks[:rows]
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ks)))
	cols := max(8, w-5)
	step := (end - start) / float64(cols)
	cell := lipgloss.NewStyle().Foreground(cAccent)
	var out []string
	for _, k := range ks {
		row := make([]string, cols)
		for c := range row {
			t := start + float64(c)*step
			if c > 0 && int(t) != int(t-step) {
				row[c] = sDim.Render("┊")
			} else {
				row[c] = " "
			}
		}
		for _, nt := range notes {
			if nt.key != k {
				continue
			}
			from := int((nt.start - start) / step)
			to := int((nt.start + nt.length - start) / step)
			// ostatnia kolumna dluzszej nuty zostaje pusta - sasiednie nuty
			// (np. szesnastki hihatu) nie zlewaja sie w jeden pasek
			last := to - 1
			if to-from >= 2 {
				last = to - 2
			}
			for c := max(0, from); c <= min(cols-1, max(from, last)); c++ {
				ch := "█"
				if nt.vel < 50 {
					ch = "▓"
				}
				row[c] = cell.Render(ch)
			}
		}
		out = append(out, sDim.Render(fmt.Sprintf("%-4s", noteName(k)))+" "+strings.Join(row, ""))
	}
	out = append(out, sDim.Render(fmt.Sprintf("     %d notes · %.0f beats", len(notes), end-start)))
	return out
}
