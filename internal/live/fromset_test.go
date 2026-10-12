package live

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c-airr/ableton-collab/als"
)

func readSet(t *testing.T, name string) *als.Doc {
	t.Helper()
	d, err := als.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Stan z pliku: klipy MIDI, tempo, mikser - w ksztalcie jak ze skryptu.
func TestSetItems(t *testing.T) {
	it := SetItems(readSet(t, "base.als"))
	if it["tempo"]["bpm"] != 140.0 {
		t.Fatalf("tempo: %v", it["tempo"])
	}
	c := it["clip/2/s0"]
	if c == nil || c["track"] != "Hats" || c["midi"] != true {
		t.Fatalf("Hats clip: %v", c)
	}
	ns := c["notes"].([]any)
	if len(ns) == 0 {
		t.Fatal("no notes")
	}
	n := ns[0].([]any)
	if len(n) != 8 || n[4] != false || n[5] != 1.0 {
		t.Fatalf("note: %v", n)
	}
	if _, ok := it["mixer/0"]["pan"].(float64); !ok {
		t.Fatalf("mixer: %v", it["mixer/0"])
	}
}

// Miedzy base a theirs zmienily sie tylko nuty klipu Hats (trzecia sciezka).
func TestCatchupItems(t *testing.T) {
	items := CatchupItems(readSet(t, "base.als"), readSet(t, "theirs.als"))
	if len(items) != 1 || items[0].Key != "clip/2/s0" {
		var keys []string
		for _, c := range items {
			keys = append(keys, c.Key)
		}
		t.Fatalf("items: %v", keys)
	}
}

func TestPyFloat(t *testing.T) {
	for x, want := range map[float64]string{4: "4.0", 0.25: "0.25", 3.253403: "3.253403", 0: "0.0", 1e-06: "1e-06", 128.5: "128.5"} {
		if got := pyFloat(x); got != want {
			t.Errorf("pyFloat(%v) = %q, want %q", x, got, want)
		}
	}
}

func note(p, at float64) any { return []any{p, at, 0.25, 100.0, false, 1.0, 0.0, 64.0} }

// Nuty: dodane i usuniete w wersji wchodza, lokalne niezapisane zostaja.
func TestMergeItem(t *testing.T) {
	base := Obj{"track": "Hats", "name": "hh", "notes": []any{note(60, 0), note(62, 1)}}
	head := Obj{"track": "Hats", "name": "hh2", "notes": []any{note(60, 0), note(67, 2)}}
	cur := Obj{"track": "Hats", "name": "hh", "color": 123.0, "notes": []any{note(60, 0), note(62, 1), note(64, 3)}}
	got := mergeItem("clip/0/s0", base, head, cur)
	if got == nil {
		t.Fatal("nothing to apply")
	}
	want := []any{note(60, 0), note(64, 3), note(67, 2)}
	if !sameNotes(got["notes"], want) || got["name"] != "hh2" || got["color"] != 123.0 {
		t.Fatalf("merged: %v", got)
	}
	// Live juz to ma (przyszlo na zywo) - nic
	if o := mergeItem("clip/0/s0", base, head, Obj{"track": "Hats", "name": "hh2", "notes": []any{note(67, 2), note(60, 0)}}); o != nil {
		t.Fatalf("applied again: %v", o)
	}
	// nowy klip w slocie, ktorego Live nie ma
	if o := mergeItem("clip/0/s1", nil, head, nil); o == nil || o["name"] != "hh2" {
		t.Fatalf("new clip: %v", o)
	}
	// usuniety w wersji, w Live bez zmian - usuwamy; zmieniony w Live - zostaje
	if o := mergeItem("clip/0/s0", base, nil, Obj{"track": "Hats", "name": "hh", "notes": []any{note(62, 1), note(60, 0)}}); o == nil || o["empty"] != true {
		t.Fatalf("removed clip: %v", o)
	}
	if o := mergeItem("clip/0/s0", base, nil, cur); o != nil {
		t.Fatalf("removed a clip changed here: %v", o)
	}
	// tempo
	if o := mergeItem("tempo", Obj{"bpm": 120.0}, Obj{"bpm": 128.0}, Obj{"bpm": 120.0}); o == nil || o["bpm"] != 128.0 {
		t.Fatalf("tempo: %v", o)
	}
}

// Silnik nanosi zmiany z wersji w Live, scalone z tym, co tam jest.
func TestEngineCatchup(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".abcollab")
	os.MkdirAll(filepath.Join(root, "devices", "AAA"), 0o755)
	cur := Obj{"track": "Hats", "name": "hh", "midi": true, "notes": []any{note(60, 0), note(64, 3)}}
	fl := newFakeLive(t, []string{"Hats"}, map[string]Obj{"clip/0/s0": cur})
	cl := Dial(fl.addr(), "tok")
	defer cl.Close()
	e := Start(cl, root, "AAA", "", func(d string) string { return d }, func() []string { return []string{"Hats"} })
	defer e.Close()
	logs := drain(e, "log")
	waitFor(t, "matched", func() bool { return e.Status().Matched })
	e.Catchup([]Catch{{Key: "clip/0/s0",
		Base: Obj{"track": "Hats", "name": "hh", "notes": []any{note(60, 0)}},
		Head: Obj{"track": "Hats", "name": "hh", "notes": []any{note(60, 0), note(67, 2)}}}})
	waitFor(t, "applied", func() bool {
		return sameNotes(fl.get("clip/0/s0")["notes"], []any{note(60, 0), note(64, 3), note(67, 2)})
	})
	waitFor(t, "logged", func() bool { n := logs(); return n != nil })
	// zmiana nie wraca do innych jako operacja (oni ja juz maja w wersji)
	time.Sleep(200 * time.Millisecond)
	if ents, _ := os.ReadDir(filepath.Join(root, "devices", "AAA", "live")); len(ents) != 0 {
		t.Fatalf("sent %d op(s)", len(ents))
	}
}
