package als

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Testy na prawdziwych plikach z Live 12.4.6 (testdata/, opis w RAPORT.md):
//   ours            = base + nowa sciezka "8-12 String Guitar"
//   theirs          = base + zmienione nuty w klipie Hats
//   theirs_conflict = base + ta sama gitara dodana niezaleznie

func load(t *testing.T, name string) *Doc {
	t.Helper()
	p := filepath.Join("..", "testdata", name)
	if _, err := os.Stat(p); err != nil {
		t.Skip("brak " + p)
	}
	d, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRealMergeOursTheirs(t *testing.T) {
	b, o, th := load(t, "base.als"), load(t, "ours.als"), load(t, "theirs.als")
	r, err := Merge(b, o, th)
	if err != nil || len(r.Conflicts) > 0 {
		t.Fatalf("err=%v konflikty=%v", err, r.Conflicts)
	}
	if errs := Validate(r.Doc); len(errs) > 0 {
		t.Fatal(errs)
	}
	// wzgledem ours: tylko zmiany od theirs (klip Hats)
	for _, c := range Diff(o, r.Doc) {
		if !strings.Contains(c.Label, `MidiTrack "Hats"`) {
			t.Errorf("nieoczekiwana zmiana wzgledem ours: %s", c)
		}
	}
	// wzgledem theirs: tylko dodana gitara
	ch := Diff(th, r.Doc)
	if len(ch) != 1 || ch[0].Kind != "added" || !strings.Contains(ch[0].Label, "8-12 String Guitar") {
		t.Errorf("wzgledem theirs: %v", ch)
	}
	// scalanie jest symetryczne
	r2, err := Merge(b, th, o)
	if err != nil || r2.Doc == nil {
		t.Fatalf("odwrotnie: %v %v", err, r2.Conflicts)
	}
	if d := Diff(r.Doc, r2.Doc); len(d) > 0 {
		t.Errorf("merge(ours,theirs) != merge(theirs,ours): %v", d)
	}
	// zapis i odczyt wyniku nic nie zmienia
	p := filepath.Join(t.TempDir(), "m.als")
	if err := r.Doc.WriteFile(p); err != nil {
		t.Fatal(err)
	}
	back, err := ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(back.XMLBytes()) != string(r.Doc.XMLBytes()) {
		t.Error("wynik zmienia sie po zapisie i odczycie")
	}
}

// Obie strony dodaly te sama gitare: Id pointee 24006.. kolidowaly, w wyniku
// sa obie sciezki, unikalne Id i ostrzezenie o duplikacie.
func TestRealDuplicateGuitar(t *testing.T) {
	b, o, th := load(t, "base.als"), load(t, "ours.als"), load(t, "theirs_conflict.als")
	r, err := Merge(b, o, th)
	if err != nil || len(r.Conflicts) > 0 {
		t.Fatalf("err=%v konflikty=%v", err, r.Conflicts)
	}
	if errs := Validate(r.Doc); len(errs) > 0 {
		t.Fatal(errs)
	}
	if got, want := len(tracks(r.Doc)), len(tracks(b))+2; got != want {
		t.Errorf("sciezek %d, oczekiwane %d", got, want)
	}
	if np := nextPointee(r.Doc); np != 24967+(24545-24006) {
		t.Errorf("NextPointeeId = %d", np)
	}
	if len(r.Warnings) != 1 || r.Warnings[0].Kind != "duplicate-track" {
		t.Errorf("ostrzezenia: %v", r.Warnings)
	}
}

// Diff na prawdziwych plikach pokazuje tylko faktyczne zmiany, bez szumu zapisu.
func TestRealDiffIsClean(t *testing.T) {
	b := load(t, "base.als")
	ch := Diff(b, load(t, "theirs.als"))
	for _, c := range ch {
		if !strings.Contains(c.Label, `MidiTrack "Hats"`) {
			t.Errorf("szum w diffie: %s", c)
		}
	}
	if len(ch) != 6 { // 5 usunietych nut + dodany KeyTrack 59
		t.Errorf("zmian %d: %v", len(ch), ch)
	}
	if ch := Diff(b, load(t, "ours.als")); len(ch) != 1 {
		t.Errorf("ours: %v", ch)
	}
}
